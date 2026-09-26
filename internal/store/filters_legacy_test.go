package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/filter"
)

// legacyFilterSQL stores a rule the way an earlier version accepted it: a regex that repeats a class
// 400 times, refused by today's repeat and cost limits. It bypasses validation on purpose.
const legacyFilterSQL = `INSERT INTO filters (name, enabled, scope, kind, terms, fields, action)
	VALUES (?, ?, 'global', 'regex', '["[a-z ]{1,400}[0-9]{3}"]', '["title","content"]', 'mute')`

func (e *env) legacyFilter(name string, enabled bool) int64 {
	e.t.Helper()
	e.exec(legacyFilterSQL, name, boolInt(enabled))
	return int64(e.count("SELECT max(id) FROM filters"))
}

func findFilter(t *testing.T, fs []Filter, id int64) Filter {
	t.Helper()
	for _, f := range fs {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("filter %d not listed", id)
	return Filter{}
}

// Review finding: one stored rule that no longer compiles made every create, patch and preview fail
// (with its message shown on whatever rule was being edited). Now only the edited rule is judged,
// and the stored one is switched off visibly with its reason.
func TestLegacyFilterDoesNotBlockOtherEdits(t *testing.T) {
	e := newEnv(t)
	on := e.legacyFilter("old on", true)
	off := e.legacyFilter("old off", false)

	created, err := e.db.CreateFilter(e.ctx, newFilter("mute", "sponsored"))
	require.NoError(t, err, "a legacy rule must not block creating another")
	require.Nil(t, created.DisabledReason)

	_, found, err := e.db.UpdateFilter(e.ctx, created.ID, func(f *Filter) error { f.Name = "renamed"; return nil })
	require.NoError(t, err)
	require.True(t, found)

	_, err = e.db.PreviewFilter(e.ctx, newFilter("mute", "other"), false, time.Second)
	require.NoError(t, err)

	require.Zero(t, e.count("SELECT enabled FROM filters WHERE id = ?", on), "the enabled legacy rule is switched off")
	fs, err := e.db.ListFilters(e.ctx)
	require.NoError(t, err)
	for _, id := range []int64{on, off} {
		f := findFilter(t, fs, id)
		require.False(t, f.Enabled)
		require.NotNil(t, f.DisabledReason, "filter %d", id)
		require.Contains(t, *f.DisabledReason, "repeats")
		require.Contains(t, *f.DisabledReason, "re-enable")
	}
	require.Nil(t, findFilter(t, fs, created.ID).DisabledReason)

	// Editing the legacy rule itself is judged by today's limits, and the error is about it.
	_, _, err = e.db.UpdateFilter(e.ctx, on, func(f *Filter) error { f.Enabled = true; return nil })
	var fe *filter.Error
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "terms[0]", fe.Field)
	require.Contains(t, fe.Message, "repeats")

	// Fixing it (and turning it back on) clears the reason.
	fixed, _, err := e.db.UpdateFilter(e.ctx, on, func(f *Filter) error {
		f.Terms, f.Enabled = []string{`[a-z ]{1,40}[0-9]{3}`}, true
		return nil
	})
	require.NoError(t, err)
	require.True(t, fixed.Enabled)
	require.Nil(t, fixed.DisabledReason)
	fs, err = e.db.ListFilters(e.ctx)
	require.NoError(t, err)
	require.Nil(t, findFilter(t, fs, on).DisabledReason)
	require.NotNil(t, findFilter(t, fs, off).DisabledReason, "the other one keeps its reason")

	// Deleting the last one drops the stored reasons entirely.
	_, _, err = e.db.DeleteFilter(e.ctx, off, UnmuteKeep, nil)
	require.NoError(t, err)
	require.Zero(t, e.count("SELECT count(*) FROM settings WHERE key = ?", settingFilterReasons))
}

// The list notices a legacy rule lazily (a read on a database no write has touched since the upgrade).
func TestListFiltersDisablesLegacyRuleLazily(t *testing.T) {
	e := newEnv(t)
	on := e.legacyFilter("old", true)
	fs, err := e.db.ListFilters(e.ctx)
	require.NoError(t, err)
	f := findFilter(t, fs, on)
	require.False(t, f.Enabled)
	require.NotNil(t, f.DisabledReason)
	require.Zero(t, e.count("SELECT enabled FROM filters WHERE id = ?", on))
	got, ok, err := e.db.GetFilter(e.ctx, on)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, got.DisabledReason)
}

// At ingest the legacy rule used to be dropped silently while the list showed it enabled; now the
// first fetch commit switches it off with its reason, and the valid rules still run.
func TestIngestDisablesLegacyRuleVisibly(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	on := e.legacyFilter("old", true)
	e.exec(`INSERT INTO filters (name, enabled, scope, kind, terms, fields, action) VALUES ('ok', 1, 'global', 'text', '["sponsored"]', '["title"]', 'mute')`)
	info := e.fetchBody(id, frss(fspec{guid: "a", title: "Sponsored post"}, fspec{guid: "b", title: "Plain"}))
	require.Equal(t, 1, info.Muted)
	require.Zero(t, e.count("SELECT enabled FROM filters WHERE id = ?", on))
	reasons, err := loadFilterReasons(e.ctx, e.db.Reader())
	require.NoError(t, err)
	require.Contains(t, reasons[on], "repeats")
}

// A legacy set over the set-wide cost cap: the newest rules give way, visibly.
func TestSanitizeDisablesNewestPastTheSetCap(t *testing.T) {
	e := newEnv(t)
	const p = `[a-z ]{1,50}[0-9]{3}`
	heavy := filter.NewRule(filter.ScopeGlobal, filter.KindRegex, filter.ActionMute, p+"a", p+"b", p+"c")
	heavy.Fields = []filter.Field{filter.FieldContent}
	c, err := filter.CompileRule(heavy)
	require.NoError(t, err)
	n := filter.MaxRegexSetCost/c.Cost() + 2
	for i := 0; i < n; i++ {
		e.exec(`INSERT INTO filters (name, enabled, scope, kind, terms, fields, action) VALUES ('h', 1, 'global', 'regex', ?, '["content"]', 'mute')`,
			`["`+p+`a","`+p+`b","`+p+`c"]`)
	}
	changed, err := e.db.SanitizeFilters(e.ctx)
	require.NoError(t, err)
	require.True(t, changed)
	over := e.count("SELECT count(*) FROM filters WHERE enabled = 0")
	require.Equal(t, 2, over)
	maxOn := e.count("SELECT max(id) FROM filters WHERE enabled = 1")
	minOff := e.count("SELECT min(id) FROM filters WHERE enabled = 0")
	require.Less(t, maxOn, minOff, "the newest rules are the ones switched off")
	changed, err = e.db.SanitizeFilters(e.ctx)
	require.NoError(t, err)
	require.False(t, changed, "idempotent")
}
