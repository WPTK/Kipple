package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/filter"
)

// Retroactive filters (backend-additions-round2 1.5): the dry-run preview and the
// explicit "apply to existing articles" run. Both scan the same candidate set with
// the same evaluation, so the preview count is what an apply then changes.

// ErrFilterNotFound is returned by ApplyFilter for an unknown id.
var ErrFilterNotFound = errors.New("store: no such filter")

const (
	retroPage  = 1000 // rows per keyset page on the reader pool
	retroBatch = 500  // ids per write transaction of an apply
	// PreviewSample is how many matching items a preview returns as samples.
	PreviewSample = 20
	// unsavedFilterID is the id an unsaved preview rule gets in its set: above every real id.
	unsavedFilterID = int64(1) << 40
)

// ErrFilterChanged is returned by ApplyFilter when the rule it is applying was deleted, disabled or
// edited after the run started: the batch that noticed it writes nothing and the run stops.
var ErrFilterChanged = errors.New("store: filter changed or removed during apply")

var errRetroBudget = errors.New("store: filter scan budget exhausted")

// retroItem is one candidate row.
type retroItem struct {
	id, feedID, folderID          int64
	folders                       []int64 // folderID, then the folders above it
	feedTitle, title, author, url string
	read, starred, muted          bool
	content                       string
	categories                    []string
}

func (it retroItem) engine() filter.Item {
	return filter.Item{FeedID: it.feedID, FolderIDs: it.folders, FeedTitle: it.feedTitle, Title: it.title,
		Author: it.author, URL: it.url, Content: it.content, Categories: it.categories}
}

// retroCols is what a retroactive scan loads beyond the narrow item columns: the union of what the
// rules it evaluates read, so no rule sees an empty field it would see filled at ingest.
type retroCols struct {
	content    bool
	contentMax int // bytes of content_text: the largest scan limit among the rules reading it
	cats       bool
}

// colsFor is the columns rules read. The engine truncates each field to its own rule's limit, so
// loading the largest one is exact for every rule.
func colsFor(rules []filter.Rule) retroCols {
	var c retroCols
	for _, r := range rules {
		for _, f := range r.Fields {
			switch f {
			case filter.FieldContent:
				c.content = true
				n := filter.MaxTextContentScan
				if r.Kind == filter.KindRegex {
					n = filter.MaxRegexContentScan
				}
				c.contentMax = max(c.contentMax, n)
			case filter.FieldCategory:
				c.cats = true
			}
		}
	}
	return c
}

// retroSQL builds the candidate query for rule r (its scope decides the candidates); cols selects
// the joins it needs.
func retroSQL(r filter.Rule, includeRead bool, cols retroCols) (from, where string, args []any) {
	from = "items i JOIN feeds f ON f.id = i.feed_id"
	if cols.content || cols.cats {
		from += " LEFT JOIN item_content c ON c.item_id = i.id"
	}
	where = "f.disabled_reason IS NOT 'archive'" // archived items never reach ingest, so rules skip them
	switch r.Scope {
	case filter.ScopeFeed:
		where += " AND i.feed_id = ?"
		args = append(args, r.FeedID)
	case filter.ScopeFolder:
		where += " AND f.folder_id IN (" + folderTreeSQL("?") + ")" // a folder rule covers its subfolders
		args = append(args, r.FolderID)
	}
	if !includeRead {
		where += " AND i.read = 0 AND i.starred = 0" // literals, so the partial indexes are usable
	}
	return
}

// retroScan walks the candidates newest first in keyset pages of retroPage and hands each page to
// onPage. deadline (zero = none) ends the walk between pages and, through the page function's own
// errRetroBudget, inside one.
func (d *DB) retroScan(ctx context.Context, r filter.Rule, rc retroCols, includeRead bool, deadline time.Time, onPage func([]retroItem) error) (truncated bool, err error) {
	from, where, args := retroSQL(r, includeRead, rc)
	cols := `i.id, i.feed_id, f.folder_id, ` + feedTitleSQL("f") + `,
		i.title, i.author, i.url, i.read, i.starred, i.muted_by IS NOT NULL`
	wantContent, wantCats := rc.content, rc.cats
	if wantContent {
		// substr counts characters, the engine bytes: a character is at least a byte, so this is enough.
		cols += fmt.Sprintf(", COALESCE(substr(c.content_text, 1, %d), '')", rc.contentMax)
	}
	if wantCats {
		cols += ", c.categories_json"
	}
	q := "SELECT " + cols + " FROM " + from + " WHERE i.id < ? AND " + where + " ORDER BY i.id DESC LIMIT " + fmt.Sprint(retroPage)
	cursor := maxInt64
	chains := map[int64][]int64{} // folder id -> folderChain, read once per scan
	for {
		if !deadline.IsZero() && time.Now().After(deadline) {
			return true, nil
		}
		rows, err := d.reader.QueryContext(ctx, q, append([]any{cursor}, args...)...)
		if err != nil {
			return false, err
		}
		page := make([]retroItem, 0, retroPage)
		for rows.Next() {
			var it retroItem
			var read, starred, muted int
			var cats sql.NullString
			dest := []any{&it.id, &it.feedID, &it.folderID, &it.feedTitle, &it.title, &it.author, &it.url, &read, &starred, &muted}
			if wantContent {
				dest = append(dest, &it.content)
			}
			if wantCats {
				dest = append(dest, &cats)
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return false, err
			}
			it.read, it.starred, it.muted = read == 1, starred == 1, muted == 1
			if cats.Valid {
				it.categories = decodeStrings(cats.String)
			}
			page = append(page, it)
		}
		if err := rows.Close(); err != nil {
			return false, err
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
		if len(page) == 0 {
			return false, nil
		}
		for i := range page {
			chain, ok := chains[page[i].folderID]
			if !ok {
				if chain, err = folderChain(ctx, d.reader, page[i].folderID); err != nil {
					return false, err
				}
				chains[page[i].folderID] = chain
			}
			page[i].folders = chain
		}
		cursor = page[len(page)-1].id
		if err := onPage(page); err != nil {
			if errors.Is(err, errRetroBudget) {
				return true, nil
			}
			return false, err
		}
		if len(page) < retroPage {
			return false, nil
		}
	}
}

// retroSet is the set a retroactive run evaluates, and the columns its scan must load. The saved
// enabled rules with r in place (or added, forced enabled) are validated as one set, so the set-wide
// limits hold. Evaluation then needs only r and the enabled star rules: retroEffect looks at r's own
// match and, for a mute, whether a star rule cancels it (star beats mute); no other rule changes
// either. Every field those rules read is loaded (a saved star rule on content must see the content
// even when r reads only titles).
func (d *DB) retroSet(ctx context.Context, r filter.Rule) (*filter.Set, filter.Rule, retroCols, error) {
	all, err := loadFilters(ctx, d.reader)
	if err != nil {
		return nil, r, retroCols{}, err
	}
	if r.ID == 0 {
		r.ID = unsavedFilterID
	}
	r.Enabled = true
	rules := make([]filter.Rule, 0, len(all)+1)
	eval := []filter.Rule{r}
	for _, f := range all {
		if f.ID != r.ID && f.Enabled {
			fr := f.Rule()
			rules = append(rules, fr)
			// A saved star rule that no longer compiles is skipped at ingest, so it is skipped here too.
			if fr.Action == filter.ActionStar && filter.Validate(fr) == nil {
				eval = append(eval, fr)
			}
		}
	}
	rules = append(rules, r)
	if err := validateEdit(rules, len(rules)-1); err != nil {
		return nil, r, retroCols{}, err
	}
	set, err := filter.NewSet(eval)
	if err != nil {
		var se *filter.SetError
		if errors.As(err, &se) {
			return nil, r, retroCols{}, se.Err
		}
		return nil, r, retroCols{}, err
	}
	return set, r, colsFor(eval), nil
}

// Effects of a retroactive rule on one item.
type retroKind int

const (
	effNone retroKind = iota
	effMute
	effRead
	effStar
	effHighlight
)

// retroEffect says what applying r would change on it. A match counts only when its action takes
// effect: a mute never touches a starred item (or one a star rule would star) and mark_read only
// helps an unread one.
func retroEffect(set *filter.Set, r filter.Rule, it retroItem) retroKind {
	res := set.Evaluate(it.engine())
	hit := false
	for _, id := range res.Matched {
		if id == r.ID {
			hit = true
			break
		}
	}
	if !hit {
		return effNone
	}
	switch r.Action {
	case filter.ActionMute:
		if res.Muted && !it.starred && !it.muted {
			return effMute
		}
	case filter.ActionMarkRead:
		if !it.read {
			return effRead
		}
	case filter.ActionStar:
		if !it.starred {
			return effStar
		}
	case filter.ActionHighlight:
		return effHighlight
	}
	return effNone
}

// PreviewResult is the outcome of a dry run.
type PreviewResult struct {
	Matches   int
	Scanned   int
	Truncated bool
	SampleIDs []int64 // newest first, at most PreviewSample
}

// PreviewFilter counts the existing items rule r would change (see retroEffect), without writing.
// Paging is by keyset on the reader pool, so no read transaction is held open. budget bounds the
// whole scan; a cut-off scan reports Truncated. f may be unsaved (ID 0). Validation failures are
// *filter.Error.
func (d *DB) PreviewFilter(ctx context.Context, f Filter, includeRead bool, budget time.Duration) (PreviewResult, error) {
	if err := checkScopeRefs(ctx, d.reader, f); err != nil {
		return PreviewResult{}, err
	}
	set, r, rc, err := d.retroSet(ctx, f.Rule())
	if err != nil {
		return PreviewResult{}, err
	}
	var out PreviewResult
	deadline := time.Now().Add(budget)
	truncated, err := d.retroScan(ctx, r, rc, includeRead, deadline, func(page []retroItem) error {
		for _, it := range page {
			// Every item: one item can cost up to filter.MaxRegexCost's worst case, so a check every few
			// dozen items could overrun the budget by seconds.
			if time.Now().After(deadline) {
				return errRetroBudget
			}
			out.Scanned++
			if retroEffect(set, r, it) != effNone {
				out.Matches++
				if len(out.SampleIDs) < PreviewSample {
					out.SampleIDs = append(out.SampleIDs, it.id)
				}
			}
		}
		return nil
	})
	out.Truncated = truncated
	return out, err
}

// ApplyProgress is reported after each scanned page of an apply.
type ApplyProgress struct {
	Done, Total, Changed int
}

// ApplyBatch is one committed write batch, for the caller to publish.
type ApplyBatch struct {
	Action filter.Action
	Res    StateResult
}

// ApplyResult is the outcome of an apply.
type ApplyResult struct {
	Scanned int
	Changed int
}

// countRetro is the number of items ApplyFilter will scan (an upper bound on the changes).
func (d *DB) countRetro(ctx context.Context, r filter.Rule, includeRead bool) (int, error) {
	from, where, args := retroSQL(r, includeRead, retroCols{})
	var n int
	err := d.reader.QueryRowContext(ctx, "SELECT count(*) FROM "+from+" WHERE "+where, args...).Scan(&n)
	return n, err
}

// RetroTotal validates that filter id can be applied and returns it with its candidate count,
// for the run's start event.
func (d *DB) RetroTotal(ctx context.Context, id int64, includeRead bool) (Filter, int, error) {
	f, ok, err := d.GetFilter(ctx, id)
	if err != nil {
		return f, 0, err
	}
	if !ok {
		return f, 0, ErrFilterNotFound
	}
	if !f.Enabled {
		return f, 0, &filter.Error{Field: "enabled", Message: "enable the filter before applying it"}
	}
	if f.Action == string(filter.ActionHighlight) {
		return f, 0, &filter.Error{Field: "action", Message: "a highlight filter has nothing to apply to stored articles (it is drawn by the client)"}
	}
	n, err := d.countRetro(ctx, f.Rule(), includeRead)
	return f, n, err
}

// ApplyFilter applies saved rule id to the existing items: mute (read + muted_by), mark_read or
// star, in write batches of retroBatch ids behind the commit gate. progress and batch (both
// optional) are called after each page and each committed batch. A rule can only add read, never
// clear it; starred items are never muted; a star clears an existing mute. muted_by is the applied
// rule's id (at ingest it is the lowest matching mute rule, but a retroactive apply is one rule).
func (d *DB) ApplyFilter(ctx context.Context, id int64, includeRead bool, total int, progress func(ApplyProgress), batch func(ApplyBatch)) (ApplyResult, error) {
	f, ok, err := d.GetFilter(ctx, id)
	if err != nil {
		return ApplyResult{}, err
	}
	if !ok {
		return ApplyResult{}, ErrFilterNotFound
	}
	applied := f
	r := f.Rule()
	set, r, rc, err := d.retroSet(ctx, r)
	if err != nil {
		return ApplyResult{}, err
	}
	var out ApplyResult
	_, err = d.retroScan(ctx, r, rc, includeRead, time.Time{}, func(page []retroItem) error {
		var ids []int64
		for _, it := range page {
			// A cancel (edit, delete, shutdown) or the caller's deadline is noticed at the next item,
			// not after a whole page of evaluations.
			if err := ctx.Err(); err != nil {
				return err
			}
			if retroEffect(set, r, it) != effNone {
				ids = append(ids, it.id)
			}
		}
		for len(ids) > 0 {
			n := min(len(ids), retroBatch)
			res, err := d.applyBatch(ctx, applied, r, ids[:n])
			if err != nil {
				return err
			}
			ids = ids[n:]
			out.Changed += len(res.Changed)
			if batch != nil && len(res.Changed) > 0 {
				batch(ApplyBatch{Action: r.Action, Res: res})
			}
		}
		out.Scanned += len(page)
		if progress != nil {
			progress(ApplyProgress{Done: out.Scanned, Total: max(total, out.Scanned), Changed: out.Changed})
		}
		return nil
	})
	return out, err
}

// SameRule reports whether two stored filters match and act alike (sameRule), reading an empty field
// list as title only, as the engine does. The API uses it to cancel an apply only on a real change.
func SameRule(a, b Filter) bool {
	normalizeFields(&a)
	normalizeFields(&b)
	return sameRule(a, b)
}

// sameRule reports whether two stored filters are the same rule for matching and acting
// (name, position, hits and timestamps do not matter).
func sameRule(a, b Filter) bool {
	return a.Enabled == b.Enabled && a.Scope == b.Scope && a.Kind == b.Kind && a.Action == b.Action &&
		a.CaseSensitive == b.CaseSensitive && a.WholeWord == b.WholeWord && a.FoldDiacritics == b.FoldDiacritics &&
		a.Invert == b.Invert && slices.Equal(a.Terms, b.Terms) && slices.Equal(a.Fields, b.Fields) &&
		slices.Equal(deref(a.FolderID), deref(b.FolderID)) && slices.Equal(deref(a.FeedID), deref(b.FeedID))
}

func deref(p *int64) []int64 {
	if p == nil {
		return nil
	}
	return []int64{*p}
}

// applyBatch writes one batch. Every UPDATE re-checks the state it expects, so a change made
// between the scan and the write is respected, and the batch first checks, inside its own write
// transaction, that the rule is still stored, enabled and unchanged (ErrFilterChanged otherwise):
// a delete, disable or edit takes the writer like any batch, so nothing is written on behalf of a
// rule after it went away.
func (d *DB) applyBatch(ctx context.Context, applied Filter, r filter.Rule, ids []int64) (StateResult, error) {
	var res StateResult
	now := d.clock.Now().Unix()
	_, err := d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
		cur, err := scanFilter(tx.QueryRowContext(ctx, "SELECT "+filterCols+" FROM filters WHERE id = ?", applied.ID))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (!cur.Enabled || !sameRule(applied, cur))) {
			return 0, ErrFilterChanged
		}
		if err != nil {
			return 0, err
		}
		js, err := idsJSON(ids)
		if err != nil {
			return 0, err
		}
		switch r.Action {
		case filter.ActionMute:
			rows, err := tx.QueryContext(ctx, `UPDATE items SET muted_by = ?1, muted_was_read = read, read_at = CASE WHEN read = 0 THEN ?2 ELSE read_at END,
				state_changed_at = CASE WHEN read = 0 THEN ?2 ELSE state_changed_at END, read = 1
				WHERE id IN (SELECT value FROM json_each(?3)) AND starred = 0 AND muted_by IS NULL RETURNING id, feed_id`, r.ID, now, js)
			if err != nil {
				return 0, err
			}
			if res.Changed, err = scanIDs(rows); err != nil {
				return 0, err
			}
		case filter.ActionMarkRead:
			if res, err = SetRead(ctx, tx, ids, true, now); err != nil {
				return 0, err
			}
		case filter.ActionStar:
			if res, err = SetStarred(ctx, tx, ids, true, now); err != nil {
				return 0, err
			}
		default:
			return 0, fmt.Errorf("store: cannot apply a %s filter", r.Action)
		}
		if len(res.Changed) > 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE filters SET hits = hits + ?, last_hit_at = ? WHERE id = ?", len(res.Changed), now, r.ID); err != nil {
				return 0, err
			}
		}
		return int64(len(res.Changed)), nil
	})
	return res, err
}

// decodeStrings parses a JSON string array; anything else is no categories.
func decodeStrings(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
