package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
func (d *DB) FeedDetail(ctx context.Context, id int64) (FeedDetail, bool, error) {
	l, err := d.uiFeeds(ctx, "f.id = ?", id)
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

// FolderExists reports whether a folder id exists.
func (d *DB) FolderExists(ctx context.Context, id int64) (bool, error) {
	var n int
	err := d.reader.QueryRowContext(ctx, "SELECT count(*) FROM folders WHERE id = ?", id).Scan(&n)
	return n > 0, err
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
	"custom_title": true, "folder_id": true, "position": true, "interval_minutes": true, "retention": true,
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
		var url, urlKey, dedup string
		var reason sql.NullString
		var retention, folder sql.NullInt64
		var custom sql.NullString
		var enabled, private int64
		err := tx.QueryRowContext(ctx, `SELECT url, url_key, dedup_mode, disabled_reason, retention, folder_id, custom_title, enabled, allow_private_net
			FROM feeds WHERE id = ?`, id).Scan(&url, &urlKey, &dedup, &reason, &retention, &folder, &custom, &enabled, &private)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFeedNotFound
		}
		if err != nil {
			return err
		}
		if reason.String == "archive" {
			return ErrArchiveFeed
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
			case "custom_title":
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
			norm, key, host, err := ValidateFeedURL(*p.URL, allow)
			if err != nil {
				return err
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
				sets = append(sets, "url_original = COALESCE(url_original, url)", "url_original_key = COALESCE(url_original_key, url_key)",
					"etag = NULL", "last_modified = NULL", "body_hash = NULL", "ttl_hint_s = NULL",
					"redirect_to = NULL", "redirect_kind = NULL", "redirect_count = 0",
					"consecutive_failures = 0", "current_delay_s = 0")
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
// move to the archive feed first (design §6.9).
func (d *DB) DeleteFeed(ctx context.Context, id int64, deleteStarred bool) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM feeds WHERE id = ?", id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrFeedNotFound
		}
		return removeFeed(ctx, tx, id, !deleteStarred)
	})
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

// ---- folders ----

// CreateFolder adds a folder; position < 0 puts it last.
func (d *DB) CreateFolder(ctx context.Context, name string, position int64) (UIFolder, error) {
	var out UIFolder
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, found, err := FindLabel(ctx, tx, []string{name}); err != nil {
			return err
		} else if found {
			return ErrFolderExists
		}
		var res sql.Result
		var err error
		if position < 0 {
			res, err = tx.ExecContext(ctx, "INSERT INTO folders (name, position) SELECT ?, COALESCE(MAX(position)+1, 1) FROM folders", name)
		} else {
			res, err = tx.ExecContext(ctx, "INSERT INTO folders (name, position) VALUES (?, ?)", name, position)
		}
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out.ID = id
		return tx.QueryRowContext(ctx, "SELECT name, position, is_default FROM folders WHERE id = ?", id).Scan(&out.Name, &out.Position, new(int))
	})
	return out, err
}

// UpdateFolder renames and/or repositions a folder. A name taken by another
// folder is ErrFolderExists (the UI never merges; only the Reader API does).
func (d *DB) UpdateFolder(ctx context.Context, id int64, name *string, position *int64) (UIFolder, error) {
	var out UIFolder
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM folders WHERE id = ?", id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrFolderNotFound
		}
		if name != nil {
			if other, found, err := FindLabel(ctx, tx, []string{*name}); err != nil {
				return err
			} else if found && other != id {
				return ErrFolderExists
			}
			if _, err := tx.ExecContext(ctx, "UPDATE folders SET name = ? WHERE id = ?", *name, id); err != nil {
				return err
			}
		}
		if position != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE folders SET position = ? WHERE id = ?", *position, id); err != nil {
				return err
			}
		}
		var def int
		out.ID = id
		if err := tx.QueryRowContext(ctx, "SELECT name, position, is_default FROM folders WHERE id = ?", id).Scan(&out.Name, &out.Position, &def); err != nil {
			return err
		}
		out.IsDefault = def == 1
		return nil
	})
	return out, err
}

// DeleteFolder deletes a folder, moving its feeds to the default folder; it
// returns their ids. The default folder cannot be deleted.
func (d *DB) DeleteFolder(ctx context.Context, id int64) (moved []int64, err error) {
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var def sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT is_default FROM folders WHERE id = ?", id).Scan(&def); errors.Is(err, sql.ErrNoRows) {
			return ErrFolderNotFound
		} else if err != nil {
			return err
		}
		if def.Int64 == 1 {
			return ErrDefaultFolder
		}
		rows, err := tx.QueryContext(ctx, "UPDATE feeds SET folder_id = 1, updated_at = unixepoch() WHERE folder_id = ? RETURNING id", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var fid int64
			if err := rows.Scan(&fid); err != nil {
				rows.Close()
				return err
			}
			moved = append(moved, fid)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		_, err = tx.ExecContext(ctx, "DELETE FROM folders WHERE id = ?", id)
		return err
	})
	return moved, err
}
