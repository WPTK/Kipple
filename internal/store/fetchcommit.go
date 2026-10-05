package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/fetch"
)

const (
	chunkThreshold = 500 // a fetch with more items than this is committed in chunks
	chunkSize      = 250
)

// CommitInfo summarizes one committed fetch.
type CommitInfo struct {
	New     int
	Updated int
	Trimmed int64
	NewIDs  []int64 // ascending; every new item, muted ones included
	// MutedIDs are the new items a mute rule muted (already read, never queued for
	// full text, left out of the fetch.done new_item_ids). Muted, MarkedRead and Starred
	// count what the filters did to this fetch's new items (a match counts only when its
	// action took effect).
	MutedIDs   []int64
	Muted      int
	MarkedRead int
	Starred    int
	// Held maps the uid to the id of every inserted item that was in
	// res.HoldUIDs and not muted: each is marked pending (MarkFulltextPending)
	// from before its transaction committed. The caller owns the marks: it queues
	// the items for extraction or clears them. Only chunks that committed count.
	Held map[string]int64
	// Migrated is set when the commit rewrote feeds.url (design §4.7).
	Migrated bool
	// Retitled is set when the feed's display name (feedTitleSQL) changed: the first fetch naming a
	// new feed, or the feed renaming itself while it has no custom name.
	Retitled bool
	// TrimPending is set when the retention trim filled its bounded batch, so the
	// feed may still hold more than its cap; the scheduler queues a trim job for it.
	TrimPending bool
	// Stale is set when the feed's URL changed under the fetch and nothing (or,
	// for a chunked commit, only the chunks before the change) was
	// written.
	Stale bool
	// URL is the feed's URL after the commit ("" when nothing was written).
	URL string
	// MergedInto is set by CommitDiscovered when another feed already has the discovered URL: this
	// feed (never fetched successfully, so it held no items) was removed instead of becoming a
	// duplicate. Migrated is set with it.
	MergedInto int64
}

type commitState struct {
	migrated   bool
	retitled   bool
	before     int // items in the feed before the fetch
	firstNewID int64
	firstID    int64
	lastID     int64
	newIDs     []int64
	updated    int
	rekeyed    int
	initRead   int
	seenTomb   int
	trimmed    int64
	trimMore   bool // the trim filled its batch: more may be left to trim
	notes      []string
	mutedIDs   []int64
	held       []heldItem // marked pending, in insert order
	fMarked    int
	fStarred   int
	keep       bool
	begun      bool
	stale      bool   // the feed's URL changed under the fetch; nothing was written
	url        string // the feed's URL as this commit leaves it
}

type heldItem struct {
	uid string
	id  int64
}

func (st *commitState) note(s string, keep bool) {
	st.notes = append(st.notes, s)
	st.keep = st.keep || keep
}

// DefaultCommitTimeout bounds each chunk's transaction (gate wait included).
const DefaultCommitTimeout = 10 * time.Second

// CommitFetch applies a successful fetch (ok, unchanged or not_modified) in
// one transaction: items, retention trim, feed bookkeeping, fetch_log
// (design §4.8). More than 500 items are committed in chunks of 250, the gate
// taken and released per chunk, with the trim and bookkeeping in the last one.
//
// Each chunk gets its own DefaultCommitTimeout; ctx should carry no deadline of
// its own (only cancellation). On a failed chunk the error comes back together
// with the CommitInfo of the chunks that did commit: those items are durable,
// so callers must still report them.
func (d *DB) CommitFetch(ctx context.Context, res *fetch.Result) (CommitInfo, error) {
	return d.CommitFetchTimeout(ctx, res, DefaultCommitTimeout)
}

// CommitFetchTimeout is CommitFetch with an explicit per-chunk deadline.
func (d *DB) CommitFetchTimeout(ctx context.Context, res *fetch.Result, perChunk time.Duration) (CommitInfo, error) {
	var items []fetch.Item
	if res.Outcome == fetch.OutcomeOK && res.Feed != nil {
		items = oldestFirst(res.Feed.Items)
	}
	chunks := [][]fetch.Item{items}
	if len(items) > chunkThreshold && !res.Snap.RekeyPending {
		chunks = chunks[:0]
		for i := 0; i < len(items); i += chunkSize {
			chunks = append(chunks, items[i:min(i+chunkSize, len(items))])
		}
	}

	st := &commitState{firstNewID: maxInt64}
	defer func() {
		if v := recover(); v != nil {
			// A panic in a chunk skips the error path below (WithWrite has no
			// recover; its context's cancel rolls the transaction back) and the
			// caller gets no CommitInfo, so it can neither queue nor clear these
			// marks: drop every one this commit set, the committed chunks'
			// included (their items are then left to on-demand extraction, as on
			// a commit error), and let the panic reach the scheduler's recover.
			for _, h := range st.held {
				d.ClearFulltextPending(h.id)
			}
			panic(v)
		}
	}()
	info := func() CommitInfo {
		var held map[string]int64
		if len(st.held) > 0 {
			held = make(map[string]int64, len(st.held))
			for _, h := range st.held {
				held[h.uid] = h.id
			}
		}
		url := st.url
		if !st.begun {
			url = ""
		}
		return CommitInfo{New: len(st.newIDs), Updated: st.updated, Trimmed: st.trimmed, NewIDs: st.newIDs, Migrated: st.migrated, Retitled: st.retitled, Stale: st.stale, TrimPending: st.trimMore,
			MutedIDs: st.mutedIDs, Muted: len(st.mutedIDs), MarkedRead: st.fMarked, Starred: st.fStarred, Held: held,
			URL: url}
	}
	for i, ch := range chunks {
		last := i == len(chunks)-1
		if err := d.commitChunk(ctx, res, ch, last, st, perChunk); err != nil {
			return info(), fmt.Errorf("store: commit fetch of feed %d (chunk %d/%d): %w", res.Snap.ID, i+1, len(chunks), err)
		}
		if st.stale {
			break // the URL changed under the fetch: stop here and report what did commit
		}
	}
	return info(), nil
}

// commitChunkTestHook, when set (tests only), runs at the end of each chunk's
// transaction, before it commits.
var commitChunkTestHook func()

// commitChunk runs one chunk under its own bounded context.
//
// The filter rules are evaluated for the chunk's new items first, on the reader and
// outside the gate (preEvaluate); the transaction only applies those results.
func (d *DB) commitChunk(ctx context.Context, res *fetch.Result, ch []fetch.Item, last bool, st *commitState, perChunk time.Duration) error {
	var pre *preEval
	if !d.testNoPreEval {
		pre = d.preEvaluate(ctx, res, ch)
	}
	if h := d.testAfterPreEval; h != nil {
		h()
	}
	cctx, cancel := context.WithTimeout(ctx, perChunk)
	defer cancel()
	release, err := d.AcquireGate(cctx)
	if err != nil {
		return err
	}
	defer release()
	saved := *st
	err = d.WithWrite(cctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := d.commitTx(ctx, tx, res, ch, last, st, pre); err != nil {
			return err
		}
		if commitChunkTestHook != nil {
			commitChunkTestHook()
		}
		return nil
	})
	if err != nil {
		// The transaction rolled back: its items never became visible, so drop
		// their pending marks, and forget what it collected.
		for _, h := range st.held[len(saved.held):] {
			d.ClearFulltextPending(h.id)
		}
		*st = saved
	}
	return err
}

// oldestFirst orders items by published ascending, ties by reverse document
// order; items without a date sort last (they are stamped with crawl time).
func oldestFirst(in []fetch.Item) []fetch.Item {
	type ent struct {
		it  fetch.Item
		idx int
	}
	es := make([]ent, len(in))
	for i, it := range in {
		es[i] = ent{it, i}
	}
	sort.SliceStable(es, func(a, b int) bool {
		pa, pb := es[a].it.Published, es[b].it.Published
		switch {
		case pa == nil && pb == nil:
			return es[a].idx > es[b].idx
		case pa == nil:
			return false
		case pb == nil:
			return true
		case pa.Equal(*pb):
			return es[a].idx > es[b].idx
		}
		return pa.Before(*pb)
	})
	out := make([]fetch.Item, len(in))
	for i, e := range es {
		out[i] = e.it
	}
	return out
}

type existingRow struct {
	id                    int64
	contentHash, textHash string
	title, author, url    string
	imageURL              string
	wordCount             int
}

func (d *DB) commitTx(ctx context.Context, tx *sql.Tx, res *fetch.Result, items []fetch.Item, last bool, st *commitState, pre *preEval) error {
	feedID := res.Snap.ID
	now := d.clock.Now().Unix()
	// A URL edit that landed while this fetch was in flight makes its result
	// stale: the validators, redirect state and schedule belong to the old URL.
	// Drop this chunk and every later one (the trim, the bookkeeping and the log
	// row with them) and leave the feed row as PATCH set it. The check is per
	// chunk, so a URL edit that lands between chunks of a large fetch leaves the
	// earlier chunks' items durable; CommitInfo.Stale is set and NewIDs/New list
	// exactly what did commit, which callers use (the scheduler still queues
	// full-text extraction for those items).
	var curURL string
	if err := tx.QueryRowContext(ctx, "SELECT url FROM feeds WHERE id = ?", feedID).Scan(&curURL); err != nil {
		return err
	}
	if curURL != res.Snap.URL {
		st.stale = true
		return nil
	}
	first := !st.begun
	st.begun = true
	st.url = curURL

	if first {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE feed_id = ?", feedID).Scan(&st.before); err != nil {
			return err
		}
	}

	if len(items) > 0 {
		if err := d.applyItems(ctx, tx, res, items, first && last, now, st, pre); err != nil {
			return err
		}
	}

	if !last {
		if len(st.newIDs) > 0 {
			return d.saveHighWater(ctx, tx)
		}
		return nil
	}

	// --- last chunk: trim, bookkeeping, log ---
	// One bounded batch; a larger backlog is reported (CommitInfo.TrimPending) for the
	// scheduler to finish with trim jobs rather than holding the writer here.
	// The feed's item count is known when this transaction read it (a single-chunk commit): inserts
	// are the only rows a commit adds, and nothing else writes inside it. After earlier chunks
	// committed, another writer (a restore) may have added rows in between, so the trim counts.
	total := -1
	if first {
		total = st.before + len(st.newIDs)
	}
	var err error
	if st.trimmed, st.trimMore, err = trimFeedBatch(ctx, tx, feedID, now, st.firstNewID, trimBatch, total); err != nil {
		return err
	}

	docSize := 0
	if res.Feed != nil {
		docSize = len(res.Feed.Items)
	}
	if n := len(st.newIDs); n >= 10 && n*10 >= docSize*8 && st.before >= 20 {
		st.note("guid_churn_suspected", true)
	}
	if st.initRead > 0 {
		st.note(fmt.Sprintf("initial_read: %d", st.initRead), false)
	}
	if len(st.mutedIDs)+st.fMarked+st.fStarred > 0 {
		st.note(fmt.Sprintf("filters: muted %d, marked_read %d, starred %d", len(st.mutedIDs), st.fMarked, st.fStarred), false)
	}
	if st.rekeyed > 0 || (res.Snap.RekeyPending && res.Outcome == fetch.OutcomeOK) {
		st.note(fmt.Sprintf("rekeyed: %d", st.rekeyed), true)
	}

	if err := d.applyRedirect(ctx, tx, res, st); err != nil {
		return err
	}

	if res.Outcome == fetch.OutcomeOK && res.Feed != nil {
		f := res.Feed
		var before, after string
		nameSQL := "SELECT " + feedTitleSQL("feeds") + " FROM feeds WHERE id = ?"
		if err := tx.QueryRowContext(ctx, nameSQL, feedID).Scan(&before); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE feeds SET
			title = CASE WHEN ?2 != '' THEN ?2 ELSE title END,
			site_url = CASE WHEN ?3 != '' THEN ?3 ELSE site_url END, description = ?4,
			custom_title = CASE WHEN last_success_at IS NULL AND custom_title = ?2 THEN NULL ELSE custom_title END,
			rekey_pending = 0
			WHERE id = ?1`, feedID, f.Title, f.SiteURL, f.Description); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, nameSQL, feedID).Scan(&after); err != nil {
			return err
		}
		st.retitled = after != before
	}
	if _, err := tx.ExecContext(ctx, `UPDATE feeds SET
		etag = CASE WHEN ?2 THEN NULLIF(?3,'') ELSE etag END,
		last_modified = CASE WHEN ?2 THEN NULLIF(?4,'') ELSE last_modified END,
		body_hash = CASE WHEN ?5 != '' THEN ?5 ELSE body_hash END,
		initial_read_before = NULL,
		last_fetch_at = ?6, last_success_at = ?6, last_status = ?7,
		consecutive_failures = 0,
		next_fetch_at = ?8, current_delay_s = ?9, ttl_hint_s = ?10,
		last_new_items_at = CASE WHEN ?11 > 0 THEN ?6 ELSE last_new_items_at END,
		updated_at = ?6
		WHERE id = ?1`,
		feedID, res.SetValidators, res.ETag, res.LastModified, res.BodyHash,
		now, res.Status, res.NextFetchAt.Unix(), res.CurrentDelayS, res.TTLHintS, len(st.newIDs)); err != nil {
		return err
	}
	if len(st.newIDs) > 0 {
		if err := d.saveHighWater(ctx, tx); err != nil {
			return err
		}
	}

	notes := append(append([]string(nil), res.Notes...), st.notes...)
	var firstID, lastID any
	if len(st.newIDs) > 0 {
		firstID, lastID = st.firstID, st.lastID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fetch_log
		(feed_id, trigger, started_at, duration_ms, outcome, http_status, new_items, updated_items, trimmed_items,
		 first_item_id, last_item_id, bytes, final_url, note, keep)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		feedID, res.Snap.Trigger, res.StartedAt.Unix(), res.Duration.Milliseconds(), res.Outcome, nullInt(res.Status),
		len(st.newIDs), st.updated, st.trimmed, firstID, lastID, res.Bytes, nullStr(res.FinalURL),
		nullStr(strings.Join(notes, "; ")), boolInt(st.keep)); err != nil {
		return err
	}
	return capFetchLog(ctx, tx, feedID, now)
}

// applyItems classifies, updates and inserts one chunk of items.
func (d *DB) applyItems(ctx context.Context, tx *sql.Tx, res *fetch.Result, items []fetch.Item, single bool, now int64, st *commitState, pre *preEval) error {
	feedID := res.Snap.ID
	uids := make([]string, len(items))
	for i, it := range items {
		uids[i] = it.UID
	}
	uidJSON, err := jsonText(uids)
	if err != nil {
		return err
	}

	// One pass over the chunk's uids: live items (kind 0, with the columns updateItem
	// needs) and ledger tombstones (kind 1) come back together.
	existing := map[string]existingRow{}
	tomb := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `WITH u(uid) AS MATERIALIZED (SELECT value FROM json_each(?2))
		SELECT 0, id, uid, content_hash, text_hash, title, author, url, COALESCE(image_url,''), word_count
		  FROM items WHERE feed_id = ?1 AND uid IN (SELECT uid FROM u)
		UNION ALL
		SELECT 1, 0, uid, '', '', '', '', '', '', 0
		  FROM trimmed_items WHERE feed_id = ?1 AND uid IN (SELECT uid FROM u)`, feedID, uidJSON)
	if err != nil {
		return err
	}
	for rows.Next() {
		var kind int
		var uid string
		var e existingRow
		if err := rows.Scan(&kind, &e.id, &uid, &e.contentHash, &e.textHash, &e.title, &e.author, &e.url, &e.imageURL, &e.wordCount); err != nil {
			rows.Close()
			return err
		}
		if kind == 1 {
			tomb[uid] = true
		} else {
			existing[uid] = e
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	var fresh []fetch.Item
	var seenTomb []string
	for _, it := range items {
		switch e, ok := existing[it.UID]; {
		case tomb[it.UID]:
			seenTomb = append(seenTomb, it.UID)
		case ok:
			if e.contentHash != it.ContentHash {
				if err := updateItem(ctx, tx, e, it, now); err != nil {
					return err
				}
				st.updated++
			}
		default:
			fresh = append(fresh, it)
		}
	}

	rekeyLeftover := map[string]bool{}
	if single && res.Snap.RekeyPending && res.Outcome == fetch.OutcomeOK {
		var err error
		if fresh, err = d.rekey(ctx, tx, feedID, res.Feed.Items, fresh, rekeyLeftover, st); err != nil {
			return err
		}
	}

	if len(fresh) > 0 {
		docTitle := ""
		if res.Feed != nil {
			docTitle = res.Feed.Title
		}
		fe, err := d.newIngestEval(ctx, tx, feedID, docTitle)
		if err != nil {
			return err
		}
		// The rules' results were computed before this transaction when they still hold
		// (preEval.usable); only an item they do not cover is matched here.
		preRes := pre.usable(fe)
		hits := map[int64]int{}
		insItem, err := tx.PrepareContext(ctx, `INSERT INTO items
			(id, feed_id, uid, url, title, author, image_url, word_count, content_hash, text_hash,
			 published_at, updated_at, sort_at, read, read_at, starred, starred_at, muted_by, muted_was_read)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer insItem.Close()
		insContent, err := tx.PrepareContext(ctx,
			`INSERT INTO item_content (item_id, content_html, content_text, enclosures_json, categories_json) VALUES (?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer insContent.Close()

		for _, it := range fresh {
			id := d.alloc.Next()
			crawl := id / 1_000_000
			pub := crawl
			if it.Published != nil {
				pub = it.Published.Unix()
			}
			sortAt := min(pub, crawl+86400)
			read := 0
			if rekeyLeftover[it.UID] {
				read = 1
			} else if b := res.Snap.InitialReadBefore; b != 0 && pub < b {
				read = 1
				st.initRead++
			}
			var starred, mutedBy, mutedWasRead, starredAt any
			baseRead := read
			if fe != nil {
				r, ok := preRes[it.UID]
				if !ok {
					if h := d.testTxMatch; h != nil {
						h()
					}
					r = fe.match(it)
				}
				v := fe.apply(r, read == 1, hits)
				if v.read {
					read = 1
				}
				if v.starred {
					starred, starredAt = 1, now
					st.fStarred++
				} else {
					starred = 0
				}
				if v.mutedBy != 0 {
					mutedBy, mutedWasRead = v.mutedBy, baseRead
					st.mutedIDs = append(st.mutedIDs, id)
				}
				if v.marked {
					st.fMarked++
				}
			} else {
				starred = 0
			}
			var readAt, updatedAt any
			if read == 1 {
				readAt = now
			}
			if it.Updated != nil {
				updatedAt = it.Updated.Unix()
			}
			if _, err := insItem.ExecContext(ctx, id, feedID, it.UID, it.URL, it.Title, it.Author, nullStr(it.ImageURL),
				it.WordCount, it.ContentHash, it.TextHash, pub, updatedAt, sortAt, read, readAt, starred, starredAt, mutedBy, mutedWasRead); err != nil {
				return err
			}
			enc, err := optJSON(it.Enclosures, len(it.Enclosures) == 0)
			if err != nil {
				return err
			}
			cats, err := categoriesJSON(it.Categories)
			if err != nil {
				return err
			}
			if _, err := insContent.ExecContext(ctx, id, it.ContentHTML, it.ContentText, enc, cats); err != nil {
				return err
			}
			if res.HoldUIDs[it.UID] && mutedBy == nil {
				// Marked inside the transaction: the item is held from the moment it
				// becomes visible. commitChunk clears the mark if this rolls back.
				d.MarkFulltextPending(id)
				st.held = append(st.held, heldItem{uid: it.UID, id: id})
			}
			if st.firstNewID == maxInt64 {
				st.firstNewID = id
			}
			if len(st.newIDs) == 0 {
				st.firstID = id
			}
			st.lastID = id
			// Every row this commit adds is counted here: trimFeedBatch skips on st.before+len(newIDs).
			st.newIDs = append(st.newIDs, id)
		}
		if err := writeHits(ctx, tx, hits, now); err != nil {
			return err
		}
	}

	if len(seenTomb) > 0 {
		b, err := jsonText(seenTomb)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE trimmed_items SET last_seen_at = ? WHERE feed_id = ? AND uid IN (SELECT value FROM json_each(?))`,
			now, feedID, b); err != nil {
			return err
		}
		st.seenTomb += len(seenTomb)
	}
	return nil
}

func updateItem(ctx context.Context, tx *sql.Tx, e existingRow, it fetch.Item, now int64) error {
	sets := []string{"content_hash = ?", "text_hash = ?"}
	args := []any{it.ContentHash, it.TextHash}
	if it.Title != e.title {
		sets, args = append(sets, "title = ?"), append(args, it.Title)
	}
	if it.Author != e.author {
		sets, args = append(sets, "author = ?"), append(args, it.Author)
	}
	if it.URL != e.url {
		sets, args = append(sets, "url = ?"), append(args, it.URL)
	}
	if it.ImageURL != e.imageURL {
		sets, args = append(sets, "image_url = ?"), append(args, nullStr(it.ImageURL))
	}
	if it.WordCount != e.wordCount {
		sets, args = append(sets, "word_count = ?"), append(args, it.WordCount)
	}
	textChanged := e.textHash != it.TextHash
	if it.Updated != nil {
		sets, args = append(sets, "updated_at = ?"), append(args, it.Updated.Unix())
	}
	if textChanged {
		sets, args = append(sets, "content_changed_at = ?"), append(args, now)
	}
	args = append(args, e.id)
	if _, err := tx.ExecContext(ctx, "UPDATE items SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		return err
	}
	enc, err := optJSON(it.Enclosures, len(it.Enclosures) == 0)
	if err != nil {
		return err
	}
	if textChanged {
		_, err = tx.ExecContext(ctx, `UPDATE item_content SET content_html = ?, content_text = ?, enclosures_json = ? WHERE item_id = ?`,
			it.ContentHTML, it.ContentText, enc, e.id)
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE item_content SET content_html = ?, enclosures_json = ? WHERE item_id = ?`,
		it.ContentHTML, enc, e.id)
	return err
}

// rekey re-keys rows of this feed that are missing from the document by uid
// but whose URL uniquely matches exactly one new item, keeping their state.
// Unmatched new items are inserted read. It returns the items still to insert.
func (d *DB) rekey(ctx context.Context, tx *sql.Tx, feedID int64, doc, fresh []fetch.Item, leftover map[string]bool, st *commitState) ([]fetch.Item, error) {
	inDoc := make(map[string]bool, len(doc))
	for _, it := range doc {
		inDoc[it.UID] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT id, uid, url FROM items WHERE feed_id = ? AND url != ''", feedID)
	if err != nil {
		return nil, err
	}
	cand := map[string][]int64{}
	for rows.Next() {
		var id int64
		var uid, url string
		if err := rows.Scan(&id, &uid, &url); err != nil {
			rows.Close()
			return nil, err
		}
		if !inDoc[uid] {
			cand[url] = append(cand[url], id)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	newByURL := map[string]int{}
	for _, it := range fresh {
		if it.URL != "" {
			newByURL[it.URL]++
		}
	}
	var out []fetch.Item
	for _, it := range fresh {
		if ids := cand[it.URL]; it.URL != "" && len(ids) == 1 && newByURL[it.URL] == 1 {
			if _, err := tx.ExecContext(ctx, "UPDATE items SET uid = ? WHERE id = ?", it.UID, ids[0]); err != nil {
				return nil, err
			}
			st.rekeyed++
			continue
		}
		leftover[it.UID] = true
		out = append(out, it)
	}
	return out, nil
}

// applyRedirect persists the §4.7 decision for a successful fetch.
func (d *DB) applyRedirect(ctx context.Context, tx *sql.Tx, res *fetch.Result, st *commitState) error {
	feedID := res.Snap.ID
	dec := res.Redirect
	switch dec.Action {
	case fetch.RedirectSet:
		_, err := tx.ExecContext(ctx, `UPDATE feeds SET redirect_to = ?, redirect_kind = ?, redirect_count = ? WHERE id = ?`,
			dec.To, dec.Kind, dec.Count, feedID)
		return err
	case fetch.RedirectMigrate:
		other, found, err := FindFeedByURL(ctx, tx, dec.To)
		if err != nil {
			return err
		}
		if found && other != feedID {
			st.note(fmt.Sprintf("redirect_target_owned_by_feed %d", other), false)
			_, err := tx.ExecContext(ctx, `UPDATE feeds SET redirect_to = ?, redirect_kind = 'permanent', redirect_count = ? WHERE id = ?`,
				dec.To, min(dec.Count, 2), feedID)
			return err
		}
		key, kerr := feedurl.Key(dec.To)
		host, herr := feedurl.Host(dec.To)
		if kerr != nil || herr != nil {
			return nil
		}
		// The credentials and the network exceptions were granted for the old
		// host. A move inside the same site (example.com -> www.example.com, a LAN
		// name gaining its domain: nas -> nas.lan) keeps them. A move to another
		// site would have to drop them (so a publisher's redirect cannot collect
		// the password or reach a private address), which silently breaks the
		// feed; so while any is set the move is not made automatically: the
		// redirect stays pending with a note, and the user accepts it by editing
		// the URL (api PATCH, which drops them the same way).
		oldHost, oerr := feedurl.Host(res.Snap.URL)
		moved := oerr != nil || !fetch.SameSite(oldHost, host)
		if moved {
			var held bool
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(http_auth, '') != '' OR allow_insecure_tls = 1 OR allow_private_net = 1
				FROM feeds WHERE id = ?`, feedID).Scan(&held); err != nil {
				return err
			}
			if held {
				st.note(fmt.Sprintf("redirect_held_new_site: %s is on another site; the feed's HTTP credentials or network exceptions apply only to %s, so the move is not automatic: edit the feed URL to accept it (they are then cleared unless the edit keeps them on)", dec.To, oldHost), false)
				_, err := tx.ExecContext(ctx, `UPDATE feeds SET redirect_to = ?, redirect_kind = 'permanent', redirect_count = ? WHERE id = ?`,
					dec.To, min(dec.Count, 2), feedID)
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE feeds SET url_original = COALESCE(url_original, url),
			url_original_key = COALESCE(url_original_key, url_key),
			url = ?2, url_key = ?3, host = ?4, redirect_to = NULL, redirect_kind = NULL, redirect_count = 0,
			http_auth = CASE WHEN ?5 THEN NULL ELSE http_auth END,
			allow_insecure_tls = CASE WHEN ?5 THEN 0 ELSE allow_insecure_tls END,
			allow_private_net = CASE WHEN ?5 THEN 0 ELSE allow_private_net END
			WHERE id = ?1`, feedID, dec.To, key, host, moved); err != nil {
			return err
		}
		st.note(fmt.Sprintf("redirect_migrated: %s -> %s", res.Snap.URL, dec.To), true)
		st.migrated, st.url = true, dec.To
		return nil
	default: // clear
		_, err := tx.ExecContext(ctx, `UPDATE feeds SET redirect_to = NULL, redirect_kind = NULL, redirect_count = 0
			WHERE id = ? AND redirect_to IS NOT NULL`, feedID)
		return err
	}
}

// FeedSnapshotsByID loads the snapshots of the given feeds (enabled or not) in
// one query, in the order of ids; unknown ids are left out. An import run uses it
// for its new feeds.
func (d *DB) FeedSnapshotsByID(ctx context.Context, set FetchSettings, ids []int64) ([]fetch.Snapshot, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	b, err := jsonText(ids)
	if err != nil {
		return nil, err
	}
	snaps, err := d.feedSnapshots(ctx, set, "WHERE id IN (SELECT value FROM json_each(?))", b)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]fetch.Snapshot, len(snaps))
	for _, s := range snaps {
		byID[s.ID] = s
	}
	out := make([]fetch.Snapshot, 0, len(snaps))
	for _, id := range ids {
		if s, ok := byID[id]; ok {
			out = append(out, s)
			delete(byID, id) // a repeated id is loaded once
		}
	}
	return out, nil
}

func (d *DB) saveHighWater(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('sys.id_high_water', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = unixepoch()
		WHERE CAST(excluded.value AS INTEGER) > CAST(settings.value AS INTEGER)`, fmt.Sprint(d.alloc.Last()))
	return err
}

// CommitFetchError is the error bookkeeping transaction: failure counters,
// the backoff schedule, the 410 disable, and the fetch_log row.
func (d *DB) CommitFetchError(ctx context.Context, res *fetch.Result) error {
	now := d.clock.Now().Unix()
	return d.gated(ctx, func(ctx context.Context, tx *sql.Tx) error {
		feedID := res.Snap.ID
		upd, err := tx.ExecContext(ctx, `UPDATE feeds SET
			consecutive_failures = consecutive_failures + 1,
			last_error = ?2, last_error_class = ?3, last_error_at = ?4, last_status = ?5, last_fetch_at = ?4,
			next_fetch_at = ?6, current_delay_s = ?7, updated_at = ?4,
			enabled = CASE WHEN ?8 THEN 0 ELSE enabled END,
			disabled_reason = CASE WHEN ?8 THEN 'gone' ELSE disabled_reason END
			WHERE id = ?1 AND url = ?9`,
			feedID, res.ErrMsg, res.ErrClass, now, nullInt(res.Status), res.NextFetchAt.Unix(), res.CurrentDelayS, res.Gone, res.Snap.URL)
		if err != nil {
			return err
		}
		if n, _ := upd.RowsAffected(); n == 0 {
			return nil // the URL was edited while this fetch ran: the error is about the old URL
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO fetch_log
			(feed_id, trigger, started_at, duration_ms, outcome, http_status, error_class, error, bytes, final_url, note)
			VALUES (?,?,?,?,'error',?,?,?,?,?,?)`,
			feedID, res.Snap.Trigger, res.StartedAt.Unix(), res.Duration.Milliseconds(), nullInt(res.Status),
			res.ErrClass, res.ErrMsg, res.Bytes, nullStr(res.FinalURL), nullStr(strings.Join(res.Notes, "; "))); err != nil {
			return err
		}
		return capFetchLog(ctx, tx, feedID, now)
	})
}

// CommitSkip writes a `skipped` fetch_log row and leaves the schedule alone.
func (d *DB) CommitSkip(ctx context.Context, snap fetch.Snapshot, note string) error {
	now := d.clock.Now().Unix()
	return d.gated(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO fetch_log (feed_id, trigger, started_at, duration_ms, outcome, note)
			VALUES (?,?,?,0,'skipped',?)`, snap.ID, snap.Trigger, now, note); err != nil {
			return err
		}
		return capFetchLog(ctx, tx, snap.ID, now)
	})
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
