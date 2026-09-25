package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// MaxRestoreDays caps retention.restore_days. The nightly ledger purge removes a
// trimmed id (and its stub) 180 days after its uid was last seen, so a longer
// stub window could never be honored.
const MaxRestoreDays = 180

// Values of fetch.user_agent_mode.
const (
	UAModeDefault   = "default"            // always Kipple's own User-Agent, never retry
	UAModeOnFailure = "browser_on_failure" // Kipple's UA; retry once as a browser on 403/406, then remember per feed
	UAModeAlways    = "browser_always"     // browser User-Agent for every fetch
)

// FetchSettings are the settings the fetch layer reads. Defaults live here;
// a settings row exists only for an overridden key.
type FetchSettings struct {
	IntervalMinutes  int    // refresh.interval_minutes, default 30
	RetentionDefault int    // retention.default, default 250 (0 = unlimited)
	RestoreDays      int    // retention.restore_days, default 90, clamped to 0..MaxRestoreDays
	UserAgent        string // fetch.user_agent, default "": optional custom UA that replaces the built-in browser string
	UAMode           string // fetch.user_agent_mode, default UAModeOnFailure
	HonorTTL         bool   // fetch.honor_publisher_ttl, default true
}

// LoadFetchSettings reads the fetch-related settings through q.
func LoadFetchSettings(ctx context.Context, q Querier) FetchSettings {
	return FetchSettings{
		IntervalMinutes:  settingInt(ctx, q, "refresh.interval_minutes", 30),
		RetentionDefault: settingInt(ctx, q, "retention.default", 250),
		RestoreDays:      min(max(settingInt(ctx, q, "retention.restore_days", 90), 0), MaxRestoreDays),
		UserAgent:        settingString(ctx, q, "fetch.user_agent", ""),
		UAMode:           settingString(ctx, q, "fetch.user_agent_mode", UAModeOnFailure),
		HonorTTL:         settingBool(ctx, q, "fetch.honor_publisher_ttl", true),
	}
}

func settingRaw(ctx context.Context, q Querier, key string) (json.RawMessage, bool) {
	var v string
	if err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v); err != nil {
		if err != sql.ErrNoRows {
			return nil, false
		}
		return nil, false
	}
	return json.RawMessage(v), true
}

func settingInt(ctx context.Context, q Querier, key string, def int) int {
	raw, ok := settingRaw(ctx, q, key)
	if !ok {
		return def
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return def
	}
	return int(n)
}

func settingBool(ctx context.Context, q Querier, key string, def bool) bool {
	raw, ok := settingRaw(ctx, q, key)
	if !ok {
		return def
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return def
	}
	return b
}

func settingString(ctx context.Context, q Querier, key, def string) string {
	raw, ok := settingRaw(ctx, q, key)
	if !ok {
		return def
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return def
	}
	return s
}

// SetSettings writes the given overrides in one transaction: a nil value
// deletes the row (back to the default). Validation is the caller's job.
func (d *DB) SetSettings(ctx context.Context, set map[string]any) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for k, v := range set {
			if v == nil {
				if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", k); err != nil {
					return err
				}
				continue
			}
			b, err := json.Marshal(v)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = unixepoch()`, k, string(b)); err != nil {
				return err
			}
		}
		return nil
	})
}

// PullInSchedule makes a lowered refresh.interval_minutes take effect now: every
// enabled, healthy feed that inherits the interval and is due later than
// last_fetch_at + interval becomes due then. It never postpones a feed (a raised
// interval applies from each feed's next fetch). Returns the feeds moved.
func (d *DB) PullInSchedule(ctx context.Context, intervalMinutes int) (int64, error) {
	var n int64
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE feeds SET next_fetch_at = last_fetch_at + ?
			WHERE enabled = 1 AND interval_minutes IS NULL AND consecutive_failures = 0
			  AND last_fetch_at IS NOT NULL AND next_fetch_at > last_fetch_at + ?`,
			intervalMinutes*60, intervalMinutes*60)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}
