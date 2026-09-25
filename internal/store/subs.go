package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/fetch"
)

// Subscription is one row of the Reader API subscription list.
type Subscription struct {
	ID       int64
	Title    string // COALESCE(custom_title, title)
	URL      string
	SiteURL  string
	Folder   string
	IconHash string // "" when the feed has no icon
}

// Subscriptions lists feeds for subscription/list (design §6.9): disabled and
// gone feeds included, the archive feed only while it holds items.
func (d *DB) Subscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := d.reader.QueryContext(ctx, `
		SELECT f.id, COALESCE(f.custom_title, f.title), f.url, f.site_url, fo.name, COALESCE(fi.hash, '')
		FROM feeds f JOIN folders fo ON fo.id = f.folder_id
		LEFT JOIN feed_icons fi ON fi.feed_id = f.id
		WHERE f.disabled_reason IS NOT 'archive' OR EXISTS (SELECT 1 FROM items WHERE feed_id = f.id)
		ORDER BY fo.position, fo.name, f.position, COALESCE(f.custom_title, f.title)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.ID, &s.Title, &s.URL, &s.SiteURL, &s.Folder, &s.IconHash); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// FolderNames lists folders in display order (tag/list).
func (d *DB) FolderNames(ctx context.Context) ([]string, error) {
	rows, err := d.reader.QueryContext(ctx, "SELECT name FROM folders ORDER BY position, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// FeedIcon returns the icon bytes for a feed when its hash matches.
func (d *DB) FeedIcon(ctx context.Context, feedID int64, hash string) (data []byte, contentType string, ok bool, err error) {
	err = d.reader.QueryRowContext(ctx, "SELECT data, content_type FROM feed_icons WHERE feed_id = ? AND hash = ?", feedID, hash).
		Scan(&data, &contentType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	return data, contentType, err == nil, err
}

// BoolSetting reads a boolean setting through the reader pool.
func (d *DB) BoolSetting(ctx context.Context, key string, def bool) bool {
	return settingBool(ctx, d.reader, key, def)
}

// FindLabel resolves a folder by name (case-insensitively), trying each
// candidate in order (design §6.2 label lookup).
func FindLabel(ctx context.Context, q Querier, candidates []string) (id int64, found bool, err error) {
	for _, c := range candidates {
		if strings.TrimSpace(c) == "" {
			continue
		}
		err = q.QueryRowContext(ctx, "SELECT id FROM folders WHERE name = ? COLLATE NOCASE", c).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, err
		}
	}
	return 0, false, nil
}

// FindLabel is FindLabel on the reader pool.
func (d *DB) FindLabel(ctx context.Context, candidates []string) (int64, bool, error) {
	return FindLabel(ctx, d.reader, candidates)
}

// ensureFolder returns the id of the folder called name, creating it at the end.
func ensureFolder(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 1, nil
	}
	id, found, err := FindLabel(ctx, tx, []string{name})
	if err != nil || found {
		return id, err
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO folders (name, position) SELECT ?, COALESCE(MAX(position)+1, 1) FROM folders", name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InvalidURLError is returned by Subscribe for a URL that cannot be a feed.
type InvalidURLError struct{ Reason string }

func (e *InvalidURLError) Error() string { return e.Reason }

// SubscribeOpts describes one API subscribe.
type SubscribeOpts struct {
	URL    string
	Folder string // label name; "" leaves an existing feed where it is (new feeds go to the default folder)
	Title  string // custom title; "" leaves it unchanged
	// FolderID places a new feed in that folder (which must exist) when Folder is
	// empty; the web UI addresses folders by id. 0 = the default folder.
	FolderID int64
}

// SubscribeResult is what quickadd and ac=subscribe report.
type SubscribeResult struct {
	FeedID  int64
	Title   string // display title
	Existed bool
}

// Subscribe is the API subscribe path (design §6.9, decision 33): idempotent on
// FindFeedByURL, no outbound HTTP and no discovery. A new feed is inserted with
// next_fetch_at = now and its host as title; the scheduler picks it up. An
// existing feed is moved or renamed only if a folder or title was given.
func (d *DB) Subscribe(ctx context.Context, o SubscribeOpts) (SubscribeResult, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(o.URL), "feed/"))
	norm, key, host, err := ValidateFeedURL(raw, false)
	if err != nil {
		return SubscribeResult{}, err
	}
	now := d.clock.Now().Unix()
	var res SubscribeResult
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		id, found, err := FindFeedByURL(ctx, tx, norm)
		if err != nil {
			return err
		}
		if found {
			res.Existed = true
			if err := applyFeedEdit(ctx, tx, id, o.Folder, o.Folder != "", o.Title); err != nil {
				return err
			}
		} else {
			folder, err := ensureFolder(ctx, tx, o.Folder)
			if err != nil {
				return err
			}
			if strings.TrimSpace(o.Folder) == "" && o.FolderID > 0 {
				// Checked in this transaction: a folder deleted after the caller's own
				// check must answer folder_not_found, not a foreign-key failure.
				var n int
				if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM folders WHERE id = ?", o.FolderID).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					return ErrFolderNotFound
				}
				folder = o.FolderID
			}
			var pos int64
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position)+1, 0) FROM feeds").Scan(&pos); err != nil {
				return err
			}
			var custom any
			if t := strings.TrimSpace(o.Title); t != "" {
				custom = t
			}
			r, err := tx.ExecContext(ctx, `INSERT INTO feeds (folder_id, url, url_key, host, title, custom_title, position, next_fetch_at)
				VALUES (?,?,?,?,?,?,?,?)`, folder, norm, key, host, host, custom, pos, now)
			if err != nil {
				return err
			}
			if id, err = r.LastInsertId(); err != nil {
				return err
			}
		}
		res.FeedID = id
		return tx.QueryRowContext(ctx, "SELECT COALESCE(custom_title, title) FROM feeds WHERE id = ?", id).Scan(&res.Title)
	})
	return res, err
}

// ValidateFeedURL is the one URL check for every way a feed URL enters the
// database (Reader subscribe, web add, web URL edit): an absolute http(s) URL
// with a host, normalized, and not a literal blocked address (loopback,
// private, link-local, ...) unless the feed allows private networks. Hostnames
// that resolve to blocked addresses are stopped at dial time by the fetch
// guard. It returns the normalized URL, its key and its host.
func ValidateFeedURL(raw string, allowPrivate bool) (norm, key, host string, err error) {
	norm, nerr := feedurl.Normalize(raw)
	if nerr != nil {
		return "", "", "", &InvalidURLError{"not an absolute http(s) URL"}
	}
	host, _ = feedurl.Host(norm)
	if ip, perr := netip.ParseAddr(host); perr == nil && !allowPrivate && fetch.Blocked(ip.Unmap()) {
		return "", "", "", &InvalidURLError{"address not allowed"}
	}
	key, _ = feedurl.Key(norm)
	return norm, key, host, nil
}

// FeedRef identifies a feed by numeric id or by URL (feed/<n> vs feed/<url>).
type FeedRef struct {
	ID  int64
	URL string
}

// resolveFeed returns the feed id for ref, or 0 when there is none.
func resolveFeed(ctx context.Context, q Querier, ref FeedRef) (int64, error) {
	if ref.ID > 0 {
		var id int64
		err := q.QueryRowContext(ctx, "SELECT id FROM feeds WHERE id = ?", ref.ID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return id, err
	}
	if ref.URL == "" {
		return 0, nil
	}
	id, found, err := FindFeedByURL(ctx, q, ref.URL)
	if err != nil || !found {
		return 0, nil // an unparseable URL is just "no such feed"
	}
	return id, nil
}

// applyFeedEdit moves and renames one feed. folder is applied when setFolder is
// true ("" then means the default folder); title when non-blank.
func applyFeedEdit(ctx context.Context, tx *sql.Tx, id int64, folder string, setFolder bool, title string) error {
	if setFolder {
		fid, err := ensureFolder(ctx, tx, folder)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE feeds SET folder_id = ?, updated_at = unixepoch() WHERE id = ? AND folder_id != ?", fid, id, fid); err != nil {
			return err
		}
	}
	if t := strings.TrimSpace(title); t != "" {
		if _, err := tx.ExecContext(ctx, "UPDATE feeds SET custom_title = ?, updated_at = unixepoch() WHERE id = ? AND custom_title IS NOT ?", t, id, t); err != nil {
			return err
		}
	}
	return nil
}

// EditOpts is subscription/edit ac=edit: a non-blank Title renames, a set
// Folder (creating it if needed) moves, and MoveToDefault (r= without a=)
// moves to Uncategorized.
type EditOpts struct {
	Title         string
	Folder        string
	SetFolder     bool
	MoveToDefault bool
}

// EditSubscription applies EditOpts to each referenced feed; unknown feeds are ignored.
// It returns the ids that exist.
func (d *DB) EditSubscription(ctx context.Context, refs []FeedRef, o EditOpts) (feedIDs []int64, err error) {
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, ref := range refs {
			id, err := resolveFeed(ctx, tx, ref)
			if err != nil {
				return err
			}
			if id == 0 {
				continue
			}
			feedIDs = append(feedIDs, id)
			switch {
			case o.SetFolder:
				err = applyFeedEdit(ctx, tx, id, o.Folder, true, o.Title)
			case o.MoveToDefault:
				err = applyFeedEdit(ctx, tx, id, "", true, o.Title)
			default:
				err = applyFeedEdit(ctx, tx, id, "", false, o.Title)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	return feedIDs, err
}

// Unsubscribe removes feeds (design decision 24): starred items are re-parented
// to the archive feed first, then the feed is deleted (cascading its items,
// ledger, stubs, log and icon). Unsubscribing the archive feed deletes it.
// Unknown feeds are ignored; the ids that existed are returned.
func (d *DB) Unsubscribe(ctx context.Context, refs []FeedRef) (feedIDs []int64, err error) {
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, ref := range refs {
			id, err := resolveFeed(ctx, tx, ref)
			if err != nil {
				return err
			}
			if id == 0 {
				continue
			}
			feedIDs = append(feedIDs, id)
			if err := removeFeed(ctx, tx, id, true); err != nil {
				return err
			}
		}
		return nil
	})
	return feedIDs, err
}

// removeFeed deletes a feed. With archiveStarred its starred items are first
// re-parented to the archive feed (design decision 24); the archive feed itself
// is always really deleted.
func removeFeed(ctx context.Context, tx *sql.Tx, id int64, archiveStarred bool) error {
	var reason sql.NullString
	var title string
	if err := tx.QueryRowContext(ctx, "SELECT disabled_reason, COALESCE(custom_title, title) FROM feeds WHERE id = ?", id).Scan(&reason, &title); err != nil {
		return err
	}
	if archiveStarred && reason.String != "archive" {
		var starred int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE feed_id = ? AND starred = 1", id).Scan(&starred); err != nil {
			return err
		}
		if starred > 0 {
			arch, err := ensureArchiveFeed(ctx, tx)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE items SET feed_id = ?1, uid = 'a' || ?2 || ':' || uid,
				origin_title = COALESCE(origin_title, ?3) WHERE feed_id = ?2 AND starred = 1`, arch, id, title); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM feeds WHERE id = ?", id)
	return err
}

func ensureArchiveFeed(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM feeds WHERE disabled_reason = 'archive'").Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO feeds (folder_id, url, url_key, host, title, enabled, disabled_reason, retention)
		VALUES (1, 'kipple:archive', 'kipple:archive', 'kipple.invalid', 'Unsubscribed (starred)', 0, 'archive', 0)`)
	if err != nil {
		return 0, fmt.Errorf("store: create archive feed: %w", err)
	}
	return res.LastInsertId()
}

// RenameLabel renames folder oldID to newName; when a different folder already
// has that name the two are merged (feeds move, the old folder is deleted). The
// default folder may be renamed but never deleted.
func (d *DB) RenameLabel(ctx context.Context, oldID int64, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return nil
	}
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		target, found, err := FindLabel(ctx, tx, []string{newName})
		if err != nil {
			return err
		}
		if found && target != oldID {
			if _, err := tx.ExecContext(ctx, "UPDATE feeds SET folder_id = ? WHERE folder_id = ?", target, oldID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM folders WHERE id = ? AND is_default = 0", oldID)
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE folders SET name = ? WHERE id = ?", newName, oldID)
		return err
	})
}

// DisableLabel deletes a folder, moving its feeds to the default folder.
func (d *DB) DisableLabel(ctx context.Context, id int64) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE feeds SET folder_id = 1 WHERE folder_id = ?", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM folders WHERE id = ? AND is_default = 0", id)
		return err
	})
}

// UnreadRow is one feed's unread count for unread-count.
type UnreadRow struct {
	FeedID int64
	Folder string
	Count  int64
	MaxID  int64
}

// UnreadCounts returns per-feed unread counts (never counting ledger rows).
//
// holdCut > 0 leaves out items held back from the Reader API (HeldSQL), so the
// counts match what the listings return.
func (d *DB) UnreadCounts(ctx context.Context, holdCut int64) ([]UnreadRow, error) {
	held := ""
	var args []any
	if holdCut > 0 {
		held = " AND NOT " + HeldSQL
		args = append(args, sql.Named("hold_cut", holdCut))
	}
	rows, err := d.reader.QueryContext(ctx, `
		SELECT u.feed_id, fo.name, u.n, u.newest
		FROM (SELECT feed_id, count(*) AS n, max(id) AS newest FROM items WHERE read = 0`+held+` GROUP BY feed_id) u
		JOIN feeds f ON f.id = u.feed_id JOIN folders fo ON fo.id = f.folder_id
		ORDER BY fo.position, fo.name, f.position, u.feed_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnreadRow
	for rows.Next() {
		var r UnreadRow
		if err := rows.Scan(&r.FeedID, &r.Folder, &r.Count, &r.MaxID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindFeedID is FindFeedByURL on the reader pool; an unparseable URL is "not found".
func (d *DB) FindFeedID(ctx context.Context, u string) (int64, bool, error) {
	if _, err := feedurl.Key(u); err != nil {
		return 0, false, nil
	}
	return FindFeedByURL(ctx, d.reader, u)
}

// FeedIconAny returns a feed's icon whatever its hash (the web UI's /api/feeds/{id}/icon;
// the ?h= value is only a cache-buster).
func (d *DB) FeedIconAny(ctx context.Context, feedID int64) (data []byte, contentType string, ok bool, err error) {
	err = d.reader.QueryRowContext(ctx, "SELECT data, content_type FROM feed_icons WHERE feed_id = ?", feedID).
		Scan(&data, &contentType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	return data, contentType, err == nil, err
}

// StringSetting reads a string setting through the reader pool.
func (d *DB) StringSetting(ctx context.Context, key, def string) string {
	return settingString(ctx, d.reader, key, def)
}

// FeedImageFlags returns, per feed id, the image proxy flag bits taken from
// the feed (bit 0 allow_private_net, bit 1 allow_insecure_tls; design §7.4).
func (d *DB) FeedImageFlags(ctx context.Context, feedIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(feedIDs))
	if len(feedIDs) == 0 {
		return out, nil
	}
	js, err := json.Marshal(feedIDs)
	if err != nil {
		return nil, err
	}
	rows, err := d.reader.QueryContext(ctx,
		"SELECT id, allow_private_net | (allow_insecure_tls << 1) FROM feeds WHERE id IN (SELECT value FROM json_each(?))", string(js))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var f int
		if err := rows.Scan(&id, &f); err != nil {
			return nil, err
		}
		out[id] = f
	}
	return out, rows.Err()
}
