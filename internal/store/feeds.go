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

// NewFeed describes a subscription to insert. Only URL is required.
type NewFeed struct {
	URL             string
	FolderID        int64 // 0 = default folder
	IntervalMinutes int   // 0 = inherit
	Retention       *int  // nil = inherit
	DedupMode       string
	AllowPrivateNet bool
	NextFetchAt     int64 // 0 = now
}

// AddFeed inserts a feed due immediately (next_fetch_at = now unless set).
func (d *DB) AddFeed(ctx context.Context, f NewFeed) (int64, error) {
	key, norm, err := feedurl.KeyAndNormalize(f.URL)
	if err != nil {
		return 0, fmt.Errorf("store: add feed: %w", err)
	}
	host, _ := feedurl.Host(norm)
	if f.FolderID == 0 {
		f.FolderID = 1
	}
	if f.DedupMode == "" {
		f.DedupMode = fetch.DedupAuto
	}
	next := f.NextFetchAt
	if next == 0 {
		next = d.clock.Now().Unix()
	}
	var interval, retention any
	if f.IntervalMinutes > 0 {
		interval = f.IntervalMinutes
	}
	if f.Retention != nil {
		retention = *f.Retention
	}
	var id int64
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO feeds
			(folder_id, url, url_key, host, interval_minutes, retention, dedup_mode, allow_private_net, next_fetch_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			f.FolderID, norm, key, host, interval, retention, f.DedupMode, boolInt(f.AllowPrivateNet), next)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FindFeedByURL matches a URL against url_key and url_original_key (so http and
// https variants and pre-migration URLs collide). found is false when no feed
// matches.
func FindFeedByURL(ctx context.Context, q Querier, u string) (id int64, found bool, err error) {
	key, err := feedurl.Key(u)
	if err != nil {
		return 0, false, err
	}
	err = q.QueryRowContext(ctx,
		"SELECT id FROM feeds WHERE url_key = ?1 OR url_original_key = ?1 LIMIT 1", key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

const snapshotCols = `id, url, host, enabled, etag, last_modified, body_hash, user_agent, http_auth,
	ignore_http_cache, disable_http2, allow_insecure_tls, allow_private_net, dedup_mode, rekey_pending,
	interval_minutes, retention, fulltext, redirect_to, redirect_kind, redirect_count,
	consecutive_failures, initial_read_before, last_success_at, ua_fallback`

// FeedSnapshots runs a snapshot query (`where` is appended after FROM feeds,
// e.g. "WHERE id = ?") on the reader pool and resolves the settings-dependent
// fields: the effective interval, User-Agent and TTL flag.
func (d *DB) feedSnapshots(ctx context.Context, set FetchSettings, where string, args ...any) ([]fetch.Snapshot, error) {
	rows, err := d.reader.QueryContext(ctx, "SELECT "+snapshotCols+" FROM feeds "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fetch.Snapshot
	for rows.Next() {
		var s fetch.Snapshot
		var etag, lm, bh, ua, auth, rto, rkind sql.NullString
		var interval, retention, irb, lsa sql.NullInt64
		var enabled, ignore, h2, insecure, private, rekey, ft, uaFallback int
		if err := rows.Scan(&s.ID, &s.URL, &s.Host, &enabled, &etag, &lm, &bh, &ua, &auth,
			&ignore, &h2, &insecure, &private, &s.DedupMode, &rekey,
			&interval, &retention, &ft, &rto, &rkind, &s.Redirect.Count,
			&s.ConsecutiveFailures, &irb, &lsa, &uaFallback); err != nil {
			return nil, err
		}
		s.Enabled = enabled == 1
		s.ETag, s.LastModified, s.BodyHash, s.HTTPAuth = etag.String, lm.String, bh.String, auth.String
		s.IgnoreHTTPCache, s.DisableHTTP2, s.AllowInsecureTLS, s.AllowPrivateNet = ignore == 1, h2 == 1, insecure == 1, private == 1
		s.RekeyPending, s.Fulltext = rekey == 1, EffectiveFulltext(nil, ft == 1, set.FulltextAll) == 1
		s.Redirect.To, s.Redirect.Kind = rto.String, rkind.String
		s.IntervalMinutes = int(interval.Int64)
		s.Retention = -1
		if retention.Valid {
			s.Retention = int(retention.Int64)
		}
		s.InitialReadBefore, s.LastSuccessAt = irb.Int64, lsa.Int64
		iv := set.IntervalMinutes
		if s.IntervalMinutes > 0 {
			iv = s.IntervalMinutes
		}
		s.IntervalS = fetch.IntervalSeconds(iv)
		s.UAFallback = uaFallback == 1
		s.UserAgent, s.RetryUserAgent = ResolveUserAgent(set, strings.TrimSpace(ua.String), s.UAFallback)
		s.HonorTTL = set.HonorTTL
		out = append(out, s)
	}
	return out, rows.Err()
}

// DueFeeds returns enabled feeds with next_fetch_at <= now, oldest first.
func (d *DB) DueFeeds(ctx context.Context, set FetchSettings, now int64, limit int) ([]fetch.Snapshot, error) {
	return d.feedSnapshots(ctx, set, "WHERE enabled = 1 AND next_fetch_at <= ? ORDER BY next_fetch_at LIMIT ?", now, limit)
}

// EnabledFeeds returns every enabled feed (for refresh-all and retention runs).
func (d *DB) EnabledFeeds(ctx context.Context, set FetchSettings) ([]fetch.Snapshot, error) {
	return d.feedSnapshots(ctx, set, "WHERE enabled = 1 ORDER BY id")
}

// FeedSnapshot returns one feed regardless of enabled; ok is false when it does not exist.
func (d *DB) FeedSnapshot(ctx context.Context, set FetchSettings, id int64) (fetch.Snapshot, bool, error) {
	l, err := d.feedSnapshots(ctx, set, "WHERE id = ?", id)
	if err != nil || len(l) == 0 {
		return fetch.Snapshot{}, false, err
	}
	return l[0], true, nil
}

// FetchSettings loads the fetch settings on the reader pool.
func (d *DB) FetchSettings(ctx context.Context) FetchSettings {
	return LoadFetchSettings(ctx, d.reader)
}

// ResolveUserAgent picks the User-Agent for a fetch and the one to retry with
// on a 403/406 ("" = no retry). The per-feed override beats everything and never
// retries. Otherwise the mode decides: default = Kipple's UA (custom setting is
// only used in place of the browser string); browser_always = browser UA;
// browser_on_failure = Kipple's UA until a retry as a browser worked for this feed.
func ResolveUserAgent(set FetchSettings, feedUA string, uaFallback bool) (ua, retry string) {
	if feedUA != "" {
		return feedUA, ""
	}
	browser := set.UserAgent
	if browser == "" {
		browser = fetch.BrowserUserAgent
	}
	switch set.UAMode {
	case UAModeAlways:
		return browser, ""
	case UAModeDefault:
		return "", ""
	default: // browser_on_failure, and anything unrecognized
		if uaFallback {
			return browser, ""
		}
		return "", browser
	}
}

// SetFeedUAFallback remembers that a feed needs the browser User-Agent. It is
// bound to the URL the fetch used (or, when that very commit migrated the feed
// after a permanent redirect, the URL it moved to; "" otherwise): a URL edit
// between the commit and this write must not mark the new URL, which was never tried.
func (d *DB) SetFeedUAFallback(ctx context.Context, feedID int64, fetchedURL, migratedTo string) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE feeds SET ua_fallback = 1 WHERE id = ? AND (url = ? OR url = NULLIF(?, ''))", feedID, fetchedURL, migratedTo)
		return err
	})
}
