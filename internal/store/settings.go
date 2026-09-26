package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	FulltextAll      bool   // fetch.fulltext_all, default false
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
		FulltextAll:      settingBool(ctx, q, SettingFulltextAll, false),
	}
}

// settingRawErr reads one settings row. A missing row is (nil, false, nil); any
// other failure (a cancelled context, a busy database) is a non-nil error, so a
// caller that caches the result can tell "not set" from "could not read".
func settingRawErr(ctx context.Context, q Querier, key string) (json.RawMessage, bool, error) {
	var v string
	if err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return json.RawMessage(v), true, nil
}

func settingRaw(ctx context.Context, q Querier, key string) (json.RawMessage, bool) {
	raw, ok, err := settingRawErr(ctx, q, key)
	return raw, ok && err == nil
}

// settingBoolErr is settingBool that reports a read failure instead of
// returning the default for it.
func settingBoolErr(ctx context.Context, q Querier, key string, def bool) (bool, error) {
	raw, ok, err := settingRawErr(ctx, q, key)
	if err != nil {
		return def, err
	}
	if !ok {
		return def, nil
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return def, nil
	}
	return b, nil
}

// settingStringErr is the string counterpart of settingBoolErr.
func settingStringErr(ctx context.Context, q Querier, key, def string) (string, error) {
	raw, ok, err := settingRawErr(ctx, q, key)
	if err != nil {
		return def, err
	}
	if !ok {
		return def, nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return def, nil
	}
	return s, nil
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
	if _, ok := set[SettingFulltextAll]; ok {
		defer d.ftAll.invalidate() // after the commit, whatever its outcome
	}
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
// enabled, healthy feed that inherits the interval and is due later than its new
// due time becomes due then. The new due time is last_fetch_at + interval, or,
// when fetch.honor_publisher_ttl is on, no earlier than last_fetch_at plus the
// feed's publisher TTL hint (capped at a day, as scheduling caps it). It never
// postpones a feed (a raised interval applies from each feed's next fetch).
// Returns the feeds moved.
func (d *DB) PullInSchedule(ctx context.Context, intervalMinutes int) (int64, error) {
	honor := 0
	if d.FetchSettings(ctx).HonorTTL {
		honor = 1
	}
	var n int64
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE feeds SET next_fetch_at = last_fetch_at + max(?1, CASE WHEN ?2 = 1 THEN min(coalesce(ttl_hint_s, 0), 86400) ELSE 0 END)
			WHERE enabled = 1 AND interval_minutes IS NULL AND consecutive_failures = 0
			  AND last_fetch_at IS NOT NULL
			  AND next_fetch_at > last_fetch_at + max(?1, CASE WHEN ?2 = 1 THEN min(coalesce(ttl_hint_s, 0), 86400) ELSE 0 END)`,
			intervalMinutes*60, honor)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}
