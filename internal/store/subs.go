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
	"github.com/WPTK/kipple/internal/filter"
)

// Subscription is one row of the Reader API subscription list.
type Subscription struct {
	ID       int64
	Title    string // the display name, feedTitleSQL
	URL      string
	SiteURL  string
	Folder   string
	IconHash string // "" when the feed has no icon
}

// Subscriptions lists feeds for subscription/list (design §6.9): disabled and
// gone feeds included, never the archive feed (listedFeedSQL).
func (d *DB) Subscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := d.reader.QueryContext(ctx, `
		SELECT f.id, `+feedTitleSQL("f")+`, f.url, f.site_url, COALESCE(fp.path, ''), COALESCE(fi.hash, '')
		FROM feeds f LEFT JOIN folder_paths fp ON fp.id = f.folder_id
		LEFT JOIN feed_icons fi ON fi.feed_id = f.id
		WHERE `+listedFeedSQL+`
		ORDER BY fp.sort_key, f.position, `+feedTitleSQL("f"))
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

// FolderNames lists the Reader API labels in display order (tag/list): every folder's full path,
// except a pure container (a folder with subfolders and no listed feed of its own), which a client
// would show as an empty folder of its own beside the "Parent/Child" labels. An empty folder without
// subfolders stays listed: a client creates a folder and then files a feed into it.
func (d *DB) FolderNames(ctx context.Context) ([]string, error) {
	rows, err := d.reader.QueryContext(ctx, `SELECT fp.path FROM folder_paths fp
		WHERE NOT EXISTS (SELECT 1 FROM folders c WHERE c.parent_id = fp.id)
		   OR EXISTS (SELECT 1 FROM feeds f WHERE f.folder_id = fp.id AND `+listedFeedSQL+`)
		ORDER BY fp.sort_key`)
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
	// FolderRefused is why the folder writer refused the label (FolderRefused): the feed was not filed
	// under it (a new feed went to the default folder, an existing one stayed). Nil otherwise.
	FolderRefused error
}

// Subscribe is the API subscribe path (design §6.9, decision 33): idempotent on
// FindFeedByURL, no outbound HTTP and no discovery. A new feed is inserted with
// next_fetch_at = now and no title of its own (feedTitleSQL then names it by its
// URL); the scheduler picks it up, and the first successful fetch stores the
// title the document gives itself (CommitFetch). A given title is the custom
// title. An existing feed is moved or renamed only if a folder or title was given.
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
			if o.Folder != "" {
				fid, refused, err := subscribeFolder(ctx, tx, o.Folder)
				if err != nil {
					return err
				}
				res.FolderRefused = refused
				if refused == nil {
					if _, err := tx.ExecContext(ctx, "UPDATE feeds SET folder_id = ?, updated_at = unixepoch() WHERE id = ? AND folder_id != ?", fid, id, fid); err != nil {
						return err
					}
				}
			}
			if err := applyFeedEdit(ctx, tx, id, "", false, o.Title); err != nil {
				return err
			}
		} else {
			folder, refused, err := subscribeFolder(ctx, tx, o.Folder)
			if err != nil {
				return err
			}
			res.FolderRefused = refused
			if refused != nil {
				folder = 1
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
			r, err := tx.ExecContext(ctx, `INSERT INTO feeds (folder_id, url, url_key, host, custom_title, position, next_fetch_at)
				VALUES (?,?,?,?,?,?,?)`, folder, norm, key, host, custom, pos, now)
			if err != nil {
				return err
			}
			if id, err = r.LastInsertId(); err != nil {
				return err
			}
		}
		res.FeedID = id
		return tx.QueryRowContext(ctx, "SELECT "+feedTitleSQL("feeds")+" FROM feeds WHERE id = ?", id).Scan(&res.Title)
	})
	return res, err
}

// FeedName is a feed's display name (feedTitleSQL), as every list shows it.
func (d *DB) FeedName(ctx context.Context, id int64) (string, error) {
	var name string
	err := d.reader.QueryRowContext(ctx, "SELECT "+feedTitleSQL("feeds")+" FROM feeds WHERE id = ?", id).Scan(&name)
	return name, err
}

// subscribeFolder is resolveFolderPath for a subscribe. A label the folder writer refuses (an empty
// level such as "News/", more than 8 levels, below the default folder) never costs the subscription: it
// is reported in refused, so a new feed goes to the default folder and an existing one stays where it
// is; the caller logs refused. The savepoint undoes any folder the refused walk created before it stopped.
func subscribeFolder(ctx context.Context, tx *sql.Tx, label string) (id int64, refused, err error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT subscribe_folder"); err != nil {
		return 0, nil, err
	}
	id, err = resolveFolderPath(ctx, tx, label)
	if FolderRefused(err) {
		refused, id, err = err, 0, nil
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO subscribe_folder"); err != nil {
			return 0, nil, err
		}
	} else if err != nil {
		return 0, nil, err
	}
	if _, err := tx.ExecContext(ctx, "RELEASE subscribe_folder"); err != nil {
		return 0, nil, err
	}
	return id, refused, nil
}

// ValidateFeedURL is the one URL check for every way a feed URL enters the
// database (Reader subscribe, web add, web URL edit): an absolute http(s) URL
// with a host, normalized, and not a literal blocked address (loopback,
// private, link-local, ...) unless the feed allows private networks. Hostnames
// that resolve to blocked addresses are stopped at dial time by the fetch
// guard. It returns the normalized URL, its key and its host.
func ValidateFeedURL(raw string, allowPrivate bool) (norm, key, host string, err error) {
	key, norm, nerr := feedurl.KeyAndNormalize(raw)
	if errors.Is(nerr, feedurl.ErrUserinfo) {
		return "", "", "", &InvalidURLError{"the URL contains a user name or password; use the feed's HTTP authentication instead"}
	}
	if nerr != nil {
		return "", "", "", &InvalidURLError{"not an absolute http(s) URL"}
	}
	host, _ = feedurl.Host(norm)
	if ip, perr := netip.ParseAddr(host); perr == nil && !allowPrivate && fetch.Blocked(ip.Unmap()) {
		return "", "", "", &InvalidURLError{"address not allowed"}
	}
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
		fid, err := resolveFolderPath(ctx, tx, folder)
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
// moves to Uncategorized. Titles, when set, holds one title per ref (a batch
// renaming several feeds at once) and replaces Title.
type EditOpts struct {
	Title         string
	Titles        []string
	Folder        string
	SetFolder     bool
	MoveToDefault bool
}

// ErrEditTitles is returned when EditOpts.Titles does not hold exactly one title per ref.
var ErrEditTitles = errors.New("store: subscription edit needs one title per feed")

// EditSubscription applies EditOpts to each referenced feed in one transaction
// (all or nothing); unknown feeds are ignored. It returns the ids that exist.
func (d *DB) EditSubscription(ctx context.Context, refs []FeedRef, o EditOpts) (feedIDs []int64, err error) {
	if o.Titles != nil && len(o.Titles) != len(refs) {
		return nil, ErrEditTitles
	}
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		feedIDs = feedIDs[:0]
		for i, ref := range refs {
			id, err := resolveFeed(ctx, tx, ref)
			if err != nil {
				return err
			}
			if id == 0 {
				continue
			}
			feedIDs = append(feedIDs, id)
			title := o.Title
			if o.Titles != nil {
				title = o.Titles[i]
			}
			switch {
			case o.SetFolder:
				err = applyFeedEdit(ctx, tx, id, o.Folder, true, title)
			case o.MoveToDefault:
				err = applyFeedEdit(ctx, tx, id, "", true, title)
			default:
				err = applyFeedEdit(ctx, tx, id, "", false, title)
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
// ledger, stubs, log and icon). Unknown feeds are ignored; the ids that were
// removed are returned. The archive feed is processed last and only deleted
// when it holds no starred items; otherwise it is skipped (see
// UnsubscribeSkipped), so starred items are never lost.
func (d *DB) Unsubscribe(ctx context.Context, refs []FeedRef) (feedIDs []int64, err error) {
	feedIDs, _, err = d.UnsubscribeSkipped(ctx, refs)
	return feedIDs, err
}

// UnsubscribeSkipped is Unsubscribe that also returns the ids it refused to
// delete (the archive feed while it still holds starred items).
func (d *DB) UnsubscribeSkipped(ctx context.Context, refs []FeedRef) (feedIDs, skipped []int64, err error) {
	// Mark every feed for deletion in one short transaction first (never fetched
	// again, see deletingURLPrefix; refs are resolved there, before a URL ref's
	// feed has its URL moved), then empty each in bounded batches
	// (purgeFeedItems leaves starred items and the archive feed alone), so the
	// transaction below only moves starred items and removes feed rows. An
	// interruption leaves marked feeds that the next unsubscribe (or
	// ResumeFeedDeletes) finishes.
	var ids []int64
	now := d.clock.Now().Unix()
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		ids = ids[:0]
		for _, ref := range refs {
			id, err := resolveFeed(ctx, tx, ref)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return markFeedsDeleting(ctx, tx, ids, now)
	})
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		if id != 0 {
			if err := d.purgeFeedItems(ctx, id, true); err != nil {
				return nil, nil, err
			}
		}
	}
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		feedIDs, skipped = nil, nil
		var archiveID int64
		seen := map[int64]bool{}
		for _, id := range ids {
			if id == 0 || seen[id] {
				continue
			}
			seen[id] = true
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM feeds WHERE id = ?", id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				continue // removed meanwhile
			}
			var reason sql.NullString
			if err := tx.QueryRowContext(ctx, "SELECT disabled_reason FROM feeds WHERE id = ?", id).Scan(&reason); err != nil {
				return err
			}
			if reason.String == "archive" {
				archiveID = id
				continue
			}
			feedIDs = append(feedIDs, id)
			if err := removeFeed(ctx, tx, id, true); err != nil {
				return err
			}
			d.bumpFilters() // its filters cascade away
		}
		if archiveID != 0 {
			// Last, after the others have moved their starred items into it.
			switch err := removeFeed(ctx, tx, archiveID, true); {
			case errors.Is(err, ErrArchiveHasStarred):
				skipped = append(skipped, archiveID)
			case err != nil:
				return err
			default:
				feedIDs = append(feedIDs, archiveID)
			}
		}
		return nil
	})
	return feedIDs, skipped, err
}

// ErrArchiveHasStarred is returned when the archive feed would be deleted
// while it still holds starred items and delete_starred was not requested.
var ErrArchiveHasStarred = errors.New("store: the archive feed holds starred items")

// removeFeed deletes a feed. With archiveStarred its starred items are first
// re-parented to the archive feed (design decision 24). The archive feed itself
// is only deleted when it holds no starred items or archiveStarred is false
// (explicit delete_starred); otherwise ErrArchiveHasStarred, nothing changed.
func removeFeed(ctx context.Context, tx *sql.Tx, id int64, archiveStarred bool) error {
	var reason sql.NullString
	var title string
	if err := tx.QueryRowContext(ctx, "SELECT disabled_reason, "+feedTitleSQL("feeds")+" FROM feeds WHERE id = ?", id).Scan(&reason, &title); err != nil {
		return err
	}
	if archiveStarred && reason.String == "archive" {
		var starred int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE feed_id = ? AND starred = 1", id).Scan(&starred); err != nil {
			return err
		}
		if starred > 0 {
			return ErrArchiveHasStarred
		}
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
	if _, err := tx.ExecContext(ctx, "DELETE FROM feeds WHERE id = ?", id); err != nil {
		return err
	}
	return dropFavorite(ctx, tx, FavFeed, id)
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

// RenameLabel is the Reader API's rename-tag: folder oldID gets the label (full path) newName, which
// may rename it, move it, or both; its subfolders follow. When a different folder already has that
// path the two are merged (feeds move, the old folder is deleted), unless the old folder has
// subfolders (ErrMergeSubfolders). Otherwise the last segment of newName becomes the folder's name and
// the rest names its new parent: the longest prefix that is an existing folder's path, then folders
// created below it for the remaining segments (as subscribe does). The default folder may be renamed
// but never deleted or moved. A refused change (FolderRefused) changes nothing. filtersChanged reports
// whether a merge converted or dropped the old folder's filters, for the filters.changed event.
func (d *DB) RenameLabel(ctx context.Context, oldID int64, newName string) (filtersChanged bool, err error) {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return false, nil
	}
	var convFilters, convFeeds int
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		convFilters, convFeeds, filtersChanged = 0, 0, false
		cur, err := loadFolder(ctx, tx, oldID)
		if err != nil {
			return err
		}
		target, found, err := folderByPath(ctx, tx, newName)
		if err != nil {
			return err
		}
		if found && target == oldID {
			// The same path in another spelling of ASCII case (same length in bytes): the folder's own
			// name takes the new spelling; the folders above it are not this rename's to change.
			return placeAndSave(ctx, tx, oldID, cur.parent, newName[len(newName)-len(cur.name):])
		}
		if !found {
			parent, rest, err := splitPath(ctx, tx, newName)
			if err != nil {
				return err
			}
			for _, seg := range rest {
				if strings.TrimSpace(seg) == "" {
					return ErrEmptyFolderSegment
				}
			}
			if len(rest) > 1 {
				if parent, _, err = walkFolders(ctx, tx, parent, rest[:len(rest)-1]); err != nil {
					return err
				}
			}
			if err := placeAndSave(ctx, tx, oldID, parent, rest[len(rest)-1]); err != nil {
				return err
			}
			if parent != cur.parent {
				d.bumpFilters() // folder filters cover subfolders: the feeds a rule matches changed
			}
			return nil
		}
		// A merge.
		var kids int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM folders WHERE parent_id = ?", oldID).Scan(&kids); err != nil {
			return err
		}
		if kids > 0 {
			return ErrMergeSubfolders
		}
		// A rename that merges folders keeps the old folder's filters at the
		// scope they had: each becomes one feed filter per feed that was in the
		// folder. Re-pointing them at the target folder would make them match
		// the target's own feeds as well.
		if convFilters, convFeeds, err = d.scopeFolderFiltersToFeeds(ctx, tx, oldID); err != nil {
			return err
		}
		var left int // folder filters the folder's deletion cascades away (an empty folder's)
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM filters WHERE folder_id = ?", oldID).Scan(&left); err != nil {
			return err
		}
		// The archive feed stays where it is (the default folder, which is never deleted).
		if _, err := tx.ExecContext(ctx, "UPDATE feeds SET folder_id = ? WHERE folder_id = ? AND disabled_reason IS NOT 'archive'", target, oldID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM folders WHERE id = ? AND is_default = 0", oldID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		filtersChanged = convFilters > 0 || (n > 0 && left > 0)
		if filtersChanged {
			d.bumpFilters()
		}
		if n > 0 {
			return dropFavorite(ctx, tx, FavFolder, oldID)
		}
		return nil
	})
	if err == nil && convFilters > 0 {
		d.log.Info("store: folder merge turned folder filters into feed filters", "folder", oldID, "filters", convFilters, "feeds", convFeeds)
	}
	return filtersChanged, err
}

// ErrMergeTooManyFilters refuses a folder merge whose feed-filter copies of the
// merged-away folder's filters (see scopeFolderFiltersToFeeds) would take the
// filter set past filter.MaxRules. Nothing is changed.
var ErrMergeTooManyFilters = errors.New("store: merging the folders would create more filters than the limit allows")

// scopeFolderFiltersToFeeds turns every folder filter of folderID into feed
// filters for the feeds now in the folder (the archive feed and a feed being
// deleted aside): the rule itself becomes the filter of the first feed, keeping
// its id, match count, muted articles and any reason Kipple switched it off
// for, and a copy (same rule, name, action, position and reason; no matches
// yet) is added for each other feed, with those feeds' muted marks moved to it.
// For the default folder, which is never deleted, the folder filter stays for
// the feeds that land there later and every feed gets a copy. A non-default
// folder with no feeds keeps its filters here; they go with the folder. Used by
// a folder merge, so the merged-away folder's rules keep matching exactly the
// feeds they matched before. A merge whose copies would not all run as their
// rule runs now (past filter.MaxRules or another set-wide limit, see
// checkMergeCopies) is ErrMergeTooManyFilters before anything changes. It
// returns the number of folder filters converted (0 when nothing changed) and
// the number of feeds they now cover.
func (d *DB) scopeFolderFiltersToFeeds(ctx context.Context, tx *sql.Tx, folderID int64) (filters, feeds int, err error) {
	filterIDs, err := queryIDs(ctx, tx, "SELECT id FROM filters WHERE scope = 'folder' AND folder_id = ? ORDER BY id", folderID)
	if err != nil || len(filterIDs) == 0 {
		return 0, 0, err
	}
	feedIDs, err := queryIDs(ctx, tx, "SELECT f.id FROM feeds f WHERE f.folder_id = ? AND f.disabled_reason IS NOT 'archive' AND "+
		notDeletingSQL+" ORDER BY f.id", folderID)
	if err != nil || len(feedIDs) == 0 {
		return 0, 0, err
	}
	var isDefault bool
	if err := tx.QueryRowContext(ctx, "SELECT is_default FROM folders WHERE id = ?", folderID).Scan(&isDefault); err != nil {
		return 0, 0, err
	}
	if err := checkMergeCopies(ctx, tx, filterIDs, feedIDs, isDefault); err != nil {
		return 0, 0, err
	}
	reasons, err := loadFilterReasons(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	now := d.clock.Now().Unix()
	// (original filter, feed) -> copy, for moving the muted marks in one pass below.
	var pairs []any
	for _, fid := range filterIDs {
		copyTo := feedIDs
		if !isDefault {
			// The rule itself stays with the first feed: its muted marks there need no move.
			if _, err := tx.ExecContext(ctx, "UPDATE filters SET scope = 'feed', folder_id = NULL, feed_id = ?, updated_at = ? WHERE id = ?",
				feedIDs[0], now, fid); err != nil {
				return 0, 0, err
			}
			copyTo = feedIDs[1:]
		}
		for _, feed := range copyTo {
			res, err := tx.ExecContext(ctx, `INSERT INTO filters (name, enabled, scope, folder_id, feed_id, kind, terms, fields,
				case_sensitive, whole_word, fold_diacritics, invert, action, position, created_at, updated_at)
				SELECT name, enabled, 'feed', NULL, ?, kind, terms, fields,
				case_sensitive, whole_word, fold_diacritics, invert, action, position, created_at, ?
				FROM filters WHERE id = ?`, feed, now, fid)
			if err != nil {
				return 0, 0, err
			}
			cp, err := res.LastInsertId()
			if err != nil {
				return 0, 0, err
			}
			pairs = append(pairs, fid, feed, cp)
			if why, ok := reasons[fid]; ok {
				reasons[cp] = why
			}
		}
	}
	if len(pairs) > 0 {
		values := strings.TrimSuffix(strings.Repeat("(?,?,?),", len(pairs)/3), ",")
		if _, err := tx.ExecContext(ctx, `WITH m(o, f, c) AS (VALUES `+values+`)
			UPDATE items SET muted_by = (SELECT c FROM m WHERE o = items.muted_by AND f = items.feed_id)
			WHERE muted_by IN (SELECT o FROM m) AND feed_id IN (SELECT f FROM m)`, pairs...); err != nil {
			return 0, 0, err
		}
		if err := saveFilterReasons(ctx, tx, reasons, now); err != nil {
			return 0, 0, err
		}
	}
	return len(filterIDs), len(feedIDs), nil
}

// checkMergeCopies is scopeFolderFiltersToFeeds' dry run: the filter set as it
// would be after the merge must stay within filter.MaxRules, and no feed filter
// made from a rule that runs now may be one the set-wide limits (regex rules,
// text terms, regex cost) would switch off. Otherwise ErrMergeTooManyFilters.
func checkMergeCopies(ctx context.Context, q Querier, filterIDs, feedIDs []int64, isDefault bool) error {
	cur, err := loadFilters(ctx, q)
	if err != nil {
		return err
	}
	type origRule struct {
		f    Filter
		runs bool
	}
	isOrig := make(map[int64]bool, len(filterIDs))
	for _, id := range filterIDs {
		isOrig[id] = true
	}
	orig := make(map[int64]origRule, len(filterIDs)) // filterIDs and cur come from the same transaction
	flagged := filter.Sanitize(filterRules(cur))
	var rules []filter.Rule
	var mustRun []int // indexes in rules of feed filters made from a rule that runs today
	for i, f := range cur {
		if !isOrig[f.ID] {
			rules = append(rules, f.Rule())
			continue
		}
		_, bad := flagged[i]
		o := origRule{f: f, runs: f.Enabled && !bad}
		orig[f.ID] = o
		if isDefault {
			rules = append(rules, f.Rule()) // the folder filter stays
			continue
		}
		r := f.Rule() // converted in place: same id
		r.Scope, r.FolderID, r.FeedID = filter.ScopeFeed, 0, feedIDs[0]
		if o.runs {
			mustRun = append(mustRun, len(rules))
		}
		rules = append(rules, r)
	}
	// The copies, numbered in the order scopeFolderFiltersToFeeds inserts them (filterIDs ascending, then
	// feeds): Sanitize applies the set-wide limits in id order, so the dry run cuts where the real set would.
	copyTo := feedIDs
	if !isDefault {
		copyTo = feedIDs[1:]
	}
	next := unsavedFilterID
	for _, id := range filterIDs {
		o := orig[id]
		for _, feed := range copyTo {
			r := o.f.Rule()
			r.ID, r.Scope, r.FolderID, r.FeedID = next, filter.ScopeFeed, 0, feed
			next++
			if o.runs {
				mustRun = append(mustRun, len(rules))
			}
			rules = append(rules, r)
		}
	}
	if len(rules) > filter.MaxRules {
		return fmt.Errorf("%w (%d filters after the merge, limit %d)", ErrMergeTooManyFilters, len(rules), filter.MaxRules)
	}
	after := filter.Sanitize(rules)
	for _, i := range mustRun {
		if why, off := after[i]; off {
			return fmt.Errorf("%w (a feed filter would be switched off: %s)", ErrMergeTooManyFilters, why)
		}
	}
	return nil
}

// UnreadRow is one feed's unread count for unread-count.
type UnreadRow struct {
	FeedID int64
	Folder string
	Count  int64
	MaxID  int64
	// Archive marks the archive feed's row: its unread items count toward the
	// reading-list total (they are in that stream) but it is not a listed feed
	// (listedFeedSQL), so it gets no feed or folder row of its own.
	Archive bool
}

// UnreadCounts returns per-feed unread counts (never counting ledger rows).
//
// holdCut > 0 leaves out items held back from the Reader API (HeldSQL), so the
// counts match what the listings return.
func (d *DB) UnreadCounts(ctx context.Context, holdCut int64) ([]UnreadRow, error) {
	held := ""
	var args []any
	if holdCut > 0 {
		held = " AND NOT " + HeldSQL(d.FulltextAll(ctx))
		args = append(args, d.holdArgs(holdCut)...)
	}
	rows, err := d.reader.QueryContext(ctx, `
		SELECT u.feed_id, COALESCE(fp.path, ''), u.n, u.newest, f.disabled_reason IS 'archive'
		FROM (SELECT feed_id, count(*) AS n, max(id) AS newest FROM items WHERE read = 0`+held+` GROUP BY feed_id) u
		JOIN feeds f ON f.id = u.feed_id LEFT JOIN folder_paths fp ON fp.id = f.folder_id
		WHERE `+notDeletingSQL+`
		ORDER BY fp.sort_key, f.position, u.feed_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnreadRow
	for rows.Next() {
		var r UnreadRow
		if err := rows.Scan(&r.FeedID, &r.Folder, &r.Count, &r.MaxID, &r.Archive); err != nil {
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

// StringSettingErr is StringSetting that reports a read failure (as opposed to
// an unset key, which is the default with a nil error), for callers that cache.
func (d *DB) StringSettingErr(ctx context.Context, key, def string) (string, error) {
	return settingStringErr(ctx, d.reader, key, def)
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
