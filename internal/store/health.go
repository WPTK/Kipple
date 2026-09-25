package store

import (
	"context"
	"database/sql"
	"os"
)

// FeedHealth is one feed's fetch health (GET /api/health/feeds, design §7.1).
// Times are unix seconds; nil means never / not set.
type FeedHealth struct {
	ID                  int64   `json:"id,string"`
	Title               string  `json:"title"`
	URL                 string  `json:"url"`
	URLOriginal         *string `json:"url_original"`
	Enabled             bool    `json:"enabled"`
	DisabledReason      *string `json:"disabled_reason"`
	LastSuccessAt       *int64  `json:"last_success_at"`
	LastFetchAt         *int64  `json:"last_fetch_at"`
	LastErrorAt         *int64  `json:"last_error_at"`
	LastErrorClass      *string `json:"last_error_class"`
	LastError           *string `json:"last_error"`
	LastStatus          *int64  `json:"last_status"`
	ConsecutiveFailures int64   `json:"consecutive_failures"`
	CurrentDelayS       int64   `json:"current_delay_s"`
	NextFetchAt         int64   `json:"next_fetch_at"`
	RedirectTo          *string `json:"redirect_to"`
	RedirectKind        *string `json:"redirect_kind"`
	RedirectCount       int64   `json:"redirect_count"`
	LastNewItemsAt      *int64  `json:"last_new_items_at"`
	TrimmedUnreadCount  int64   `json:"trimmed_unread_count"`
	TrimmedUnreadSince  *int64  `json:"trimmed_unread_since"`

	// Host and CreatedAt feed FeedStatus and the host throttle lookup; not serialized.
	Host      string `json:"-"`
	CreatedAt int64  `json:"-"`
}

// FeedHealth lists every feed except the archive feed, by title.
func (d *DB) FeedHealth(ctx context.Context) ([]FeedHealth, error) {
	rows, err := d.reader.QueryContext(ctx, `
		SELECT id, COALESCE(NULLIF(custom_title,''), NULLIF(title,''), url), url, url_original, enabled, disabled_reason,
		       last_success_at, last_fetch_at, last_error_at, last_error_class, last_error, last_status,
		       consecutive_failures, current_delay_s, next_fetch_at, redirect_to, redirect_kind, redirect_count,
		       last_new_items_at, trimmed_unread_count, trimmed_unread_since, host, created_at
		FROM feeds WHERE disabled_reason IS NOT 'archive'
		ORDER BY lower(COALESCE(NULLIF(custom_title,''), NULLIF(title,''), url)), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeedHealth{}
	for rows.Next() {
		var h FeedHealth
		var enabled int64
		var origURL, reason, class, lastErr, rTo, rKind sql.NullString
		var succ, fetch, errAt, status, newAt, trimSince sql.NullInt64
		if err := rows.Scan(&h.ID, &h.Title, &h.URL, &origURL, &enabled, &reason,
			&succ, &fetch, &errAt, &class, &lastErr, &status,
			&h.ConsecutiveFailures, &h.CurrentDelayS, &h.NextFetchAt, &rTo, &rKind, &h.RedirectCount,
			&newAt, &h.TrimmedUnreadCount, &trimSince, &h.Host, &h.CreatedAt); err != nil {
			return nil, err
		}
		h.Enabled = enabled == 1
		h.URLOriginal, h.DisabledReason = strp(origURL), strp(reason)
		h.LastSuccessAt, h.LastFetchAt, h.LastErrorAt = intp(succ), intp(fetch), intp(errAt)
		h.LastErrorClass, h.LastError, h.LastStatus = strp(class), strp(lastErr), intp(status)
		h.RedirectTo, h.RedirectKind = strp(rTo), strp(rKind)
		h.LastNewItemsAt, h.TrimmedUnreadSince = intp(newAt), intp(trimSince)
		out = append(out, h)
	}
	return out, rows.Err()
}

// UnreadTotal counts unread items (ledger rows are not in items).
func (d *DB) UnreadTotal(ctx context.Context) (int64, error) {
	var n int64
	err := d.reader.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE read = 0").Scan(&n)
	return n, err
}

func strp(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

func intp(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

// DiskUsage is the on-disk footprint shown in the health view (audit D7).
type DiskUsage struct {
	DBBytes     int64 `json:"db_bytes"`
	WALBytes    int64 `json:"wal_bytes"`
	BackupBytes int64 `json:"backup_bytes"`
	// ImgcacheBytes is 0 until the image cache exists (backend plan step 13).
	ImgcacheBytes int64 `json:"imgcache_bytes"`
}

// DiskUsage sizes the database file, its WAL and the backup directory
// (snapshots and pre-migration copies). Missing files count as 0.
func (d *DB) DiskUsage() DiskUsage {
	var u DiskUsage
	if fi, err := os.Stat(d.path); err == nil {
		u.DBBytes = fi.Size()
	}
	if fi, err := os.Stat(d.path + "-wal"); err == nil {
		u.WALBytes = fi.Size()
	}
	if ents, err := os.ReadDir(d.backupDir); err == nil {
		for _, e := range ents {
			if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
				u.BackupBytes += info.Size()
			}
		}
	}
	return u
}
