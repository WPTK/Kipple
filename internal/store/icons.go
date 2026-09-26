package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// IconJob is one feed due for a favicon lookup (design §4.11), with the
// transport flags and User-Agent its own fetch would use.
type IconJob struct {
	FeedID  int64
	FeedURL string // feeds.url
	SiteURL string // feeds.site_url, "" when the feed has none

	AllowPrivateNet  bool
	AllowInsecureTLS bool
	DisableHTTP2     bool
	UserAgent        string // resolved like the feed fetch ("" = client default)
	RetryUserAgent   string

	// Failures is the count of consecutive failed lookups for this same site
	// (0 when the last lookup succeeded, there was none, or the site changed).
	Failures int
}

// IconResult is what a lookup found. Nil means it failed.
type IconResult struct {
	Data        []byte
	ContentType string
	SourceURL   string
	Hash        string
}

// iconSame is true when the check row still describes the feed's current site.
const iconSame = `c.site_url = f.site_url AND (f.site_url != '' OR c.feed_url = f.url)`

// NextIconJob returns the enabled, once-fetched feed whose icon lookup is most
// overdue: never looked up first, then by next_check_at, then any whose site
// changed since the last lookup. ok is false when nothing is due at now.
func (d *DB) NextIconJob(ctx context.Context, set FetchSettings, now int64) (job IconJob, ok bool, err error) {
	var ua sql.NullString
	var private, insecure, h2, uaFallback int
	err = d.reader.QueryRowContext(ctx, `
		SELECT f.id, f.url, f.site_url, f.allow_private_net, f.allow_insecure_tls, f.disable_http2,
		       f.user_agent, f.ua_fallback,
		       CASE WHEN c.feed_id IS NOT NULL AND `+iconSame+` THEN c.failures ELSE 0 END
		FROM feeds f LEFT JOIN feed_icon_checks c ON c.feed_id = f.id
		WHERE f.enabled = 1 AND f.last_success_at IS NOT NULL
		  AND (c.feed_id IS NULL OR c.next_check_at <= ? OR NOT (`+iconSame+`))
		ORDER BY c.feed_id IS NOT NULL, CASE WHEN `+iconSame+` THEN c.next_check_at ELSE 0 END, f.id
		LIMIT 1`, now).
		Scan(&job.FeedID, &job.FeedURL, &job.SiteURL, &private, &insecure, &h2, &ua, &uaFallback, &job.Failures)
	if errors.Is(err, sql.ErrNoRows) {
		return IconJob{}, false, nil
	}
	if err != nil {
		return IconJob{}, false, err
	}
	job.AllowPrivateNet, job.AllowInsecureTLS, job.DisableHTTP2 = private == 1, insecure == 1, h2 == 1
	job.UserAgent, job.RetryUserAgent = ResolveUserAgent(set, strings.TrimSpace(ua.String), uaFallback == 1)
	return job, true, nil
}

// SaveIconCheck records one lookup for job: the icon when res is non-nil
// (replacing any stored one), and the check row with its next due time and
// failure count. A failed lookup keeps the icon already stored. Nothing is
// written when the feed is gone or its url or site_url changed since the job
// was read (the changed feed is simply due again). The feed's own row, and so
// its health, is never touched.
func (d *DB) SaveIconCheck(ctx context.Context, job IconJob, res *IconResult, errMsg string, failures int, now, next int64) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM feeds WHERE id = ? AND url = ? AND site_url = ?",
			job.FeedID, job.FeedURL, job.SiteURL).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if res != nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO feed_icons (feed_id, data, content_type, source_url, hash, fetched_at)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (feed_id) DO UPDATE SET data = excluded.data, content_type = excluded.content_type,
				  source_url = excluded.source_url, hash = excluded.hash, fetched_at = excluded.fetched_at`,
				job.FeedID, res.Data, res.ContentType, res.SourceURL, res.Hash, now); err != nil {
				return err
			}
		}
		var lastErr any
		if errMsg != "" {
			lastErr = errMsg
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO feed_icon_checks (feed_id, site_url, feed_url, checked_at, next_check_at, failures, last_error)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (feed_id) DO UPDATE SET site_url = excluded.site_url, feed_url = excluded.feed_url,
			  checked_at = excluded.checked_at, next_check_at = excluded.next_check_at,
			  failures = excluded.failures, last_error = excluded.last_error`,
			job.FeedID, job.SiteURL, job.FeedURL, now, next, failures, lastErr)
		return err
	})
}
