package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

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
	// DisabledReason says why Kipple switched the rule off (it no longer meets the current
	// limits); nil for a rule the user controls. Editing the rule so it validates clears it.
	DisabledReason *string `json:"disabled_reason"`
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
	if err := json.Unmarshal([]byte(fields), &f.Fields); err != nil {
		f.Fields = nil
	}
	normalizeFields(&f)
	return f, nil
}

// normalizeFields makes an empty field list explicit: the engine reads it as title only, and the
// client's highlighter (which only sees the stored list) needs to know that too.
func normalizeFields(f *Filter) {
	if len(f.Fields) == 0 {
		f.Fields = []string{string(filter.FieldTitle)}
	}
}

// feedTitleSQL is the one rule for a feed's display name, for the feeds table under alias a (use
// "feeds" for an unaliased query): the custom title, else the title the feed gives itself, else the
// feed URL (both titles trimmed of ASCII whitespace, blank counts as absent). Every list, the Reader
// API, a rule's `feed` field, the stats snapshots and the fetch commit's rename check use it, so they
// all name a feed the same way. A new feed stores no title until its first fetch, so it shows its URL.
func feedTitleSQL(a string) string {
	return "COALESCE(" + feedOwnTitleSQL(a) + ", " + a + ".url)"
}

// feedOwnTitleSQL is feedTitleSQL without the URL fallback: NULL when the feed has no title yet.
func feedOwnTitleSQL(a string) string {
	return "COALESCE(NULLIF(trim(" + a + ".custom_title, " + sqlSpace + "), ''), NULLIF(trim(" + a + ".title, " + sqlSpace + "), ''))"
}

// FeedOwnTitleSQL is feedOwnTitleSQL for other packages (the OPML export writes no name for a feed
// that has none yet, rather than a placeholder a re-import would keep as a custom name).
func FeedOwnTitleSQL(a string) string { return feedOwnTitleSQL(a) }

// sqlSpace and goSpace are the same set of whitespace, for SQL trim() and strings.Trim.
const (
	sqlSpace = "char(32, 9, 10, 11, 12, 13)"
	goSpace  = " \t\n\v\f\r"
)

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

// ListFilters returns every filter in display order. It is also where stored rules that no
// longer meet the current limits are noticed lazily: when the list holds one that is still
// enabled (or has no recorded reason), they are switched off with their reason first
// (SanitizeFilters), so the list the user sees is what ingest runs.
func (d *DB) ListFilters(ctx context.Context) ([]Filter, error) {
	fs, err := loadFilters(ctx, d.reader)
	if err != nil {
		return nil, err
	}
	reasons, err := loadFilterReasons(ctx, d.reader)
	if err != nil {
		return nil, err
	}
	if sanitizeNeeded(fs, reasons) {
		if _, err := d.SanitizeFilters(ctx); err != nil {
			// Best effort (the writer may be busy): the list still answers, and the next
			// list, filter write or fetch commit tries again.
			d.log.Warn("store: disable stored filters the current limits refuse", "err", err)
			attachReasons(fs, reasons)
			return fs, nil
		}
		if fs, err = loadFilters(ctx, d.reader); err != nil {
			return nil, err
		}
		if reasons, err = loadFilterReasons(ctx, d.reader); err != nil {
			return nil, err
		}
	}
	attachReasons(fs, reasons)
	return fs, nil
}

// GetFilter returns one filter.
func (d *DB) GetFilter(ctx context.Context, id int64) (Filter, bool, error) {
	f, err := getFilter(ctx, d.reader, id)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	return f, err == nil, err
}

// getFilter loads one filter with its disabled reason.
func getFilter(ctx context.Context, q Querier, id int64) (Filter, error) {
	f, err := scanFilter(q.QueryRowContext(ctx, "SELECT "+filterCols+" FROM filters WHERE id = ?", id))
	if err != nil {
		return f, err
	}
	reasons, err := loadFilterReasons(ctx, q)
	if err != nil {
		return f, err
	}
	fs := []Filter{f}
	attachReasons(fs, reasons)
	return fs[0], nil
}

// ---- rules the current limits refuse ----

// settingFilterReasons holds, as a JSON object keyed by filter id, the reason Kipple disabled a
// stored rule (a system key: never exported, never PATCHable).
const settingFilterReasons = "sys.filter_disabled_reasons"

func loadFilterReasons(ctx context.Context, q Querier) (map[int64]string, error) {
	var raw string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", settingFilterReasons).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[int64]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var byKey map[string]string
	if err := json.Unmarshal([]byte(raw), &byKey); err != nil {
		return map[int64]string{}, nil // unreadable: start over, the next sanitize rewrites it
	}
	out := make(map[int64]string, len(byKey))
	for k, v := range byKey {
		var id int64
		if _, err := fmt.Sscan(k, &id); err == nil {
			out[id] = v
		}
	}
	return out, nil
}

func saveFilterReasons(ctx context.Context, q Querier, m map[int64]string, now int64) error {
	if len(m) == 0 {
		_, err := q.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", settingFilterReasons)
		return err
	}
	byKey := make(map[string]string, len(m))
	for id, v := range m {
		byKey[fmt.Sprint(id)] = v
	}
	v, err := jsonText(byKey)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES(?1, ?2, ?3)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, settingFilterReasons, v, now)
	return err
}

func attachReasons(fs []Filter, reasons map[int64]string) {
	for i := range fs {
		fs[i].DisabledReason = nil
		if why, ok := reasons[fs[i].ID]; ok && !fs[i].Enabled {
			fs[i].DisabledReason = &why
		}
	}
}

func filterRules(fs []Filter) []filter.Rule {
	rules := make([]filter.Rule, len(fs))
	for i, f := range fs {
		rules[i] = f.Rule()
	}
	return rules
}

// sanitizeNeeded reports whether a stored rule the current limits refuse is still enabled or has
// no recorded reason, so SanitizeFilters has something to write.
func sanitizeNeeded(fs []Filter, reasons map[int64]string) bool {
	for i := range filter.Sanitize(filterRules(fs)) {
		if _, has := reasons[fs[i].ID]; fs[i].Enabled || !has {
			return true
		}
	}
	return false
}

// SanitizeFilters disables every stored rule that cannot run under the current limits (a rule
// saved by an older version whose regex is now too expensive, or one that pushes the enabled
// rules past a set-wide limit) and records why, for the filters list to show. Without it such a
// rule would look enabled while ingest skipped it. It reports whether any rule was disabled. It
// runs lazily (the filters list, every filter write and the ingest hook); a startup call is cheap.
func (d *DB) SanitizeFilters(ctx context.Context) (bool, error) {
	var changed bool
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		changed, err = d.sanitizeFiltersTx(ctx, tx)
		return err
	})
	return changed, err
}

// sanitizeFiltersTx is SanitizeFilters inside the caller's write transaction.
func (d *DB) sanitizeFiltersTx(ctx context.Context, q Querier) (changed bool, err error) {
	fs, err := loadFilters(ctx, q)
	if err != nil {
		return false, err
	}
	reasons, err := loadFilterReasons(ctx, q)
	if err != nil {
		return false, err
	}
	if !sanitizeNeeded(fs, reasons) {
		return false, nil
	}
	now := d.clock.Now().Unix()
	next := map[int64]string{}
	for _, f := range fs { // entries of rules that are gone or back on are dropped
		if why, ok := reasons[f.ID]; ok && !f.Enabled {
			next[f.ID] = why
		}
	}
	for i, why := range filter.Sanitize(filterRules(fs)) {
		f := fs[i]
		if f.Enabled {
			if _, err := q.ExecContext(ctx, "UPDATE filters SET enabled = 0, updated_at = ? WHERE id = ?", now, f.ID); err != nil {
				return false, err
			}
			d.log.Warn("store: disabled a stored filter the current limits refuse", "filter", f.ID, "name", f.Name, "reason", why)
			changed = true
		} else if _, has := next[f.ID]; has {
			continue // keep the reason it was disabled for
		}
		next[f.ID] = why + " (edit the filter to re-enable it)"
	}
	if err := saveFilterReasons(ctx, q, next, now); err != nil {
		return false, err
	}
	if changed {
		d.bumpFilters()
	}
	return changed, nil
}

// dropFilterReason forgets the recorded reason of one rule (an edit that validates, a delete).
func dropFilterReason(ctx context.Context, q Querier, id, now int64) error {
	reasons, err := loadFilterReasons(ctx, q)
	if err != nil {
		return err
	}
	if _, ok := reasons[id]; !ok {
		return nil
	}
	delete(reasons, id)
	return saveFilterReasons(ctx, q, reasons, now)
}

// ---- the compiled-set cache ----

// filterSet is one compiled generation of the rules.
type filterSet struct {
	set     *filter.Set
	actions map[int64]filter.Action
	// src is the rule list the set was compiled from (invalid rules included). Two sets
	// compiled from equal lists decide every item alike, which is how the ingest path tells
	// whether verdicts computed before its write transaction still hold (preEval.usable).
	src []filter.Rule
}

// filterCache holds the compiled set for the generation it was built at. Every
// filter write bumps DB.filterGen inside its own transaction, before it commits; the ingest
// path recompiles only when the generation moved. Only this process writes filters (design
// 1.5), so a counter is enough. The set is only ever loaded inside a write transaction (the
// ingest path), and write transactions are serialized, so a load either ran before the filter
// write's transaction began (old rows, old generation) or after it committed (new generation):
// a fetch commit that takes the writer right after a filter write can never use the stale set.
// The pre-transaction evaluation (preEvaluate) may read the cached set but never stores one.
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
	// A stored rule the current limits refuse is switched off visibly here (the first fetch
	// commit after an upgrade), not just skipped: the filters list then shows why. It runs in
	// the commit's own transaction, so it is durable exactly when the commit is. A failure only
	// costs the visible reason: compileSkippingBad still leaves the rule out.
	if _, err := d.sanitizeFiltersTx(ctx, q); err != nil {
		d.log.Warn("store: disable stored filters the current limits refuse", "err", err)
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
	src := rules // the removal below always copies (capped slice), so src stays whole
	for {
		set, err := filter.NewSet(rules)
		if err == nil {
			acts := make(map[int64]filter.Action, len(rules))
			for _, r := range rules {
				acts[r.ID] = r.Action
			}
			return &filterSet{set: set, actions: acts, src: src}
		}
		var se *filter.SetError
		if !errors.As(err, &se) || se.Index < 0 || se.Index >= len(rules) {
			d.log.Warn("store: compile filters; ingesting without them", "err", err)
			return &filterSet{set: nil, actions: nil, src: src}
		}
		d.log.Warn("store: skipping an invalid stored filter", "filter", se.RuleID, "err", err)
		rules = append(rules[:se.Index:se.Index], rules[se.Index+1:]...)
	}
}

// ---- CRUD ----

// validateEdit checks rules[edited] (the rule being created, edited, previewed or applied) with
// filter.ValidateEdit and returns the failure as a *filter.Error about that rule. Other stored
// rules that no longer compile cannot fail it.
func validateEdit(rules []filter.Rule, edited int) error {
	if err := filter.ValidateEdit(rules, edited); err != nil {
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
		if _, err := d.sanitizeFiltersTx(ctx, tx); err != nil {
			return err
		}
		cur, err := loadFilters(ctx, tx)
		if err != nil {
			return err
		}
		rules := make([]filter.Rule, 0, len(cur)+1)
		for _, c := range cur {
			rules = append(rules, c.Rule())
		}
		f.ID = unsavedFilterID // above any real id, so the set treats it as new
		normalizeFields(&f)
		rules = append(rules, f.Rule())
		if err := validateEdit(rules, len(rules)-1); err != nil {
			return err
		}
		if err := checkScopeRefs(ctx, tx, f); err != nil {
			return err
		}
		terms, err := jsonText(f.Terms)
		if err != nil {
			return err
		}
		fields, err := jsonText(f.Fields)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO filters (name, enabled, scope, folder_id, feed_id, kind, terms, fields,
			case_sensitive, whole_word, fold_diacritics, invert, action, position, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.Name, boolInt(f.Enabled), f.Scope, nullableID(f.FolderID), nullableID(f.FeedID), f.Kind, terms, fields,
			boolInt(f.CaseSensitive), boolInt(f.WholeWord), boolInt(f.FoldDiacritics), boolInt(f.Invert), f.Action, f.Position, now, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = getFilter(ctx, tx, id)
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
		if _, err := d.sanitizeFiltersTx(ctx, tx); err != nil {
			return err
		}
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
		normalizeFields(&f)
		rules := make([]filter.Rule, len(cur))
		for i, c := range cur {
			rules[i] = c.Rule()
		}
		rules[idx] = f.Rule()
		if err := validateEdit(rules, idx); err != nil {
			return err
		}
		// The rule now validates: a reason Kipple recorded for disabling it goes once it is back on
		// or its matching changed; a rename or move of a still-disabled rule keeps it.
		if f.Enabled || !sameRule(cur[idx], f) {
			if err := dropFilterReason(ctx, tx, id, now); err != nil {
				return err
			}
		}
		if err := checkScopeRefs(ctx, tx, f); err != nil {
			return err
		}
		terms, err := jsonText(f.Terms)
		if err != nil {
			return err
		}
		fields, err := jsonText(f.Fields)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE filters SET name=?, enabled=?, scope=?, folder_id=?, feed_id=?, kind=?, terms=?, fields=?,
			case_sensitive=?, whole_word=?, fold_diacritics=?, invert=?, action=?, position=?, updated_at=? WHERE id=?`,
			f.Name, boolInt(f.Enabled), f.Scope, nullableID(f.FolderID), nullableID(f.FeedID), f.Kind, terms, fields,
			boolInt(f.CaseSensitive), boolInt(f.WholeWord), boolInt(f.FoldDiacritics), boolInt(f.Invert), f.Action, f.Position, now, id); err != nil {
			return err
		}
		out, err = getFilter(ctx, tx, id)
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
	res, ok, err := d.DeleteFilterWithin(ctx, id, unmute, time.Time{}, onBatch)
	return res.Changed, ok, err
}

// DeleteResult is the outcome of one DeleteFilterWithin call.
type DeleteResult struct {
	Changed    int64 // items restored (muted_by cleared)
	MadeUnread int64 // of those, the ones that went back to unread (unmute=unread only)
	Done       bool  // false: the deadline cut the restore short; the rule is disabled, call again
}

// DeleteFilterWithin is DeleteFilter that stops restoring once deadline (zero = none) has passed,
// after at least one batch, so every call makes progress. A cut-off call leaves the rule disabled
// and its row in place (Done false); calling it again resumes where it stopped (see DeleteFilter).
func (d *DB) DeleteFilterWithin(ctx context.Context, id int64, unmute string, deadline time.Time, onBatch func(StateResult)) (out DeleteResult, ok bool, err error) {
	if unmute == "" {
		unmute = UnmuteRead
	}
	if unmute != UnmuteKeep && unmute != UnmuteRead && unmute != UnmuteUnread {
		return out, false, fmt.Errorf("store: unmute mode %q", unmute)
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
				if err := dropFilterReason(ctx, tx, id, d.clock.Now().Unix()); err != nil {
					return err
				}
				d.bumpFilters()
				d.filterTxDone()
			}
			return err
		})
		out.Done = true
		return out, ok, err
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
		return out, false, err
	}

	// 2. Restore its items, batch by batch. Each batch takes the lowest ids still muted by it, so a
	// re-run picks up exactly where a cut-off one stopped. The deadline is checked after each full
	// batch, before the next one waits for the commit gate (so every call makes progress), and a
	// batch whose gate wait or write runs into ctx's end (the caller's hard backstop, past the
	// deadline) ends the call as cut short (Done false, no error) rather than failing it: what
	// committed stays, the rule stays disabled, and the caller calls again.
	orphans := false
	expired := func() bool { return !deadline.IsZero() && time.Now().After(deadline) }
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
			js, err := idsJSON(ids)
			if err != nil {
				return 0, err
			}
			if unmute == UnmuteUnread {
				// Only what was unread before the mute goes back to unread; an item the user had already read
				// (or one muted from the initial-read window) keeps its read state and read_at.
				urows, err := tx.QueryContext(ctx, `UPDATE items SET muted_by = NULL, muted_was_read = NULL,
					read = CASE WHEN COALESCE(muted_was_read, 1) = 1 THEN read ELSE 0 END,
					read_at = CASE WHEN COALESCE(muted_was_read, 1) = 1 THEN read_at ELSE NULL END,
					state_changed_at = CASE WHEN COALESCE(muted_was_read, 1) = 1 OR read = 0 THEN state_changed_at ELSE ?3 END
					WHERE id IN (SELECT value FROM json_each(?1)) AND muted_by = ?2 RETURNING id, feed_id, read`, js, id, d.clock.Now().Unix())
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
			if expired() && errors.Is(err, context.DeadlineExceeded) {
				return out, true, nil // cut short past the budget: resumable, not a failure
			}
			return out, hadRow || orphans, err
		}
		if n == 0 {
			break
		}
		orphans = true
		out.Changed += int64(len(res.Changed))
		out.MadeUnread += int64(len(res.MadeUnread))
		if onBatch != nil && len(res.Changed) > 0 {
			onBatch(res)
		}
		if n == unmuteBatch && expired() {
			return out, true, nil // more may be left: the caller calls again
		}
	}

	// 3. Delete the row last.
	if hadRow {
		err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM filters WHERE id = ?", id); err != nil {
				return err
			}
			if err := dropFilterReason(ctx, tx, id, d.clock.Now().Unix()); err != nil {
				return err
			}
			d.bumpFilters()
			d.filterTxDone()
			return nil
		})
		if err != nil {
			if expired() && errors.Is(err, context.DeadlineExceeded) {
				return out, true, nil // everything is restored; the next call deletes the row
			}
			return out, true, err
		}
	}
	out.Done = true
	return out, hadRow || orphans, nil
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

// MutedCount is the number of muted items, leaving out a feed marked for
// deletion. The partial index answers the total; the (usually absent) deleting
// feeds' muted items are counted through their feed_id and subtracted.
func (d *DB) MutedCount(ctx context.Context) (int64, error) {
	var n int64
	err := d.reader.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM items WHERE muted_by IS NOT NULL)
		- (SELECT count(*) FROM items WHERE muted_by IS NOT NULL AND `+deletingItemSQL+`)`).Scan(&n)
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
	gen       uint64 // the filter generation fs belongs to
	feedID    int64
	folders   []int64 // the feed's folder, then the folders above it (folderChain)
	feedTitle string
}

// newIngestEval loads what the rules need to know about the feed. It returns nil
// when there is nothing to evaluate (no rules), which is the common case and costs
// an atomic load. It runs inside the write transaction, where the generation cannot
// move (filter writes bump it inside their own write transactions).
func (d *DB) newIngestEval(ctx context.Context, tx *sql.Tx, feedID int64, docTitle string) (*ingestEval, error) {
	g := d.filterGen.Load()
	fs := d.filters(ctx, tx)
	if fs == nil || fs.set.Len() == 0 {
		return nil, nil
	}
	e := &ingestEval{fs: fs, gen: g, feedID: feedID}
	if err := e.loadFeed(ctx, tx, docTitle); err != nil {
		return nil, err
	}
	return e, nil
}

// ---- evaluation before the write transaction ----

// preEval holds the rule results of a chunk's fresh candidates, computed on the reader
// before the chunk's write transaction, so the regex work (up to ~180 ms an item in the
// worst case) never runs while the single writer is held. The transaction applies them
// only when they provably match what it would compute itself (usable); otherwise it
// evaluates as before.
type preEval struct {
	gen       uint64 // the filter generation read before the rules were loaded
	fs        *filterSet
	folders   []int64
	feedTitle string
	results   map[string]filter.Result // by uid
}

// preEvaluate evaluates the rules for the items of one chunk that the reader does not know
// yet (neither live nor in the ledger). It returns nil when there are no rules or anything
// fails: the transaction then evaluates on its own, exactly as without this step.
//
// The set is the ingest cache's when that is current (built inside a write transaction, so
// it is exact for its generation), else one compiled from the reader and never cached (a
// reader can see the rows from before a filter write that already bumped the generation;
// usable catches that by comparing the rule lists).
func (d *DB) preEvaluate(ctx context.Context, res *fetch.Result, items []fetch.Item) *preEval {
	if len(items) == 0 {
		return nil
	}
	g := d.filterGen.Load()
	fs := d.cachedFilters(g)
	if fs == nil {
		var err error
		if fs, err = d.readerFilters(ctx); err != nil {
			d.log.Debug("store: pre-evaluate filters; evaluating in the transaction", "feed", res.Snap.ID, "err", err)
			return nil
		}
	}
	if fs.set.Len() == 0 {
		return nil
	}
	docTitle := ""
	if res.Feed != nil {
		docTitle = res.Feed.Title
	}
	e := &ingestEval{fs: fs, gen: g, feedID: res.Snap.ID}
	if err := e.loadFeed(ctx, d.reader, docTitle); err != nil {
		d.log.Debug("store: pre-evaluate filters; evaluating in the transaction", "feed", res.Snap.ID, "err", err)
		return nil
	}
	known, err := d.knownUIDs(ctx, res.Snap.ID, items)
	if err != nil {
		d.log.Debug("store: pre-evaluate filters; evaluating in the transaction", "feed", res.Snap.ID, "err", err)
		return nil
	}
	p := &preEval{gen: g, fs: fs, folders: e.folders, feedTitle: e.feedTitle, results: map[string]filter.Result{}}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if known[it.UID] {
			continue
		}
		if seen[it.UID] {
			delete(p.results, it.UID) // a uid repeated in the chunk is left to the transaction
			continue
		}
		seen[it.UID] = true
		if ctx.Err() != nil {
			return nil
		}
		p.results[it.UID] = e.match(it)
	}
	return p
}

// cachedFilters returns the ingest cache's set when it is current at generation g, else nil.
func (d *DB) cachedFilters(g uint64) *filterSet {
	d.fcache.mu.Lock()
	defer d.fcache.mu.Unlock()
	if d.fcache.ok && d.fcache.gen == g {
		return d.fcache.set
	}
	return nil
}

// readerFilters compiles the current rules from the reader pool (never cached).
func (d *DB) readerFilters(ctx context.Context) (*filterSet, error) {
	all, err := loadFilters(ctx, d.reader)
	if err != nil {
		return nil, err
	}
	rules := make([]filter.Rule, len(all))
	for i, f := range all {
		rules[i] = f.Rule()
	}
	return d.compileSkippingBad(rules), nil
}

// knownUIDs returns the uids of items this feed already has, live or trimmed, as the reader
// sees them. A stale answer only moves work: an item missed here is evaluated in the
// transaction, a result for an item that turns out to exist is never used.
func (d *DB) knownUIDs(ctx context.Context, feedID int64, items []fetch.Item) (map[string]bool, error) {
	uids := make([]string, len(items))
	for i, it := range items {
		uids[i] = it.UID
	}
	b, err := jsonText(uids)
	if err != nil {
		return nil, err
	}
	rows, err := d.reader.QueryContext(ctx, `WITH u(uid) AS MATERIALIZED (SELECT value FROM json_each(?2))
		SELECT uid FROM items WHERE feed_id = ?1 AND uid IN (SELECT uid FROM u)
		UNION ALL
		SELECT uid FROM trimmed_items WHERE feed_id = ?1 AND uid IN (SELECT uid FROM u)`, feedID, b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out[uid] = true
	}
	return out, rows.Err()
}

// usable returns the pre-computed results when the transaction's evaluator e would decide
// every item exactly as they say: same filter generation, the same rules (the very cached set,
// or a set compiled from an equal rule list) and the same folder and feed title. Otherwise nil,
// and the transaction evaluates itself, as it always did.
func (p *preEval) usable(e *ingestEval) map[string]filter.Result {
	if p == nil || e == nil || p.gen != e.gen || !slices.Equal(p.folders, e.folders) || p.feedTitle != e.feedTitle {
		return nil
	}
	if p.fs != e.fs && !reflect.DeepEqual(p.fs.src, e.fs.src) {
		return nil
	}
	return p.results
}

// loadFeed fills the folders and the title a rule's `feed` field sees: feedTitleSQL, except that a
// feed with no title stored yet (a brand-new subscription) uses the fetched document's title, which
// this commit stores as its title, before the URL. The commit and the full-text prediction
// (MutedUIDs) both use it, so they cannot disagree, and a retroactive run sees the same title.
func (e *ingestEval) loadFeed(ctx context.Context, q Querier, docTitle string) error {
	var own sql.NullString
	var url string
	var folder int64
	if err := q.QueryRowContext(ctx, "SELECT f.folder_id, "+feedOwnTitleSQL("f")+", f.url FROM feeds f WHERE f.id = ?", e.feedID).Scan(&folder, &own, &url); err != nil {
		return err
	}
	chain, err := folderChain(ctx, q, folder)
	if err != nil {
		return err
	}
	e.folders = chain
	switch doc := strings.Trim(docTitle, goSpace); {
	case own.Valid:
		e.feedTitle = own.String
	case doc != "":
		e.feedTitle = doc
	default:
		e.feedTitle = url
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
	return e.apply(e.match(it), baseRead, hits)
}

// match runs the rules over one item: the expensive part (the regex and text matching),
// which depends on nothing but the item, the rules, the folder and the feed title.
func (e *ingestEval) match(it fetch.Item) filter.Result {
	return e.fs.set.Evaluate(filter.Item{FeedID: e.feedID, FolderIDs: e.folders, FeedTitle: e.feedTitle,
		Title: it.Title, Author: it.Author, URL: it.URL, Content: it.ContentText, Categories: it.Categories})
}

// apply turns a match result into the item's verdict and hit counts: the cheap part, which
// depends on the item's read state before the rules (known only inside the transaction).
func (e *ingestEval) apply(res filter.Result, baseRead bool, hits map[int64]int) ingestVerdict {
	v := ingestVerdict{read: baseRead}
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
func categoriesJSON(c []string) (any, error) { return optJSON(c, len(c) == 0) }

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
	cs, err := d.readerFilters(ctx)
	if err != nil {
		return nil, err
	}
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

// MutedByFilter counts the muted items per filter id (orphans of deleted filters included), leaving
// out a feed marked for deletion as MutedCount does.
func (d *DB) MutedByFilter(ctx context.Context) (map[int64]int64, error) {
	rows, err := d.reader.QueryContext(ctx, "SELECT muted_by, count(*) FROM items WHERE muted_by IS NOT NULL AND "+notDeletingItemSQL+" GROUP BY muted_by")
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
