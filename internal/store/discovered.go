package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/fetch"
)

// CommitDiscovered applies a fetch that found a web page linking a feed (fetch.Result.Discovered,
// design §4.9 "Discovery on the first fetch"), in one gated transaction:
//
//   - nothing, when the feed's URL changed under the fetch (Stale), as CommitFetch;
//   - when another feed already has the discovered URL, this feed is a duplicate that holds no
//     items (it never fetched successfully): it is removed as an unsubscribe removes a feed, and
//     the kept feed takes its folder (unless it was in the default folder) and its custom title, as
//     a subscribe of an existing feed does (Subscribe). A kept feed that is disabled stays disabled,
//     as there too; that is logged (MergedInto);
//   - otherwise the link becomes the feed's URL (url_original keeps the page, so the same page
//     address finds this feed again), validators are dropped, and the feed is due at once: the
//     scheduler fetches it like any feed, under that host's own limits. The HTTP login is cleared
//     on a change of host and the network exceptions on a change of site, as PatchFeed does (the
//     fetch already refuses such a link; this commit does not rely on it). A permanent-redirect
//     migration (applyRedirect, design §4.7) differs: it keeps the login on another host of the
//     same site. The fetch_log row is outcome ok with the note
//     "discovered: <page> -> <feed>" (kept).
//
// Migrated and URL are set when the feed row changed.
func (d *DB) CommitDiscovered(ctx context.Context, res *fetch.Result) (CommitInfo, error) {
	var info CommitInfo
	now := d.clock.Now().Unix()
	err := d.gated(ctx, func(ctx context.Context, tx *sql.Tx) error {
		info = CommitInfo{}
		feedID := res.Snap.ID
		var curURL, curHost string
		var folder int64
		var custom sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT url, host, folder_id, custom_title FROM feeds WHERE id = ?", feedID).
			Scan(&curURL, &curHost, &folder, &custom); err != nil {
			return err
		}
		if curURL != res.Snap.URL {
			info.Stale = true
			return nil
		}
		other, found, err := FindFeedByURL(ctx, tx, res.Discovered)
		if err != nil {
			return err
		}
		if found && other != feedID {
			return d.mergeDuplicate(ctx, tx, res, res.Discovered, feedID, other, folder, custom, &info)
		}
		key, kerr := feedurl.Key(res.Discovered)
		host, herr := feedurl.Host(res.Discovered)
		if kerr != nil || herr != nil {
			return fmt.Errorf("store: discovered URL %q: %w", res.Discovered, errors.Join(kerr, herr))
		}
		hostChanged := !strings.EqualFold(curHost, host)
		siteChanged := !fetch.SameSite(curHost, host)
		if _, err := tx.ExecContext(ctx, `UPDATE feeds SET url_original = COALESCE(url_original, url),
			url_original_key = COALESCE(url_original_key, url_key), url = ?2, url_key = ?3, host = ?4,
			http_auth = CASE WHEN ?5 THEN NULL ELSE http_auth END,
			allow_private_net = CASE WHEN ?6 THEN 0 ELSE allow_private_net END,
			allow_insecure_tls = CASE WHEN ?6 THEN 0 ELSE allow_insecure_tls END,
			etag = NULL, last_modified = NULL, body_hash = NULL, ttl_hint_s = NULL,
			redirect_to = NULL, redirect_kind = NULL, redirect_count = 0,
			last_fetch_at = ?7, last_status = ?8, next_fetch_at = ?7, current_delay_s = 0, updated_at = ?7
			WHERE id = ?1`, feedID, res.Discovered, key, host, hostChanged, siteChanged, now, nullInt(res.Status)); err != nil {
			return err
		}
		note := fmt.Sprintf("discovered: %s -> %s", res.Snap.URL, res.Discovered)
		if _, err := tx.ExecContext(ctx, `INSERT INTO fetch_log
			(feed_id, trigger, started_at, duration_ms, outcome, http_status, new_items, updated_items, trimmed_items,
			 bytes, final_url, note, keep)
			VALUES (?,?,?,?,?,?,0,0,0,?,?,?,1)`,
			feedID, res.Snap.Trigger, res.StartedAt.Unix(), res.Duration.Milliseconds(), fetch.OutcomeOK, nullInt(res.Status),
			res.Bytes, nullStr(res.FinalURL), note); err != nil {
			return err
		}
		info.Migrated, info.URL = true, res.Discovered
		return capFetchLog(ctx, tx, feedID, now)
	})
	return info, err
}

// CommitRedirectDuplicate applies the one fetch outcome that would otherwise leave a duplicate: a
// feed that has never fetched successfully answered through a redirect to the address of a feed the
// user already has (an address typed, imported or subscribed from a client that is a moved or
// alternate address of that feed). It is removed and the kept feed takes its folder and custom title,
// exactly as for a discovered duplicate (mergeDuplicate). handled is false when this is not that case
// (no redirect, the redirect target is free, the feed already fetched before, or its URL changed
// under the fetch), and the caller commits the fetch normally. Add feed in the web app refuses such an
// address before it creates the feed; this is the same rule for the paths that cannot fetch first.
func (d *DB) CommitRedirectDuplicate(ctx context.Context, res *fetch.Result) (info CommitInfo, handled bool, err error) {
	to := res.Redirect.To
	if to == "" || (res.Redirect.Action != fetch.RedirectSet && res.Redirect.Action != fetch.RedirectMigrate) {
		return info, false, nil
	}
	err = d.gated(ctx, func(ctx context.Context, tx *sql.Tx) error {
		info, handled = CommitInfo{}, false
		feedID := res.Snap.ID
		var curURL string
		var folder int64
		var custom sql.NullString
		var fetched bool
		if err := tx.QueryRowContext(ctx, "SELECT url, folder_id, custom_title, last_success_at IS NOT NULL FROM feeds WHERE id = ?", feedID).
			Scan(&curURL, &folder, &custom, &fetched); err != nil {
			return err
		}
		if curURL != res.Snap.URL || fetched {
			return nil
		}
		other, found, err := FindFeedByURL(ctx, tx, to)
		if err != nil || !found || other == feedID {
			return err
		}
		handled = true
		return d.mergeDuplicate(ctx, tx, res, to, feedID, other, folder, custom, &info)
	})
	return info, handled && err == nil, err
}

// mergeDuplicate removes feedID, a duplicate of other (the feed that owns target), carrying its folder
// and custom title over.
func (d *DB) mergeDuplicate(ctx context.Context, tx *sql.Tx, res *fetch.Result, target string, feedID, other, folder int64,
	custom sql.NullString, info *CommitInfo) error {
	var isDefault bool
	if err := tx.QueryRowContext(ctx, "SELECT is_default FROM folders WHERE id = ?", folder).Scan(&isDefault); err != nil {
		return err
	}
	if !isDefault {
		if _, err := tx.ExecContext(ctx, "UPDATE feeds SET folder_id = ?, updated_at = unixepoch() WHERE id = ? AND folder_id != ?", folder, other, folder); err != nil {
			return err
		}
	}
	if custom.Valid {
		if err := applyFeedEdit(ctx, tx, other, "", false, custom.String); err != nil {
			return err
		}
	}
	var enabled bool
	if err := tx.QueryRowContext(ctx, "SELECT enabled FROM feeds WHERE id = ?", other).Scan(&enabled); err != nil {
		return err
	}
	if err := removeFeed(ctx, tx, feedID, true); err != nil {
		return err
	}
	d.bumpFilters() // its filters cascade away
	d.log.Info("store: an address led to a feed that is already subscribed; the duplicate was removed",
		"feed", feedID, "address", res.Snap.URL, "feed_url", target, "kept", other, "kept_enabled", enabled)
	info.MergedInto, info.Migrated = other, true
	return nil
}
