package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/WPTK/kipple/internal/fetch"
)

// Errors of the web UI's feed and folder management (design §7.1).
var (
	ErrFeedNotFound   = errors.New("store: no such feed")
	ErrFolderNotFound = errors.New("store: no such folder")
	ErrArchiveFeed    = errors.New("store: the archive feed cannot be edited")
	ErrFolderExists   = errors.New("store: a folder with that name exists")
	ErrDefaultFolder  = errors.New("store: the default folder cannot be deleted")
	ErrLogNotFound    = errors.New("store: no such fetch log row")
)

// URLCollisionError reports that another feed already owns a URL (a
// FindFeedByURL match on url_key or url_original_key).
type URLCollisionError struct{ Other int64 }

func (e *URLCollisionError) Error() string {
	return fmt.Sprintf("url is already used by feed %d", e.Other)
}

// FeedLabel names a feed for a message: its display title and the folder it is filed in, as a path
// ("News / World"). folder is "" for the default folder (a feed there is not "in a folder" to the
// reader). A missing feed is ErrFeedNotFound.
func (d *DB) FeedLabel(ctx context.Context, id int64) (title, folder string, err error) {
	var fid int64
	var isDefault bool
	err = d.reader.QueryRowContext(ctx, "SELECT "+feedTitleSQL("f")+", f.folder_id, fo.is_default FROM feeds f JOIN folders fo ON fo.id = f.folder_id WHERE f.id = ?", id).
		Scan(&title, &fid, &isDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrFeedNotFound
	}
	if err != nil || isDefault {
		return title, "", err
	}
	names, err := FolderNames(ctx, d.reader, fid)
	return title, strings.Join(names, " / "), err
}

// FeedDetail is a feed as the web UI's add and edit calls return it: the
// bootstrap object plus everything the edit dialog shows. The stored HTTP
// credentials are never returned, only whether they are set.
type FeedDetail struct {
	UIFeed
	URL              string  `json:"url"`
	URLOriginal      *string `json:"url_original"`
	CustomTitle      *string `json:"custom_title"`
	Position         int64   `json:"position"`
	Enabled          bool    `json:"enabled"`
	DisabledReason   *string `json:"disabled_reason"`
	DedupMode        string  `json:"dedup_mode"`
	RekeyPending     bool    `json:"rekey_pending"`
	UserAgent        *string `json:"user_agent"`
	HasHTTPAuth      bool    `json:"has_http_auth"`
	IgnoreHTTPCache  bool    `json:"ignore_http_cache"`
	DisableHTTP2     bool    `json:"disable_http2"`
	AllowInsecureTLS bool    `json:"allow_insecure_tls"`
	AllowPrivateNet  bool    `json:"allow_private_net"`
	NextFetchAt      int64   `json:"next_fetch_at"`
}

// FeedDetail loads one feed (the archive feed included); ok is false when it does not exist.
func (d *DB) FeedDetail(ctx context.Context, id int64, env StatusEnv) (FeedDetail, bool, error) {
	l, err := d.uiFeeds(ctx, env, "f.id = ?", id)
	if err != nil || len(l) == 0 {
		return FeedDetail{}, false, err
	}
	fd := FeedDetail{UIFeed: l[0]}
	var orig, custom, reason, ua, auth sql.NullString
	var enabled, rekey, ignore, h2, insecure, private int64
	err = d.reader.QueryRowContext(ctx, `SELECT url, url_original, custom_title, position, enabled, disabled_reason, dedup_mode,
		rekey_pending, user_agent, http_auth, ignore_http_cache, disable_http2, allow_insecure_tls, allow_private_net, next_fetch_at
		FROM feeds WHERE id = ?`, id).Scan(&fd.URL, &orig, &custom, &fd.Position, &enabled, &reason, &fd.DedupMode,
		&rekey, &ua, &auth, &ignore, &h2, &insecure, &private, &fd.NextFetchAt)
	if err != nil {
		return FeedDetail{}, false, err
	}
	fd.URLOriginal, fd.CustomTitle, fd.DisabledReason, fd.UserAgent = strp(orig), strp(custom), strp(reason), strp(ua)
	fd.Enabled, fd.RekeyPending, fd.HasHTTPAuth = enabled == 1, rekey == 1, auth.String != ""
	fd.IgnoreHTTPCache, fd.DisableHTTP2, fd.AllowInsecureTLS, fd.AllowPrivateNet = ignore == 1, h2 == 1, insecure == 1, private == 1
	return fd, true, nil
}

// FeedPatch is a validated PATCH /api/feeds/{id}. Cols maps whitelisted column
// names to their new value (nil = NULL); URL and Enabled have their own rules.
type FeedPatch struct {
	Cols    map[string]any
	URL     *string
	Enabled *bool
}

// patchable is the whitelist of plain columns. Values are validated by the caller.
var patchable = map[string]bool{
	"custom_title": true, "folder_id": true, "position": true, "interval_minutes": true, "retention": true, "auto_read_days": true,
	"fulltext": true, "dedup_mode": true, "user_agent": true, "http_auth": true, "ignore_http_cache": true,
	"disable_http2": true, "allow_insecure_tls": true, "allow_private_net": true,
}

// PatchResult says what a PatchFeed call changed, for the caller's follow-ups.
type PatchResult struct {
	Changed          bool // anything at all was written
	RetentionChanged bool // enqueue a trim_only job
	NeedsFetch       bool // the URL changed or the feed was enabled: next_fetch_at = now, wake the scheduler
	Notify           bool // a rename, move, URL change, enable or disable: publish feed.changed
}

// PatchFeed applies a validated patch in one transaction (design §7.1).
//
//   - dedup_mode changing sets rekey_pending;
//   - enabled:true clears the disabled reason, resets failures and backoff and
//     sets next_fetch_at = now; enabled:false disables with reason 'user';
//   - a URL change is validated like a create (allow_private_net as it will be
//     after the patch), checked with FindFeedByURL (another feed owning it is a
//     *URLCollisionError), keeps the first URL in url_original(_key), and resets
//     the conditional-request state, redirect bookkeeping, failures and backoff.
func (d *DB) PatchFeed(ctx context.Context, id int64, p FeedPatch) (PatchResult, error) {
	var res PatchResult
	for c := range p.Cols {
		if !patchable[c] {
			return res, fmt.Errorf("store: patch: column %q is not patchable", c)
		}
	}
	now := d.clock.Now().Unix()
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res = PatchResult{}
		var url, urlKey, dedup, oldHost string
		var reason sql.NullString
		var retention, folder sql.NullInt64
		var custom, curUA sql.NullString
		var enabled, private int64
		err := tx.QueryRowContext(ctx, `SELECT url, url_key, host, dedup_mode, disabled_reason, retention, folder_id, custom_title, enabled, allow_private_net, user_agent
			FROM feeds WHERE id = ?`, id).Scan(&url, &urlKey, &oldHost, &dedup, &reason, &retention, &folder, &custom, &enabled, &private, &curUA)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFeedNotFound
		}
		if err != nil {
			return err
		}
		if reason.String == "archive" {
			return ErrArchiveFeed
		}
		if isDeleting(url) {
			return ErrFeedNotFound // being deleted: only finishing the delete is left
		}

		var sets []string
		var args []any
		set := func(col string, v any) {
			sets = append(sets, col+" = ?")
			args = append(args, v)
		}
		for col, v := range p.Cols {
			switch col {
			case "dedup_mode":
				if v != dedup {
					set("rekey_pending", 1)
					res.Changed = true
				}
			case "retention":
				if !sameNullInt(v, retention) {
					res.RetentionChanged = true
				}
			case "folder_id":
				if !sameNullInt(v, folder) {
					res.Notify = true
				}
				var n int
				if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM folders WHERE id = ?", v).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					return ErrFolderNotFound
				}
			case "user_agent":
				// The learned browser-UA flag belongs to the old identity.
				if str, isStr := v.(string); v == nil && curUA.Valid || isStr && (!curUA.Valid || str != curUA.String) {
					sets = append(sets, "ua_fallback = 0")
				}
			case "custom_title":
				// The one name rule (fetch.CleanName), as on every other way a name comes in; a name
				// that is blank once cleaned (only invisible characters) is no name.
				if str, isStr := v.(string); isStr {
					if v = fetch.CleanName(str); v == "" {
						v = nil
					}
				}
				if str, isStr := v.(string); v == nil && custom.Valid || isStr && (!custom.Valid || str != custom.String) {
					res.Notify = true
				}
			}
			set(col, v)
			res.Changed = true
		}

		if p.URL != nil {
			allow := private == 1
			if v, ok := p.Cols["allow_private_net"]; ok {
				allow = truthy(v)
			}
			_, setsPrivate := p.Cols["allow_private_net"]
			norm, key, host, err := ValidateFeedURL(*p.URL, allow)
			if err != nil {
				return err
			}
			// A move to another site resets the network exceptions (the rule a
			// permanent redirect follows, and what its held-redirect note promises):
			// they were granted for the old host. So the new URL is validated
			// without the kept private-network grant.
			crossSite := norm != url && !fetch.SameSite(oldHost, host)
			if crossSite && allow && !setsPrivate {
				if norm, key, host, err = ValidateFeedURL(*p.URL, false); err != nil {
					return err
				}
			}
			if norm != url {
				other, found, err := FindFeedByURL(ctx, tx, norm)
				if err != nil {
					return err
				}
				if found && other != id {
					return &URLCollisionError{Other: other}
				}
				set("url", norm)
				set("url_key", key)
				set("host", host)
				// Credentials are for the host they were entered for: moving the feed
				// to another host must not send them there. A patch that sets
				// http_auth in the same request wins.
				if _, has := p.Cols["http_auth"]; !has && !strings.EqualFold(host, oldHost) {
					sets = append(sets, "http_auth = NULL")
				}
				if crossSite {
					if _, has := p.Cols["allow_insecure_tls"]; !has {
						sets = append(sets, "allow_insecure_tls = 0")
					}
					if !setsPrivate {
						sets = append(sets, "allow_private_net = 0")
					}
				}
				sets = append(sets, "url_original = COALESCE(url_original, url)", "url_original_key = COALESCE(url_original_key, url_key)",
					"etag = NULL", "last_modified = NULL", "body_hash = NULL", "ttl_hint_s = NULL",
					"redirect_to = NULL", "redirect_kind = NULL", "redirect_count = 0",
					"consecutive_failures = 0", "current_delay_s = 0", "ua_fallback = 0",
					// The new URL has never been fetched: its first success brings a backlog, not
					// arrivals, so feed_daily_new leaves it out like a new subscription's.
					"url_succeeded = 0")
				set("next_fetch_at", now)
				res.Changed, res.NeedsFetch, res.Notify = true, true, true
			}
		}

		if p.Enabled != nil {
			switch {
			case *p.Enabled:
				sets = append(sets, "enabled = 1", "disabled_reason = NULL", "consecutive_failures = 0", "current_delay_s = 0")
				set("next_fetch_at", now)
				res.NeedsFetch = true
			case enabled == 1:
				sets = append(sets, "enabled = 0", "disabled_reason = 'user'")
			}
			if *p.Enabled || enabled == 1 {
				res.Changed, res.Notify = true, true
			}
		}

		if len(sets) == 0 {
			return nil
		}
		set("updated_at", now)
		args = append(args, id)
		_, err = tx.ExecContext(ctx, "UPDATE feeds SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
		return err
	})
	return res, err
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int:
		return x == 1
	case int64:
		return x == 1
	}
	return false
}

func sameNullInt(v any, cur sql.NullInt64) bool {
	if v == nil {
		return !cur.Valid
	}
	n, ok := v.(int64)
	if !ok {
		if i, ok2 := v.(int); ok2 {
			n, ok = int64(i), true
		}
	}
	return ok && cur.Valid && cur.Int64 == n
}

// DeleteFeed removes a feed. Unless deleteStarred is set, its starred items
// move to the archive feed first (design §6.9). The feed is first marked for
// deletion in one short transaction (never fetched again, see deletingURLPrefix),
// then its items and ledger are deleted in bounded batches (purgeFeedItems), so
// the final transaction stays short however large the feed; an interrupted
// delete resumes on retry, and the feed is not refetched in between.
func (d *DB) DeleteFeed(ctx context.Context, id int64, deleteStarred bool) error {
	now := d.clock.Now().Unix()
	if err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM feeds WHERE id = ?", id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrFeedNotFound
		}
		return markFeedsDeleting(ctx, tx, []int64{id}, now)
	}); err != nil {
		return err
	}
	if err := d.purgeFeedItems(ctx, id, !deleteStarred); err != nil {
		return err
	}
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM feeds WHERE id = ?", id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrFeedNotFound
		}
		return removeFeed(ctx, tx, id, !deleteStarred)
	})
	if err == nil {
		d.bumpFilters() // the feed's own filters cascade away with it
	}
	return err
}

// PurgeArchiveUnstarred deletes the archive feed's unstarred items and returns
// how many went and the archive feed's id (0 when there is none).
func (d *DB) PurgeArchiveUnstarred(ctx context.Context) (deleted, archiveID int64, err error) {
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT id FROM feeds WHERE disabled_reason = 'archive'").Scan(&archiveID); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM items WHERE starred = 0 AND feed_id = ?", archiveID)
		if err != nil {
			return err
		}
		deleted, err = res.RowsAffected()
		return err
	})
	return deleted, archiveID, err
}

// MarkFetchRead marks read the unread items (and ledger rows) of feedID whose
// id lies in the [first_item_id, last_item_id] range of one of its fetch_log
// rows. It is a bulk mark-read: no stats.
func (d *DB) MarkFetchRead(ctx context.Context, feedID, logID int64) (StateResult, error) {
	var res StateResult
	now := d.clock.Now().Unix()
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var first, last sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT first_item_id, last_item_id FROM fetch_log WHERE id = ? AND feed_id = ?", logID, feedID).Scan(&first, &last)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLogNotFound
		}
		if err != nil {
			return err
		}
		if !first.Valid || !last.Valid {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM items WHERE feed_id = ?1 AND id BETWEEN ?2 AND ?3 AND read = 0
			UNION SELECT id FROM trimmed_items WHERE feed_id = ?1 AND id BETWEEN ?2 AND ?3 AND read = 0`, feedID, first.Int64, last.Int64)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		res, err = SetRead(ctx, tx, ids, true, now)
		return err
	})
	return res, err
}

// ResetTrimmedUnread zeroes a feed's trimmed-unread notice.
func (d *DB) ResetTrimmedUnread(ctx context.Context, feedID int64) error {
	now := d.clock.Now().Unix()
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE feeds SET trimmed_unread_count = 0, trimmed_unread_since = ? WHERE id = ?", now, feedID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrFeedNotFound
		}
		return nil
	})
}

// FetchLogRow is one fetch_log row as GET /api/health/feeds/{id}/log lists it.
type FetchLogRow struct {
	ID           int64   `json:"id,string"`
	Trigger      string  `json:"trigger"`
	StartedAt    int64   `json:"started_at"`
	DurationMS   int64   `json:"duration_ms"`
	Outcome      string  `json:"outcome"`
	HTTPStatus   *int64  `json:"http_status"`
	ErrorClass   *string `json:"error_class"`
	Error        *string `json:"error"`
	NewItems     int64   `json:"new_items"`
	UpdatedItems int64   `json:"updated_items"`
	TrimmedItems int64   `json:"trimmed_items"`
	FirstItemID  *int64  `json:"first_item_id,string"`
	LastItemID   *int64  `json:"last_item_id,string"`
	Bytes        *int64  `json:"bytes"`
	FinalURL     *string `json:"final_url"`
	Note         *string `json:"note"`
	Keep         bool    `json:"keep"`
}

// FetchLog returns a feed's fetch_log rows, newest first: the last 14 days plus
// every kept row. found is false when the feed does not exist.
func (d *DB) FetchLog(ctx context.Context, feedID int64) (rows []FetchLogRow, found bool, err error) {
	var n int
	if err := d.reader.QueryRowContext(ctx, "SELECT count(*) FROM feeds WHERE id = ?", feedID).Scan(&n); err != nil || n == 0 {
		return nil, false, err
	}
	since := d.clock.Now().Unix() - 14*86400
	rs, err := d.reader.QueryContext(ctx, `SELECT id, trigger, started_at, duration_ms, outcome, http_status, error_class, error,
		new_items, updated_items, trimmed_items, first_item_id, last_item_id, bytes, final_url, note, keep
		FROM fetch_log WHERE feed_id = ? AND (started_at >= ? OR keep = 1) ORDER BY id DESC`, feedID, since)
	if err != nil {
		return nil, true, err
	}
	defer rs.Close()
	rows = []FetchLogRow{}
	for rs.Next() {
		var r FetchLogRow
		var status, first, last, bytes sql.NullInt64
		var class, msg, final, note sql.NullString
		var keep int64
		if err := rs.Scan(&r.ID, &r.Trigger, &r.StartedAt, &r.DurationMS, &r.Outcome, &status, &class, &msg,
			&r.NewItems, &r.UpdatedItems, &r.TrimmedItems, &first, &last, &bytes, &final, &note, &keep); err != nil {
			return nil, true, err
		}
		r.HTTPStatus, r.FirstItemID, r.LastItemID, r.Bytes = intp(status), intp(first), intp(last), intp(bytes)
		r.ErrorClass, r.Error, r.FinalURL, r.Note, r.Keep = strp(class), strp(msg), strp(final), strp(note), keep == 1
		rows = append(rows, r)
	}
	return rows, true, rs.Err()
}

// ErrReorder is a bad Reorder request (an unknown or repeated id); it wraps
// the reason for the caller's message.
type ErrReorder struct{ Reason string }

func (e *ErrReorder) Error() string { return "store: reorder: " + e.Reason }

// FolderOrder is one folder of a Reorder: its position is its index in the list, and Parent, when
// not nil, moves it inside that folder first (0 = the top level).
type FolderOrder struct {
	ID     int64
	Parent *int64
}

// FeedOrder is the wanted order of the feeds of one folder.
type FeedOrder struct {
	FolderID int64
	IDs      []int64
}

// ReorderResult lists what actually changed.
type ReorderResult struct {
	Feeds   []int64 // feeds whose position or folder moved
	Folders []int64 // folders whose position or parent moved
}

// Reorder moves folders to new parents and sets folder positions (0..n-1 in the
// order given), and sets feed positions (0..n-1 inside each named folder, moving
// a feed into that folder if it is elsewhere), in ONE transaction. Each folder
// move is checked by the folder writer's own rules (placeFolder, as UpdateFolder
// checks it) against the tree the earlier entries left, not against the final
// tree: moves that are only valid together (a swap, or a move that fits only
// after a later entry takes a subfolder out) are refused, so callers send them
// in separate requests or in an order where each step is valid. Any unknown id,
// repeated id, the archive feed or a refused move aborts the whole call with
// nothing written (*ErrReorder, ErrArchiveFeed, or the folder writer's error:
// ErrFolderCycle, ErrFolderDepth, ErrFolderParent, ErrFolderExists,
// ErrParentNotFound). Only rows whose values change are written.
func (d *DB) Reorder(ctx context.Context, folders []FolderOrder, feeds []FeedOrder) (ReorderResult, error) {
	var res ReorderResult
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res = ReorderResult{}
		seenF := map[int64]bool{}
		moved := false
		for i, f := range folders {
			id := f.ID
			if seenF[id] {
				return &ErrReorder{fmt.Sprintf("folder %d is listed twice", id)}
			}
			seenF[id] = true
			var pos int64
			var parent sql.NullInt64
			var name string
			if err := tx.QueryRowContext(ctx, "SELECT position, parent_id, name FROM folders WHERE id = ?", id).Scan(&pos, &parent, &name); errors.Is(err, sql.ErrNoRows) {
				return &ErrReorder{fmt.Sprintf("no such folder %d", id)}
			} else if err != nil {
				return err
			}
			changed := false
			if f.Parent != nil && *f.Parent != parent.Int64 {
				if err := placeAndSave(ctx, tx, id, *f.Parent, name); err != nil {
					return err
				}
				moved, changed = true, true
			}
			if pos != int64(i) {
				if _, err := tx.ExecContext(ctx, "UPDATE folders SET position = ? WHERE id = ?", i, id); err != nil {
					return err
				}
				changed = true
			}
			if changed {
				res.Folders = append(res.Folders, id)
			}
		}
		if moved {
			d.bumpFilters() // folder filters cover subfolders: the feeds a rule matches changed
		}
		seenG := map[int64]bool{}
		seenID := map[int64]bool{}
		for _, g := range feeds {
			if seenG[g.FolderID] {
				return &ErrReorder{fmt.Sprintf("folder %d has two feed lists", g.FolderID)}
			}
			seenG[g.FolderID] = true
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM folders WHERE id = ?", g.FolderID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return &ErrReorder{fmt.Sprintf("no such folder %d", g.FolderID)}
			}
			for i, id := range g.IDs {
				if seenID[id] {
					return &ErrReorder{fmt.Sprintf("feed %d is listed twice", id)}
				}
				seenID[id] = true
				var pos, folder int64
				var reason sql.NullString
				err := tx.QueryRowContext(ctx, "SELECT position, folder_id, disabled_reason FROM feeds WHERE id = ?", id).Scan(&pos, &folder, &reason)
				if errors.Is(err, sql.ErrNoRows) {
					return &ErrReorder{fmt.Sprintf("no such feed %d", id)}
				} else if err != nil {
					return err
				}
				if reason.String == "archive" {
					return ErrArchiveFeed
				}
				if pos != int64(i) || folder != g.FolderID {
					if _, err := tx.ExecContext(ctx, "UPDATE feeds SET position = ?, folder_id = ?, updated_at = unixepoch() WHERE id = ?", i, g.FolderID, id); err != nil {
						return err
					}
					res.Feeds = append(res.Feeds, id)
				}
			}
		}
		return nil
	})
	if err != nil {
		return ReorderResult{}, err
	}
	return res, nil
}
