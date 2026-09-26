package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// undo0006 is the first step of every downgrade helper: a schema-5 database has no feed_icon_checks.
const undo0006 = `DROP TABLE feed_icon_checks`

func TestMigration0006FeedIconChecks(t *testing.T) {
	db, _ := openTest(t)
	require.GreaterOrEqual(t, LatestVersion(), 6)
	require.Equal(t, 1, scalar[int](t, db.Reader(), "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'feed_icon_checks'"))
}

func TestNextIconJobDueRules(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now().Unix()
	set := e.db.FetchSettings(e.ctx)

	_, err := e.db.AddFeed(e.ctx, NewFeed{URL: "https://a.example.com/feed"})
	require.NoError(t, err)
	_, ok, err := e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.False(t, ok, "a feed that never fetched successfully is not due")

	f, err := e.db.AddFeed(e.ctx, NewFeed{URL: "https://b.example.com/feed", AllowPrivateNet: true})
	require.NoError(t, err)
	e.exec("UPDATE feeds SET last_success_at = ?, site_url = 'https://b.example.com/' WHERE id = ?", now, f)
	job, ok, err := e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, f, job.FeedID)
	require.Equal(t, "https://b.example.com/", job.SiteURL)
	require.True(t, job.AllowPrivateNet)
	require.Zero(t, job.Failures)

	// A failure backs off; the feed's health is untouched.
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, nil, "boom", 1, now, now+3600))
	_, ok, err = e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, 0, e.count("SELECT consecutive_failures FROM feeds WHERE id = ?", f))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND last_error IS NULL", f))

	job, ok, err = e.db.NextIconJob(e.ctx, set, now+3600, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, job.Failures, "failures carry over for the same site")

	// Success stores the icon and resets failures.
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, &IconResult{Data: []byte{1, 2}, ContentType: "image/png", SourceURL: "https://b.example.com/i.png", Hash: "h1"}, "", 0, now, now+7*86400))
	data, ct, ok, err := e.db.FeedIcon(e.ctx, f, "h1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []byte{1, 2}, data)
	require.Equal(t, "image/png", ct)
	_, ok, err = e.db.NextIconJob(e.ctx, set, now+86400, nil)
	require.NoError(t, err)
	require.False(t, ok)

	// A site change makes the feed due at once, with failures starting over.
	e.exec("UPDATE feed_icon_checks SET failures = 3 WHERE feed_id = ?", f)
	e.exec("UPDATE feeds SET site_url = 'https://c.example.com/' WHERE id = ?", f)
	job, ok, err = e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "https://c.example.com/", job.SiteURL)
	require.Zero(t, job.Failures)

	// With no site_url, a feed URL change counts as a site change.
	require.NoError(t, e.db.SaveIconCheck(e.ctx, IconJob{FeedID: f, FeedURL: job.FeedURL, SiteURL: job.SiteURL}, nil, "x", 1, now, now+3600))
	e.exec("UPDATE feeds SET site_url = '' WHERE id = ?", f)
	job, ok, err = e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, nil, "x", 1, now, now+3600))
	_, ok, err = e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.False(t, ok)
	e.exec("UPDATE feeds SET url = 'https://d.example.com/feed', url_key = 'd.example.com/feed' WHERE id = ?", f)
	_, ok, err = e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.True(t, ok)

	// Disabled feeds are never due.
	e.exec("UPDATE feeds SET enabled = 0 WHERE id = ?", f)
	_, ok, err = e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.False(t, ok)
}

// A lookup that finishes after the feed changed, or was deleted, writes nothing.
func TestSaveIconCheckSkipsAChangedOrDeletedFeed(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now().Unix()
	f, err := e.db.AddFeed(e.ctx, NewFeed{URL: "https://b.example.com/feed"})
	require.NoError(t, err)
	e.exec("UPDATE feeds SET last_success_at = ?, site_url = 'https://b.example.com/' WHERE id = ?", now, f)
	job, ok, err := e.db.NextIconJob(e.ctx, e.db.FetchSettings(e.ctx), now, nil)
	require.NoError(t, err)
	require.True(t, ok)
	icon := &IconResult{Data: []byte{1}, ContentType: "image/png", Hash: "h"}

	e.exec("UPDATE feeds SET site_url = 'https://other.example.com/' WHERE id = ?", f)
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, icon, "", 0, now, now+1))
	require.Equal(t, 0, e.count("SELECT count(*) FROM feed_icons"))
	require.Equal(t, 0, e.count("SELECT count(*) FROM feed_icon_checks"))

	e.exec("UPDATE feeds SET site_url = 'https://b.example.com/' WHERE id = ?", f)
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, icon, "", 0, now, now+1))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feed_icons"))
	e.exec("DELETE FROM feeds WHERE id = ?", f)
	require.Equal(t, 0, e.count("SELECT count(*) FROM feed_icons"), "cascade")
	require.Equal(t, 0, e.count("SELECT count(*) FROM feed_icon_checks"), "cascade")
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, icon, "", 0, now, now+1))
	require.Equal(t, 0, e.count("SELECT count(*) FROM feed_icons"))
}
