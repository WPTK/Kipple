package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/filter"
)

// Keyword filters (backend-additions-round2 section 1): the stored rules, the
// generation-checked compiled-set cache the ingest path reads, and the ingest
// hook itself. The engine is internal/filter (pure); this file is its storage.

// Filter is one stored rule as the API shows it. Ids are strings in JSON.
type Filter struct {
	ID             int64    `json:"id,string"`
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	Scope          string   `json:"scope"`
	FolderID       *int64   `json:"folder_id,string"`
	FeedID         *int64   `json:"feed_id,string"`
	Kind           string   `json:"kind"`
	Terms          []string `json:"terms"`
	Fields         []string `json:"fields"`
	CaseSensitive  bool     `json:"case_sensitive"`
	WholeWord      bool     `json:"whole_word"`
	FoldDiacritics bool     `json:"fold_diacritics"`
	Invert         bool     `json:"invert"`
	Action         string   `json:"action"`
	Position       int      `json:"position"`
	Hits           int64    `json:"hits"`
	LastHitAt      *int64   `json:"last_hit_at"`
	CreatedAt      int64    `json:"created_at"`
	UpdatedAt      int64    `json:"updated_at"`
}

// Rule converts a stored filter to the engine's rule.
func (f Filter) Rule() filter.Rule {
	r := filter.Rule{
		ID: f.ID, Name: f.Name, Enabled: f.Enabled, Scope: filter.Scope(f.Scope), Kind: filter.Kind(f.Kind),
		Terms: append([]string(nil), f.Terms...), CaseSensitive: f.CaseSensitive, WholeWord: f.WholeWord,
		FoldDiacritics: f.FoldDiacritics, Invert: f.Invert, Action: filter.Action(f.Action), Position: f.Position,
	}
	if f.FolderID != nil {
		r.FolderID = *f.FolderID
	}
	if f.FeedID != nil {
		r.FeedID = *f.FeedID
	}
	for _, x := range f.Fields {
		r.Fields = append(r.Fields, filter.Field(x))
	}
	return r
}

const filterCols = `id, name, enabled, scope, folder_id, feed_id, kind, terms, fields, case_sensitive, whole_word,
	fold_diacritics, invert, action, position, hits, last_hit_at, created_at, updated_at`

func scanFilter(sc interface{ Scan(...any) error }) (Filter, error) {
	var f Filter
	var enabled, cs, ww, fd, inv int
	var folder, feed, last sql.NullInt64
	var terms, fields string
	if err := sc.Scan(&f.ID, &f.Name, &enabled, &f.Scope, &folder, &feed, &f.Kind, &terms, &fields, &cs, &ww, &fd, &inv,
		&f.Action, &f.Position, &f.Hits, &last, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return f, err
	}
	f.Enabled, f.CaseSensitive, f.WholeWord, f.FoldDiacritics, f.Invert = enabled == 1, cs == 1, ww == 1, fd == 1, inv == 1
	if folder.Valid {
		f.FolderID = &folder.Int64
	}
	if feed.Valid {
		f.FeedID = &feed.Int64
	}
	if last.Valid {
		f.LastHitAt = &last.Int64
	}
	if err := json.Unmarshal([]byte(terms), &f.Terms); err != nil || f.Terms == nil {
		f.Terms = []string{}
	}
	if err := json.Unmarshal([]byte(fields), &f.Fields); err != nil || f.Fields == nil {
		f.Fields = []string{"title"}
	}
	return f, nil
}

func loadFilters(ctx context.Context, q Querier) ([]Filter, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+filterCols+" FROM filters ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Filter{}
	for rows.Next() {
		f, err := scanFilter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListFilters returns every filter in display order.
func (d *DB) ListFilters(ctx context.Context) ([]Filter, error) {
	return loadFilters(ctx, d.reader)
}

// GetFilter returns one filter.
func (d *DB) GetFilter(ctx context.Context, id int64) (Filter, bool, error) {
	f, err := scanFilter(d.reader.QueryRowContext(ctx, "SELECT "+filterCols+" FROM filters WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	return f, err == nil, err
}

// ---- the compiled-set cache ----

// filterSet is one compiled generation of the rules.
type filterSet struct {
	set     *filter.Set
	actions map[int64]filter.Action
}

// filterCache holds the compiled set for the generation it was built at. Every
// filter write bumps DB.filterGen inside its own transaction, before it commits; the ingest
// path recompiles only when the generation moved. Only this process writes filters (design
// 1.5), so a counter is enough. The set is only ever loaded inside a write transaction (the
// ingest path), and write transactions are serialized, so a load either ran before the filter
// write's transaction began (old rows, old generation) or after it committed (new generation):
// a fetch commit that takes the writer right after a filter write can never use the stale set.
type filterCache struct {
	mu  sync.Mutex
	gen uint64
	set *filterSet
	ok  bool
}

// bumpFilters invalidates the compiled set. Call it inside the write transaction that changes
// filters (or cascades them away), before the transaction returns, as Unsubscribe does.
func (d *DB) bumpFilters() { d.filterGen.Add(1) }

// filterTxDone runs the test hook, if any, at the end of a filter write's transaction.
func (d *DB) filterTxDone() {
	if h := d.testFilterTxHook; h != nil {
		h()
	}
}

// filters returns the current compiled set, nil when it could not be loaded
// (ingest then runs without filters: a rule problem must never block a fetch).
func (d *DB) filters(ctx context.Context, q Querier) *filterSet {
	g := d.filterGen.Load()
	d.fcache.mu.Lock()
	defer d.fcache.mu.Unlock()
	if d.fcache.ok && d.fcache.gen == g {
		return d.fcache.set
	}
	fs, err := loadFilters(ctx, q)
	if err != nil {
		d.log.Warn("store: load filters; ingesting without them", "err", err)
		return nil
	}
	rules := make([]filter.Rule, len(fs))
	for i, f := range fs {
		rules[i] = f.Rule()
	}
	cs := d.compileSkippingBad(rules)
	d.fcache.gen, d.fcache.set, d.fcache.ok = g, cs, true
	return cs
}

// compileSkippingBad builds the set; a rule that no longer validates (a stored
// row from another version) is logged and dropped rather than failing every fetch.
func (d *DB) compileSkippingBad(rules []filter.Rule) *filterSet {
	for {
		set, err := filter.NewSet(rules)
		if err == nil {
			acts := make(map[int64]filter.Action, len(rules))
			for _, r := range rules {
				acts[r.ID] = r.Action
			}
			return &filterSet{set: set, actions: acts}
		}
		var se *filter.SetError
		if !errors.As(err, &se) || se.Index < 0 || se.Index >= len(rules) {
			d.log.Warn("store: compile filters; ingesting without them", "err", err)
			return &filterSet{set: nil, actions: nil}
		}
		d.log.Warn("store: skipping an invalid stored filter", "filter", se.RuleID, "err", err)
		rules = append(rules[:se.Index:se.Index], rules[se.Index+1:]...)
	}
}

// ---- CRUD ----

// validateSet compiles rules as one set and returns the failure as a *filter.Error.
func validateSet(rules []filter.Rule) error {
	if _, err := filter.NewSet(rules); err != nil {
		var se *filter.SetError
		if errors.As(err, &se) {
			return se.Err
		}
		return err
	}
	return nil
}

// checkScopeRefs makes sure the folder or feed a rule names exists.
func checkScopeRefs(ctx context.Context, tx Querier, f Filter) error {
	var n int
	switch {
	case f.Scope == "folder" && f.FolderID != nil:
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM folders WHERE id = ?", *f.FolderID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return &filter.Error{Field: "folder_id", Message: "no such folder"}
		}
	case f.Scope == "feed" && f.FeedID != nil:
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM feeds WHERE id = ? AND disabled_reason IS NOT 'archive'", *f.FeedID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return &filter.Error{Field: "feed_id", Message: "no such feed"}
		}
	}
	return nil
}

func nullableID(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// CreateFilter validates f (the whole set, so the set-wide caps apply) and stores
// it. Validation failures are *filter.Error. The filter's own ID, Hits and
// timestamps are ignored.
func (d *DB) CreateFilter(ctx context.Context, f Filter) (Filter, error) {
	var out Filter
	now := d.clock.Now().Unix()
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		cur, err := loadFilters(ctx, tx)
		if err != nil {
			return err
		}
		rules := make([]filter.Rule, 0, len(cur)+1)
		for _, c := range cur {
			rules = append(rules, c.Rule())
		}
		f.ID = 1 << 40 // above any real id, so the set treats it as new
		rules = append(rules, f.Rule())
		if err := validateSet(rules); err != nil {
			return err
		}
		if err := checkScopeRefs(ctx, tx, f); err != nil {
			return err
		}
		terms, _ := json.Marshal(f.Terms)
		fields, _ := json.Marshal(f.Fields)
		res, err := tx.ExecContext(ctx, `INSERT INTO filters (name, enabled, scope, folder_id, feed_id, kind, terms, fields,
			case_sensitive, whole_word, fold_diacritics, invert, action, position, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.Name, boolInt(f.Enabled), f.Scope, nullableID(f.FolderID), nullableID(f.FeedID), f.Kind, string(terms), string(fields),
			boolInt(f.CaseSensitive), boolInt(f.WholeWord), boolInt(f.FoldDiacritics), boolInt(f.Invert), f.Action, f.Position, now, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = scanFilter(tx.QueryRowContext(ctx, "SELECT "+filterCols+" FROM filters WHERE id = ?", id))
		if err != nil {
			return err
		}
		d.bumpFilters()
		d.filterTxDone()
		return nil
	})
	return out, err
}

// UpdateFilter loads filter id, lets mutate change it, validates the result
// within the whole set and stores it. ok is false when no such filter exists.
func (d *DB) UpdateFilter(ctx context.Context, id int64, mutate func(*Filter) error) (out Filter, ok bool, err error) {
	now := d.clock.Now().Unix()
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		cur, err := loadFilters(ctx, tx)
		if err != nil {
			return err
		}
		idx := -1
		for i, c := range cur {
			if c.ID == id {
				idx = i
			}
		}
		if idx < 0 {
			return nil
		}
		ok = true
		f := cur[idx]
		f.Terms, f.Fields = append([]string(nil), f.Terms...), append([]string(nil), f.Fields...)
		if err := mutate(&f); err != nil {
			return err
		}
		f.ID = id
		rules := make([]filter.Rule, len(cur))
		for i, c := range cur {
			rules[i] = c.Rule()
		}
		rules[idx] = f.Rule()
		if err := validateSet(rules); err != nil {
			return err
		}
		if err := checkScopeRefs(ctx, tx, f); err != nil {
			return err
		}
		terms, _ := json.Marshal(f.Terms)
		fields, _ := json.Marshal(f.Fields)
		if _, err := tx.ExecContext(ctx, `UPDATE filters SET name=?, enabled=?, scope=?, folder_id=?, feed_id=?, kind=?, terms=?, fields=?,
			case_sensitive=?, whole_word=?, fold_diacritics=?, invert=?, action=?, position=?, updated_at=? WHERE id=?`,
			f.Name, boolInt(f.Enabled), f.Scope, nullableID(f.FolderID), nullableID(f.FeedID), f.Kind, string(terms), string(fields),
			boolInt(f.CaseSensitive), boolInt(f.WholeWord), boolInt(f.FoldDiacritics), boolInt(f.Invert), f.Action, f.Position, now, id); err != nil {
			return err
		}
		out, err = scanFilter(tx.QueryRowContext(ctx, "SELECT "+filterCols+" FROM filters WHERE id = ?", id))
		if err != nil {
			return err
		}
		d.bumpFilters()
		d.filterTxDone()
		return nil
	})
	return out, ok, err
}

// Values of the ?unmute= choice on filter deletion.
const (
	UnmuteKeep   = "keep"   // items keep muted_by and show as "muted by a deleted filter"
	UnmuteRead   = "read"   // muted_by is cleared; the items stay read and reappear in All
	UnmuteUnread = "unread" // muted_by is cleared and the items become unread
)

// unmuteBatch is how many items one restore transaction touches.
const unmuteBatch = 500

// DeleteFilter deletes filter id and, per unmute, restores the items it muted.
//
// The order makes it resumable: the rule is first disabled (so ingest and a running apply stop
// muting with it; one transaction, generation bumped), the items are then restored in batches
// of unmuteBatch behind the commit gate, and the rule row is deleted last. A cancelled or
// crashed run therefore leaves a disabled rule and some still-muted items, and running the same
// delete again finishes the job. For an id whose row is already gone, unmute=read|unread still
// restores any orphans that carry that muted_by (a retry after a run that was cut off after the
// row went, or an orphan left by ?unmute=keep), and ok reports whether there was a row or an
// orphan. unmute=keep just deletes the row. onBatch (optional) receives each restored batch so the
// caller can publish it. changed counts restored items.
func (d *DB) DeleteFilter(ctx context.Context, id int64, unmute string, onBatch func(StateResult)) (changed int64, ok bool, err error) {
	if unmute == "" {
		unmute = UnmuteRead
	}
	if unmute != UnmuteKeep && unmute != UnmuteRead && unmute != UnmuteUnread {
		return 0, false, fmt.Errorf("store: unmute mode %q", unmute)
	}
	if unmute == UnmuteKeep {
		err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, "DELETE FROM filters WHERE id = ?", id)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			ok = n > 0
			if err == nil && ok {
				d.bumpFilters()
				d.filterTxDone()
			}
			return err
		})
		return 0, ok, err
	}

	// 1. Stop the rule from muting anything further.
	hadRow := false
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE filters SET enabled = 0 WHERE id = ?", id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		hadRow = n > 0
		if err == nil && hadRow {
			d.bumpFilters()
			d.filterTxDone()
		}
		return err
	})
	if err != nil {
		return 0, false, err
	}

	// 2. Restore its items, batch by batch. Each batch takes the lowest ids still muted by it, so a
	// re-run picks up exactly where a cut-off one stopped.
	orphans := false
	for {
		var res StateResult
		var n int
		_, err := d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
			var ids []int64
			rows, err := tx.QueryContext(ctx, "SELECT id FROM items WHERE muted_by = ?1 ORDER BY id LIMIT ?2", id, unmuteBatch)
			if err != nil {
				return 0, err
			}
			for rows.Next() {
				var x int64
				if err := rows.Scan(&x); err != nil {
					rows.Close()
					return 0, err
				}
				ids = append(ids, x)
			}
			if err := rows.Close(); err != nil {
				return 0, err
			}
			n = len(ids)
			if n == 0 {
				return 0, nil
			}
			js, _ := idsJSON(ids)
			if unmute == UnmuteUnread {
				// Only what was unread before the mute goes back to unread; an item the user had already read
				// (or one muted from the initial-read window) keeps its read state and read_at.
				urows, err := tx.QueryContext(ctx, `UPDATE items SET muted_by = NULL, muted_was_read = NULL,
					read = CASE WHEN COALESCE(muted_was_read, 1) = 1 THEN read ELSE 0 END,
					read_at = CASE WHEN COALESCE(muted_was_read, 1) = 1 THEN read_at ELSE NULL END
					WHERE id IN (SELECT value FROM json_each(?1)) AND muted_by = ?2 RETURNING id, feed_id, read`, js, id)
				if err != nil {
					return 0, err
				}
				if res, err = scanUnmuted(urows); err != nil {
					return 0, err
				}
			} else {
				urows, err := tx.QueryContext(ctx, `UPDATE items SET muted_by = NULL, muted_was_read = NULL
					WHERE id IN (SELECT value FROM json_each(?1)) AND muted_by = ?2 RETURNING id, feed_id`, js, id)
				if err != nil {
					return 0, err
				}
				if res.Changed, err = scanIDs(urows); err != nil {
					return 0, err
				}
			}
			return int64(len(res.Changed)), nil
		})
		if err != nil {
			return changed, hadRow || orphans, err
		}
		if n == 0 {
			break
		}
		orphans = true
		changed += int64(len(res.Changed))
		if onBatch != nil && len(res.Changed) > 0 {
			onBatch(res)
		}
	}

	// 3. Delete the row last.
	if hadRow {
		err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM filters WHERE id = ?", id); err != nil {
				return err
			}
			d.bumpFilters()
			d.filterTxDone()
			return nil
		})
		if err != nil {
			return changed, true, err
		}
	}
	return changed, hadRow || orphans, nil
}

// scanUnmuted reads RETURNING id, feed_id, read of an un-mute UPDATE: every id is Changed, and
// those now unread are MadeUnread.
func scanUnmuted(rows *sql.Rows) (StateResult, error) {
	defer rows.Close()
	var res StateResult
	for rows.Next() {
		var id, feed int64
		var read int
		if err := rows.Scan(&id, &feed, &read); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, id)
		if read == 0 {
			res.MadeUnread = append(res.MadeUnread, id)
		}
	}
	return res, rows.Err()
}

// MutedCount is the number of muted items (the partial index answers it).
func (d *DB) MutedCount(ctx context.Context) (int64, error) {
	var n int64
	err := d.reader.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE muted_by IS NOT NULL").Scan(&n)
	return n, err
}

// Highlight is one highlight rule as the client needs it (bootstrap `highlights`).
type Highlight struct {
	ID             int64    `json:"id,string"`
	Scope          string   `json:"scope"`
	FolderID       *int64   `json:"folder_id,string"`
	FeedID         *int64   `json:"feed_id,string"`
	Terms          []string `json:"terms"`
	Fields         []string `json:"fields"`
	CaseSensitive  bool     `json:"case_sensitive"`
	WholeWord      bool     `json:"whole_word"`
	FoldDiacritics bool     `json:"fold_diacritics"`
}

// Highlights lists the enabled highlight rules the client can apply: text rules only
// (engine validation) and not inverted (there is nothing to mark where a term is absent).
func (d *DB) Highlights(ctx context.Context) ([]Highlight, error) {
	all, err := loadFilters(ctx, d.reader)
	if err != nil {
		return nil, err
	}
	out := []Highlight{}
	for _, f := range all {
		if !f.Enabled || f.Action != string(filter.ActionHighlight) || f.Kind != string(filter.KindText) || f.Invert {
			continue
		}
		out = append(out, Highlight{ID: f.ID, Scope: f.Scope, FolderID: f.FolderID, FeedID: f.FeedID, Terms: f.Terms,
			Fields: f.Fields, CaseSensitive: f.CaseSensitive, WholeWord: f.WholeWord, FoldDiacritics: f.FoldDiacritics})
	}
	return out, nil
}

// ---- the ingest hook ----

// ingestEval evaluates the fresh items of one feed commit.
type ingestEval struct {
	fs        *filterSet
	feedID    int64
	folderID  int64
	feedTitle string
}

// newIngestEval loads what the rules need to know about the feed. It returns nil
// when there is nothing to evaluate (no rules), which is the common case and costs
// an atomic load.
func (d *DB) newIngestEval(ctx context.Context, tx *sql.Tx, feedID int64, docTitle string) (*ingestEval, error) {
	fs := d.filters(ctx, tx)
	if fs == nil || fs.set.Len() == 0 {
		return nil, nil
	}
	e := &ingestEval{fs: fs, feedID: feedID}
	if err := e.loadFeed(ctx, tx, docTitle); err != nil {
		return nil, err
	}
	return e, nil
}

// loadFeed fills the folder and the title a rule's `feed` field sees: the custom title, else the
// stored title, else the fetched document's title (a brand-new subscription has none stored yet).
// The commit and the full-text prediction (MutedUIDs) both use it, so they cannot disagree.
func (e *ingestEval) loadFeed(ctx context.Context, q Querier, docTitle string) error {
	var custom, title sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT folder_id, custom_title, title FROM feeds WHERE id = ?", e.feedID).Scan(&e.folderID, &custom, &title); err != nil {
		return err
	}
	e.feedTitle = strings.TrimSpace(custom.String)
	if e.feedTitle == "" {
		e.feedTitle = strings.TrimSpace(title.String)
	}
	if e.feedTitle == "" {
		e.feedTitle = docTitle
	}
	return nil
}

// ingestVerdict is what the rules decided for one fresh item.
type ingestVerdict struct {
	read, starred bool
	mutedBy       int64 // 0 = not muted
	marked        bool  // a mark_read rule took effect (the item was unread before it)
}

// eval decides one fresh item. baseRead is its read state before any rule (initial-read window,
// rekey leftover). hits accumulates the per-rule counts of matches whose action took effect.
func (e *ingestEval) eval(it fetch.Item, baseRead bool, hits map[int64]int) ingestVerdict {
	v := ingestVerdict{read: baseRead}
	res := e.fs.set.Evaluate(filter.Item{FeedID: e.feedID, FolderID: e.folderID, FeedTitle: e.feedTitle,
		Title: it.Title, Author: it.Author, URL: it.URL, Content: it.ContentText, Categories: it.Categories})
	if !res.Any() {
		return v
	}
	for _, id := range res.Matched {
		switch e.fs.actions[id] {
		case filter.ActionStar:
			hits[id]++
		case filter.ActionMute:
			if res.Muted { // a star rule on the same item cancels the mute, and the hit with it
				hits[id]++
			}
		case filter.ActionMarkRead:
			if !baseRead {
				hits[id]++
			}
		}
	}
	if res.Star {
		v.starred = true
	}
	if res.Muted {
		v.mutedBy = res.MutedBy
	}
	if res.Read {
		v.read = true
	}
	v.marked = res.MarkRead && !res.Muted && !baseRead
	return v
}

// writeHits adds this chunk's hit counts inside its transaction.
func writeHits(ctx context.Context, tx *sql.Tx, hits map[int64]int, now int64) error {
	for id, n := range hits {
		if _, err := tx.ExecContext(ctx, "UPDATE filters SET hits = hits + ?, last_hit_at = ? WHERE id = ?", n, now, id); err != nil {
			return err
		}
	}
	return nil
}

// categoriesJSON is the item_content.categories_json value: NULL without categories.
func categoriesJSON(c []string) any {
	if len(c) == 0 {
		return nil
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// MutedUIDs predicts which of items (a feed's fresh candidates, before their commit) the current
// rules would mute, so the full-text picker does not spend its per-fetch cap on them. It is an
// estimate: the commit re-evaluates, and its MutedIDs are what the queue finally skips. The set is
// compiled from the reader pool here, never stored in the ingest cache (a reader can see rows
// from before a filter write that already bumped the generation).
func (d *DB) MutedUIDs(ctx context.Context, feedID int64, docTitle string, items []fetch.Item) (map[string]bool, error) {
	var n int
	if err := d.reader.QueryRowContext(ctx, "SELECT count(*) FROM filters WHERE enabled = 1 AND action = 'mute'").Scan(&n); err != nil || n == 0 {
		return nil, err
	}
	all, err := loadFilters(ctx, d.reader)
	if err != nil {
		return nil, err
	}
	rules := make([]filter.Rule, len(all))
	for i, f := range all {
		rules[i] = f.Rule()
	}
	cs := d.compileSkippingBad(rules)
	if cs.set == nil || cs.set.Len() == 0 {
		return nil, nil
	}
	e := &ingestEval{fs: cs, feedID: feedID}
	if err := e.loadFeed(ctx, d.reader, docTitle); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, it := range items {
		if v := e.eval(it, false, map[int64]int{}); v.mutedBy != 0 {
			out[it.UID] = true
		}
	}
	return out, nil
}

// MutedByFilter counts the muted items per filter id (orphans of deleted filters included).
func (d *DB) MutedByFilter(ctx context.Context) (map[int64]int64, error) {
	rows, err := d.reader.QueryContext(ctx, "SELECT muted_by, count(*) FROM items WHERE muted_by IS NOT NULL GROUP BY muted_by")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
