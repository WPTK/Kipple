package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIconSiteKey(t *testing.T) {
	for _, c := range []struct{ site, feed, want string }{
		{"https://Example.com/a?sid=1", "https://feeds.example.net/rss", "example.com"},
		{"http://example.com./b#x", "", "example.com"},
		{"https://example.com:443/", "", "example.com"},
		{"http://example.com:8080/", "", "example.com:8080"},
		{"", "https://feeds.example.net/rss?x=1", "feeds.example.net"},
		{"javascript:alert(1)", "http://[::1]:81/feed", "[::1]:81"},
		{"/relative", "ftp://example.com/", ""},
	} {
		require.Equal(t, c.want, IconSiteKey(c.site, c.feed), c)
	}
}

// A site_url that changes only in scheme, path or query (a session id, a
// tracking parameter, http/https flapping) is the same site: it is not due
// before next_check_at, and its failure count carries over.
func TestNextIconJobSameSiteKeepsBackoff(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now().Unix()
	set := e.db.FetchSettings(e.ctx)
	f, err := e.db.AddFeed(e.ctx, NewFeed{URL: "https://feeds.example.com/rss"})
	require.NoError(t, err)
	e.exec("UPDATE feeds SET last_success_at = ?, site_url = 'https://example.com/?sid=1' WHERE id = ?", now, f)
	job, ok, err := e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "feeds.example.com", job.FeedHost)
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, nil, "boom", 3, now, now+3600))

	for _, site := range []string{"https://example.com/?sid=2", "http://example.com/?sid=3", "https://EXAMPLE.com/other/path"} {
		e.exec("UPDATE feeds SET site_url = ? WHERE id = ?", site, f)
		_, ok, err = e.db.NextIconJob(e.ctx, set, now, nil)
		require.NoError(t, err)
		require.False(t, ok, "%s: the same site is not due again", site)
	}
	job, ok, err = e.db.NextIconJob(e.ctx, set, now+3600, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 3, job.Failures, "failures are kept")

	// A lookup that ends after the link flapped again is still recorded.
	e.exec("UPDATE feeds SET site_url = 'https://example.com/?sid=9' WHERE id = ?", f)
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, nil, "boom", 4, now+3600, now+7200))
	require.Equal(t, 4, e.count("SELECT failures FROM feed_icon_checks WHERE feed_id = ?", f))

	// Another host is another site: due at once, failures start over.
	e.exec("UPDATE feeds SET site_url = 'https://blog.example.com/' WHERE id = ?", f)
	job, ok, err = e.db.NextIconJob(e.ctx, set, now+3600, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Zero(t, job.Failures)
}

// The feed's own host is part of the check's identity: the network exceptions
// are scoped to it, so a feed that moves to another host is looked up again at
// once even when site_url stays. A path change of the feed URL is not a move.
func TestNextIconJobFeedHostChange(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now().Unix()
	set := e.db.FetchSettings(e.ctx)
	f, err := e.db.AddFeed(e.ctx, NewFeed{URL: "https://feeds.example.com/rss"})
	require.NoError(t, err)
	e.exec("UPDATE feeds SET last_success_at = ?, site_url = 'https://example.com/' WHERE id = ?", now, f)
	job, ok, err := e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, nil, "x", 2, now, now+86400))

	e.exec("UPDATE feeds SET url = 'https://feeds.example.com/rss2', url_key = 'feeds.example.com/rss2' WHERE id = ?", f)
	_, ok, err = e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.False(t, ok, "same feed host: not due")

	e.exec("UPDATE feeds SET url = 'https://nas.lan/rss', url_key = 'nas.lan/rss', host = 'nas.lan' WHERE id = ?", f)
	job, ok, err = e.db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.True(t, ok, "the feed moved to another host")
	require.Equal(t, "nas.lan", job.FeedHost)
	require.Zero(t, job.Failures)

	// A lookup read before the move writes nothing after it.
	stale := job
	stale.FeedURL = "https://feeds.example.com/rss"
	require.NoError(t, e.db.SaveIconCheck(e.ctx, stale, &IconResult{Data: []byte{1}, ContentType: "image/png", Hash: "h"}, "", 0, now, now+1))
	require.Zero(t, e.count("SELECT count(*) FROM feed_icons"))
}

func TestNextIconJobSkip(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now().Unix()
	a, err := e.db.AddFeed(e.ctx, NewFeed{URL: "https://a.example.com/feed"})
	require.NoError(t, err)
	b, err := e.db.AddFeed(e.ctx, NewFeed{URL: "https://b.example.com/feed"})
	require.NoError(t, err)
	e.exec("UPDATE feeds SET last_success_at = ?", now)
	job, ok, err := e.db.NextIconJob(e.ctx, e.db.FetchSettings(e.ctx), now, func(j IconJob) bool { return j.FeedID == a })
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, b, job.FeedID)
	_, ok, err = e.db.NextIconJob(e.ctx, e.db.FetchSettings(e.ctx), now, func(IconJob) bool { return true })
	require.NoError(t, err)
	require.False(t, ok)
}

// A recheck that finds the same icon does not rewrite its bytes; fetched_at
// and source_url still move.
func TestSaveIconCheckKeepsAnUnchangedIcon(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now().Unix()
	f, err := e.db.AddFeed(e.ctx, NewFeed{URL: "https://b.example.com/feed"})
	require.NoError(t, err)
	e.exec("UPDATE feeds SET last_success_at = ? WHERE id = ?", now, f)
	e.exec(`CREATE TABLE icon_writes (n INTEGER)`)
	e.exec(`CREATE TRIGGER icon_data_write AFTER UPDATE OF data ON feed_icons BEGIN INSERT INTO icon_writes VALUES (1); END`)
	job, ok, err := e.db.NextIconJob(e.ctx, e.db.FetchSettings(e.ctx), now, nil)
	require.NoError(t, err)
	require.True(t, ok)

	icon := &IconResult{Data: []byte{1, 2, 3}, ContentType: "image/png", SourceURL: "https://b.example.com/a.png", Hash: "h1"}
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, icon, "", 0, now, now+1))
	same := *icon
	same.SourceURL = "https://b.example.com/b.png"
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, &same, "", 0, now+100, now+101))
	require.Zero(t, e.count("SELECT count(*) FROM icon_writes"), "same hash: data untouched")
	require.Equal(t, int(now+100), e.count("SELECT fetched_at FROM feed_icons WHERE feed_id = ?", f))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feed_icons WHERE source_url = 'https://b.example.com/b.png'"))

	changed := &IconResult{Data: []byte{9}, ContentType: "image/gif", SourceURL: "https://b.example.com/c.gif", Hash: "h2"}
	require.NoError(t, e.db.SaveIconCheck(e.ctx, job, changed, "", 0, now+200, now+201))
	require.Equal(t, 1, e.count("SELECT count(*) FROM icon_writes"), "a new icon is written")
	data, ct, ok, err := e.db.FeedIcon(e.ctx, f, "h2")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []byte{9}, data)
	require.Equal(t, "image/gif", ct)
}

// A populated schema-5 database (feeds with a fetch behind them, one stored
// icon) migrates to 6, and the finder's queries work on it at once.
func TestMigration0006OnPopulatedSchema5(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now().Unix()
	a, err := e.db.AddFeed(e.ctx, NewFeed{URL: "https://a.example.com/feed"})
	require.NoError(t, err)
	b, err := e.db.AddFeed(e.ctx, NewFeed{URL: "https://b.example.com/feed", AllowPrivateNet: true})
	require.NoError(t, err)
	_, err = e.db.AddFeed(e.ctx, NewFeed{URL: "https://c.example.com/feed"}) // never fetched
	require.NoError(t, err)
	e.exec("UPDATE feeds SET last_success_at = ?, site_url = 'https://b.example.com/' WHERE id IN (?, ?)", now, a, b)
	e.exec(`INSERT INTO feed_icons (feed_id, data, content_type, source_url, hash, fetched_at) VALUES (?, x'01', 'image/png', '', 'old', ?)`, a, now)
	e.exec(undo0006)
	e.exec(`PRAGMA user_version = 5`)
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	db, err := Open(e.ctx, Options{Path: path, Clock: e.clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	e.db = db
	v, err := db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, 6, v)
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backup", "pre-migration-5-*.db"))
	require.Len(t, snaps, 1)
	requireCleanIntegrity(t, db.Reader())
	require.Equal(t, 1, e.count("SELECT count(*) FROM feed_icons WHERE hash = 'old'"), "stored icons survive")

	set := db.FetchSettings(e.ctx)
	job, ok, err := db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, a, job.FeedID)
	require.NoError(t, db.SaveIconCheck(e.ctx, job, &IconResult{Data: []byte{2}, ContentType: "image/png", Hash: "new"}, "", 0, now, now+100))
	job, ok, err = db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, b, job.FeedID)
	require.True(t, job.AllowPrivateNet)
	require.Equal(t, "b.example.com", job.FeedHost)
	require.NoError(t, db.SaveIconCheck(e.ctx, job, nil, "x", 1, now, now+100))
	_, ok, err = db.NextIconJob(e.ctx, set, now, nil)
	require.NoError(t, err)
	require.False(t, ok, "the never-fetched feed is not due")
	require.Equal(t, 1, e.count("SELECT count(*) FROM feed_icons WHERE feed_id = ? AND hash = 'new'", a))
}
