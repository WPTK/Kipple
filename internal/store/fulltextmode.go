package store

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"sync"
	"time"
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

// get returns the cached value, loading it on a miss. A load that fails is not
// cached: the caller gets the (default) value for this call only and the next
// call tries again.
func (c *boolCache) get(load func() (bool, error)) bool {
	c.mu.Lock()
	if c.ok {
		v := c.val
		c.mu.Unlock()
		return v
	}
	g := c.gen
	c.mu.Unlock()
	v, err := load()
	c.mu.Lock()
	if err == nil && c.gen == g {
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
	return d.ftAll.get(func() (bool, error) {
		// The value is shared by every request, so the load must not die with
		// the (possibly cancelled) request that happened to miss the cache.
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return settingBoolErr(lctx, d.reader, SettingFulltextAll, false)
	})
}

// txFulltextAll reads the switch inside a transaction (no cache: it must agree
// with the rest of that transaction).
func txFulltextAll(ctx context.Context, tx *sql.Tx) (bool, error) {
	return settingBoolErr(ctx, tx, SettingFulltextAll, false)
}

// ftPending is the set of items the ingest extraction pool has queued or is
// running (design §6.5): the only items the Reader API holds back. It is process
// memory on purpose: a queue does not survive a restart, so neither does the
// hold, and an item the pool never accepted (over the per-fetch cap, queue
// full, shutdown) is served at once with the feed's own content instead of
// waiting out the hold window for text that is not coming.
type ftPending struct {
	mu  sync.Mutex
	ids map[int64]struct{}
}

// MarkFulltextPending records that the pool has queued these items.
func (d *DB) MarkFulltextPending(ids ...int64) {
	d.ftPend.mu.Lock()
	defer d.ftPend.mu.Unlock()
	if d.ftPend.ids == nil {
		d.ftPend.ids = make(map[int64]struct{}, len(ids))
	}
	for _, id := range ids {
		d.ftPend.ids[id] = struct{}{}
	}
}

// ClearFulltextPending forgets an item once its extraction has finished or been
// abandoned (or its queueing failed).
func (d *DB) ClearFulltextPending(ids ...int64) {
	d.ftPend.mu.Lock()
	defer d.ftPend.mu.Unlock()
	for _, id := range ids {
		delete(d.ftPend.ids, id)
	}
}

// HoldPending is the pending set as a JSON array, the value bound as :pending
// next to :hold_cut for HeldSQL. Callers of MarkAllRead with a HoldCut fill
// MarkScope.HoldPending with it.
func (d *DB) HoldPending() string {
	d.ftPend.mu.Lock()
	defer d.ftPend.mu.Unlock()
	if len(d.ftPend.ids) == 0 {
		return "[]"
	}
	// Sorted, so the encoding is deterministic (map order is not). Built by hand: a []int64
	// cannot fail to encode, so there is no error to drop.
	ids := make([]int64, 0, len(d.ftPend.ids))
	for id := range d.ftPend.ids {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	b := append(make([]byte, 0, len(ids)*8), '[')
	for i, id := range ids {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, id, 10)
	}
	return string(append(b, ']'))
}

// holdArgs are the named arguments HeldSQL needs.
func (d *DB) holdArgs(holdCut int64) []any {
	return []any{sql.Named("hold_cut", holdCut), sql.Named("pending", d.HoldPending())}
}

// FeedFulltextNow is the effective full-text mode of the feed's new items right
// now: the feed's current flag and the current switch, not what a fetch
// snapshotted earlier.
func (d *DB) FeedFulltextNow(ctx context.Context, feedID int64) (bool, error) {
	if d.FulltextAll(ctx) {
		return true, nil
	}
	var ft int
	if err := d.reader.QueryRowContext(ctx, "SELECT fulltext FROM feeds WHERE id = ?", feedID).Scan(&ft); err != nil {
		return false, err
	}
	return ft == 1, nil
}
