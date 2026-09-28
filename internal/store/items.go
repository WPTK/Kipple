package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

// StreamFilter is the predicate of a Reader API stream plus its it/xt state
// filters (design §6.4). Contradictory Read or Starred values simply match nothing.
type StreamFilter struct {
	Empty    bool  // matches nothing (broadcast, like, unknown streams)
	FeedID   int64 // feed_id = FeedID when non-zero
	FolderID int64 // feed in folder FolderID when non-zero
	Read     []int // each entry ANDs read = v
	Starred  []int // each entry ANDs starred = v
	// HoldCut, when positive, hides items still held back from the Reader API
	// (see HeldSQL): id > HoldCut, no item_fulltext row yet, effective full-text mode 1.
	// It is (now - hold window) in microseconds, the same unit as ids.
	HoldCut int64
	// FulltextAll is fetch.fulltext_all and HoldPending the pending set (DB.HoldPending), both for
	// HeldSQL; StreamIDs fills them in.
	FulltextAll bool
	HoldPending string
	// Deleting is the JSON array of the feeds marked for deletion, "" when there are none (the usual
	// case, so the query and its index plan stay as they are); StreamIDs fills it in.
	Deleting string
}

// HeldSQL is the predicate for an items row that the Reader API holds back
// (design §6.5): a full-text item whose extraction has not finished (neither a
// result nor a stored error) and that is younger than the hold window. Its id
// is its crawl time in microseconds, so "younger" is id > :hold_cut. Bind
// :hold_cut and :pending (see DB.HoldPending) with sql.Named. Evaluated in SQL so paging and LIMIT stay correct.
//
// all is fetch.fulltext_all (see FulltextModeSQL).
func HeldSQL(all bool) string {
	return `(items.id > :hold_cut
  AND NOT EXISTS (SELECT 1 FROM item_fulltext WHERE item_fulltext.item_id = items.id)
  AND items.id IN (SELECT value FROM json_each(:pending))
  AND ` + FulltextModeSQL("items.fulltext_mode", "(SELECT feeds.fulltext FROM feeds WHERE feeds.id = items.feed_id)", all) + ` = 1)`
}

func intPreds(col string, vs []int) string {
	var b strings.Builder
	for _, v := range vs {
		if v != 0 {
			v = 1
		}
		fmt.Fprintf(&b, " AND %s = %d", col, v) // literals, so the partial indexes are usable
	}
	return b.String()
}

// where renders the predicate (always starts with "1") and its named args.
func (f StreamFilter) where() (string, []any) {
	w := "1"
	var args []any
	if f.FeedID != 0 {
		w += " AND feed_id = :feed"
		args = append(args, sql.Named("feed", f.FeedID))
	}
	if f.FolderID != 0 {
		w += " AND feed_id IN (SELECT id FROM feeds WHERE folder_id = :folder)"
		args = append(args, sql.Named("folder", f.FolderID))
	}
	w += intPreds("read", f.Read) + intPreds("starred", f.Starred)
	if f.Deleting != "" && !slices.ContainsFunc(f.Starred, func(v int) bool { return v != 0 }) {
		// A feed being deleted is gone from the streams as from subscription/list and unread-count; a
		// starred stream keeps its items, which move to the archive feed when the deletion finishes.
		w += " AND feed_id NOT IN (SELECT value FROM json_each(:deleting))"
		args = append(args, sql.Named("deleting", f.Deleting))
	}
	if f.HoldCut > 0 {
		w += " AND NOT " + HeldSQL(f.FulltextAll)
		args = append(args, sql.Named("hold_cut", f.HoldCut), sql.Named("pending", f.HoldPending))
	}
	return w, args
}

// IDPage is the paging and time-window part of stream/items/ids.
type IDPage struct {
	N       int   // rows to return (the query fetches N+1 to detect a next page)
	Asc     bool  // r=o
	Cont    int64 // c=, valid only when HasCont
	HasCont bool
	OT      int64 // ot= seconds, valid only when HasOT
	HasOT   bool
	NT      int64 // nt= seconds, valid only when HasNT
	HasNT   bool
	// UserChanges (greader.ot_includes_user_changes) adds items whose read or starred state
	// changed since ot (read, unread, star or unstar: items.state_changed_at) to the second leg,
	// so a sync app that passes ot also hears about state changes made in Kipple.
	UserChanges bool
}

// otSlack is the 120 s slack on both legs of the ot filter (design §3).
const otSlack = 120

// streamIDsSQL builds the id query (design §6.5): no ot is one ordered range;
// with ot it is two disjoint ordered legs (crawled after ot, then content
// changed after ot among older ids) merged, so continuation stays valid
// across both legs.
func streamIDsSQL(f StreamFilter, p IDPage) (string, []any) {
	preds, args := f.where()
	cont := p.Cont
	if !p.HasCont {
		if p.Asc {
			cont = 0
		} else {
			cont = math.MaxInt64
		}
	}
	args = append(args, sql.Named("c", cont), sql.Named("n1", p.N+1))
	nt := ""
	if p.HasNT {
		nt = " AND id < :nt_us"
		args = append(args, sql.Named("nt_us", (p.NT+1)*1_000_000))
	}
	order, cmp := "DESC", "<"
	if p.Asc {
		order, cmp = "ASC", ">"
	}
	if !p.HasOT {
		return fmt.Sprintf(`SELECT id FROM items WHERE %s AND id %s :c%s ORDER BY id %s LIMIT :n1`, preds, cmp, nt, order), args
	}
	otS := p.OT - otSlack
	args = append(args, sql.Named("ot_s", otS), sql.Named("ot_us", otS*1_000_000))
	var leg2Bound string
	if p.Asc {
		leg2Bound = "id < :ot_us AND id > :c"
	} else {
		leg2Bound = "id < min(:ot_us, :c)"
	}
	leg2 := `SELECT id FROM (SELECT id FROM items INDEXED BY idx_items_changed
                  WHERE content_changed_at >= :ot_s AND %[5]s AND %[2]s%[3]s ORDER BY id %[4]s LIMIT :n1)`
	legs := "UNION ALL"
	if p.UserChanges {
		// Items whose read or starred state changed since ot (state_changed_at, set by every
		// change after ingest, unread and unstar included) join leg 2 as a third ordered branch
		// on their own partial index. An item can match both branches of leg 2, so the compound
		// is UNION (distinct); leg 1 and leg 2 stay disjoint by id, so nothing else changes.
		leg2 += `
  UNION
  SELECT id FROM (SELECT id FROM items INDEXED BY idx_items_state_changed
                  WHERE state_changed_at >= :ot_s AND %[5]s AND %[2]s%[3]s ORDER BY id %[4]s LIMIT :n1)`
		legs = "UNION"
	}
	q := fmt.Sprintf(`SELECT id FROM (
  SELECT id FROM (SELECT id FROM items WHERE id >= :ot_us AND id %[1]s :c AND %[2]s%[3]s ORDER BY id %[4]s LIMIT :n1)
  `+legs+`
  `+leg2+`
) ORDER BY id %[4]s LIMIT :n1`, cmp, preds, nt, order, leg2Bound)
	return q, args
}

// StreamIDs runs the id query on the reader pool and calls fn for each of the
// first p.N ids in order, one row at a time. more is true when an N+1th row
// exists (the caller then emits last as the continuation).
func (d *DB) StreamIDs(ctx context.Context, f StreamFilter, p IDPage, fn func(id int64) error) (last int64, more bool, err error) {
	if f.Empty || p.N <= 0 {
		return 0, false, nil
	}
	f.FulltextAll = d.FulltextAll(ctx)
	f.HoldPending = d.HoldPending()
	// Looked up here rather than as a subquery in the stream predicate, so the usual case (no feed being
	// deleted) keeps its query text and covering-index plans exactly as they are.
	if !slices.ContainsFunc(f.Starred, func(v int) bool { return v != 0 }) { // a starred stream keeps them anyway
		deleting, err := deletingFeedIDs(ctx, d.reader)
		if err != nil {
			return 0, false, err
		}
		if len(deleting) > 0 {
			if f.Deleting, err = idsJSON(deleting); err != nil {
				return 0, false, err
			}
		}
	}
	q, args := streamIDsSQL(f, p)
	rows, err := d.reader.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return last, false, err
		}
		if n == p.N {
			return last, true, nil
		}
		if err := fn(id); err != nil {
			return last, false, err
		}
		last = id
		n++
	}
	return last, false, rows.Err()
}

// ContentRow is one item as served by stream/items/contents.
type ContentRow struct {
	ID           int64
	FeedID       int64
	URL          string
	Title        string
	Author       string
	HTML         string // item_content.content_html
	Published    int64
	Updated      sql.NullInt64
	Read         bool
	Starred      bool
	Enclosures   string // enclosures_json, "" when none
	OriginTitle  string // "" unless re-parented to the archive feed
	FeedTitle    string
	SiteURL      string
	Folder       string
	UseFulltext  bool           // EffectiveFulltext = 1
	FulltextHTML sql.NullString // extracted text, when one exists
}

// StreamItems calls fn for each requested id that is still in items (trimmed
// and unknown ids are absent), one row at a time, ordered by id (ascending when
// asc). The id list is bound as one JSON array, so any count is one statement.
func (d *DB) StreamItems(ctx context.Context, ids []int64, asc bool, holdCut int64, fn func(*ContentRow) error) error {
	if len(ids) == 0 {
		return nil
	}
	js, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	order := "DESC"
	if asc {
		order = "ASC"
	}
	// Held items (holdCut > 0) are absent here too, so a client cannot fetch one by id early.
	all := d.FulltextAll(ctx) // once per request: the hold and the content mode must agree
	held := ""
	args := []any{string(js)}
	if holdCut > 0 {
		held = " AND NOT " + strings.ReplaceAll(HeldSQL(all), "items.", "i.")
		args = append(args, d.holdArgs(holdCut)...)
	}
	rows, err := d.reader.QueryContext(ctx, `
SELECT i.id, i.feed_id, i.url, i.title, i.author, c.content_html, i.published_at, i.updated_at,
       i.read, i.starred, c.enclosures_json, i.origin_title,
       COALESCE(f.custom_title, f.title), f.site_url, fo.name,
       `+FulltextModeSQL("i.fulltext_mode", "f.fulltext", all)+`, ft.content_html
FROM items i JOIN item_content c ON c.item_id = i.id
JOIN feeds f ON f.id = i.feed_id JOIN folders fo ON fo.id = f.folder_id
LEFT JOIN item_fulltext ft ON ft.item_id = i.id
WHERE i.id IN (SELECT value FROM json_each(?))`+held+`
ORDER BY i.id `+order, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r ContentRow
		var read, starred, ft int
		var enc, origin sql.NullString
		if err := rows.Scan(&r.ID, &r.FeedID, &r.URL, &r.Title, &r.Author, &r.HTML, &r.Published, &r.Updated,
			&read, &starred, &enc, &origin, &r.FeedTitle, &r.SiteURL, &r.Folder, &ft, &r.FulltextHTML); err != nil {
			return err
		}
		r.Read, r.Starred, r.UseFulltext = read == 1, starred == 1, ft == 1
		r.Enclosures, r.OriginTitle = enc.String, origin.String
		if err := fn(&r); err != nil {
			return err
		}
	}
	return rows.Err()
}
