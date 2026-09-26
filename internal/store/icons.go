package store

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
)

// IconJob is one feed due for a favicon lookup (design §4.11), with the
// transport flags and User-Agent its own fetch would use.
type IconJob struct {
	FeedID   int64
	FeedURL  string // feeds.url
	FeedHost string // feeds.host: the only host the network exceptions below cover
	SiteURL  string // feeds.site_url, "" when the feed has none

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

// IconSiteKey is the identity of the site a lookup starts from: the host (and a
// non-default port) of site_url when it is an absolute http(s) URL, else of the
// feed URL. The scheme, path, query and fragment are not part of it, so a link
// that changes on every fetch (a session id, a tracking parameter, http/https
// flapping) is still the same site. "" when neither URL has an http(s) host.
func IconSiteKey(siteURL, feedURL string) string {
	if k := hostKey(strings.TrimSpace(siteURL)); k != "" {
		return k
	}
	return hostKey(feedURL)
}

// hostKey is the lowercase hostname of an absolute http(s) URL, with its port
// unless that is 80 or 443.
func hostKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	h := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if h == "" {
		return ""
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return net.JoinHostPort(h, p)
	}
	return h
}

// feedHostOf is the lowercase hostname of a feed URL ("" when it has none).
func feedHostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// sameIconSite is true when a check row made for (checkSite, checkFeed) still
// describes a feed now at (site, feed): the same IconSiteKey and the same feed
// host (the host the network exceptions are scoped to).
func sameIconSite(checkSite, checkFeed, site, feed string) bool {
	return IconSiteKey(checkSite, checkFeed) == IconSiteKey(site, feed) && feedHostOf(checkFeed) == feedHostOf(feed)
}

// NextIconJob returns the enabled, once-fetched feed whose icon lookup is most
// overdue and that skip (optional) does not pass over: never looked up first,
// then any whose site or feed host changed since the last lookup, then by
// next_check_at. ok is false when nothing is due at now.
func (d *DB) NextIconJob(ctx context.Context, set FetchSettings, now int64, skip func(IconJob) bool) (job IconJob, ok bool, err error) {
	// The SQL keeps only rows that may be due (a raw URL change is a superset of
	// a site change); sameIconSite decides.
	rows, err := d.reader.QueryContext(ctx, `
		SELECT f.id, f.url, f.host, f.site_url, f.allow_private_net, f.allow_insecure_tls, f.disable_http2,
		       f.user_agent, f.ua_fallback,
		       c.feed_id IS NOT NULL, COALESCE(c.site_url, ''), COALESCE(c.feed_url, ''),
		       COALESCE(c.next_check_at, 0), COALESCE(c.failures, 0)
		FROM feeds f LEFT JOIN feed_icon_checks c ON c.feed_id = f.id
		WHERE f.enabled = 1 AND f.last_success_at IS NOT NULL
		  AND (c.feed_id IS NULL OR c.next_check_at <= ? OR c.site_url != f.site_url OR c.feed_url != f.url)`, now)
	if err != nil {
		return IconJob{}, false, err
	}
	defer rows.Close()
	type cand struct {
		job     IconJob
		checked bool
		rank    int64 // 0 for a changed site, else next_check_at
		ua      sql.NullString
		uaFall  bool
	}
	var cands []cand
	for rows.Next() {
		var c cand
		var private, insecure, h2, uaFallback, checked int
		var cSite, cFeed string
		var next int64
		if err := rows.Scan(&c.job.FeedID, &c.job.FeedURL, &c.job.FeedHost, &c.job.SiteURL, &private, &insecure, &h2,
			&c.ua, &uaFallback, &checked, &cSite, &cFeed, &next, &c.job.Failures); err != nil {
			return IconJob{}, false, err
		}
		c.checked = checked == 1
		c.job.AllowPrivateNet, c.job.AllowInsecureTLS, c.job.DisableHTTP2 = private == 1, insecure == 1, h2 == 1
		c.uaFall = uaFallback == 1
		if c.checked {
			if sameIconSite(cSite, cFeed, c.job.SiteURL, c.job.FeedURL) {
				if next > now {
					continue // only the raw URL changed (same site): not due
				}
				c.rank = next
			} else {
				c.job.Failures = 0 // a new site starts over
			}
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return IconJob{}, false, err
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.checked != b.checked {
			return !a.checked
		}
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		return a.job.FeedID < b.job.FeedID
	})
	for _, c := range cands {
		c.job.UserAgent, c.job.RetryUserAgent = ResolveUserAgent(set, strings.TrimSpace(c.ua.String), c.uaFall)
		if skip != nil && skip(c.job) {
			continue
		}
		return c.job, true, nil
	}
	return IconJob{}, false, nil
}

// SaveIconCheck records one lookup for job: the icon when res is non-nil
// (replacing a stored one only when its bytes changed; fetched_at and
// source_url are refreshed either way), and the check row with its next due
// time and failure count. A failed lookup keeps the icon already stored.
// Nothing is written when the feed is gone or its site or feed host changed
// since the job was read (the changed feed is simply due again); a change of
// scheme, path or query alone is the same site. The feed's own row, and so its
// health, is never touched.
func (d *DB) SaveIconCheck(ctx context.Context, job IconJob, res *IconResult, errMsg string, failures int, now, next int64) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var curURL, curSite string
		err := tx.QueryRowContext(ctx, "SELECT url, site_url FROM feeds WHERE id = ?", job.FeedID).Scan(&curURL, &curSite)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameIconSite(job.SiteURL, job.FeedURL, curSite, curURL) {
			return nil
		}
		if res != nil {
			// The blob is rewritten only when the icon changed: a weekly recheck
			// that finds the same bytes updates fetched_at and source_url alone.
			if _, err := tx.ExecContext(ctx, `INSERT INTO feed_icons (feed_id, data, content_type, source_url, hash, fetched_at)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (feed_id) DO UPDATE SET data = excluded.data, content_type = excluded.content_type,
				  source_url = excluded.source_url, hash = excluded.hash, fetched_at = excluded.fetched_at
				WHERE feed_icons.hash IS NOT excluded.hash`,
				job.FeedID, res.Data, res.ContentType, res.SourceURL, res.Hash, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE feed_icons SET fetched_at = ?, source_url = ?
				WHERE feed_id = ? AND hash = ? AND (fetched_at != ? OR source_url != ?)`,
				now, res.SourceURL, job.FeedID, res.Hash, now, res.SourceURL); err != nil {
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
