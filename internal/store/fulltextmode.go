package store

import (
	"context"
	"database/sql"
	"sync"
)

// SettingFulltextAll is the global full-text switch ("Fetch the full article
// for every feed"). Its semantics need no schema: the effective full-text mode
// of an item is
//
//	COALESCE(items.fulltext_mode, CASE WHEN <fetch.fulltext_all> THEN 1 ELSE feeds.fulltext END)
//
// so an item-level 1/0 always wins, and while the switch is on the per-feed flag
// no longer matters (a feed's own fulltext=0 does not override it). Flipping it
// rewrites nothing: it changes what new fetches extract and what every read path
// treats as full text from then on. EVERY place that needs the effective mode
// goes through EffectiveFulltext (Go) or FulltextModeSQL (SQL); do not spell the
// COALESCE out by hand.
const SettingFulltextAll = "fetch.fulltext_all"

// EffectiveFulltext is the effective full-text mode (0 or 1) of one item:
// itemMode is items.fulltext_mode (nil = follow), feed is feeds.fulltext, all is
// fetch.fulltext_all.
func EffectiveFulltext(itemMode *int, feed, all bool) int {
	switch {
	case itemMode != nil:
		return *itemMode
	case all, feed:
		return 1
	}
	return 0
}

// FulltextModeSQL is EffectiveFulltext as a SQL expression. itemCol is the
// items.fulltext_mode column reference and feedExpr an expression for the
// feed's fulltext flag (a column or a scalar subquery); neither is user input.
// The switch value is inlined as a literal so the planner sees a constant.
func FulltextModeSQL(itemCol, feedExpr string, all bool) string {
	if all {
		return "COALESCE(" + itemCol + ", 1)"
	}
	return "COALESCE(" + itemCol + ", " + feedExpr + ", 0)"
}

// boolCache holds one setting's value between changes. A load that raced an
// invalidate is discarded (generation check), so a stale value never outlives
// the PATCH that changed it.
type boolCache struct {
	mu  sync.Mutex
	gen uint64
	ok  bool
	val bool
}

func (c *boolCache) get(load func() bool) bool {
	c.mu.Lock()
	if c.ok {
		v := c.val
		c.mu.Unlock()
		return v
	}
	g := c.gen
	c.mu.Unlock()
	v := load()
	c.mu.Lock()
	if c.gen == g {
		c.val, c.ok = v, true
	}
	c.mu.Unlock()
	return v
}

func (c *boolCache) invalidate() {
	c.mu.Lock()
	c.gen++
	c.ok = false
	c.mu.Unlock()
}

// FulltextAll reports fetch.fulltext_all (default false), cached until
// SetSettings changes it.
func (d *DB) FulltextAll(ctx context.Context) bool {
	return d.ftAll.get(func() bool { return settingBool(ctx, d.reader, SettingFulltextAll, false) })
}

// txFulltextAll reads the switch inside a transaction (no cache: it must agree
// with the rest of that transaction).
func txFulltextAll(ctx context.Context, tx *sql.Tx) bool {
	return settingBool(ctx, tx, SettingFulltextAll, false)
}
