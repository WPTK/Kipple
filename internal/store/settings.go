package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
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

// LoadFetchSettingsErr reads the fetch-related settings through q. A missing row
// or an unparseable value is the default; a failed read (cancelled context, busy
// or broken database) is a non-nil error, with the settings that could not be
// read left at their defaults. Callers inside a write transaction must fail it
// rather than act on those defaults (a trim or purge with the wrong cap or
// window is not undone by a retry that never happens).
func LoadFetchSettingsErr(ctx context.Context, q Querier) (FetchSettings, error) {
	var errs []error
	keep := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	var s FetchSettings
	var err error
	s.IntervalMinutes, err = settingIntErr(ctx, q, "refresh.interval_minutes", 30)
	keep(err)
	s.RetentionDefault, err = settingIntErr(ctx, q, "retention.default", 250)
	keep(err)
	s.RestoreDays, err = settingIntErr(ctx, q, "retention.restore_days", 90)
	keep(err)
	s.RestoreDays = min(max(s.RestoreDays, 0), MaxRestoreDays)
	s.UserAgent, err = settingStringErr(ctx, q, "fetch.user_agent", "")
	keep(err)
	s.UAMode, err = settingStringErr(ctx, q, "fetch.user_agent_mode", UAModeOnFailure)
	keep(err)
	s.HonorTTL, err = settingBoolErr(ctx, q, "fetch.honor_publisher_ttl", true)
	keep(err)
	s.FulltextAll, err = settingBoolErr(ctx, q, SettingFulltextAll, false)
	keep(err)
	return s, errors.Join(errs...)
}

// LoadFetchSettings is LoadFetchSettingsErr for callers outside a write
// transaction that can live with defaults for one pass: a read failure is logged
// at warn and the defaults are used. The result must not be cached.
func LoadFetchSettings(ctx context.Context, q Querier) FetchSettings {
	s, err := LoadFetchSettingsErr(ctx, q)
	if err != nil {
		slog.Warn("store: reading fetch settings failed; using defaults for this pass", "err", err)
	}
	return s
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

// settingRaw is settingRawErr for callers that fall back to a default: a read
// failure is logged at warn (never silently taken for "not set") and reported as
// not found.
func settingRaw(ctx context.Context, q Querier, key string) (json.RawMessage, bool) {
	raw, ok, err := settingRawErr(ctx, q, key)
	if err != nil {
		slog.Warn("store: reading setting failed; using its default", "key", key, "err", err)
		return nil, false
	}
	return raw, ok
}

// settingIntErr is the integer counterpart of settingBoolErr.
func settingIntErr(ctx context.Context, q Querier, key string, def int) (int, error) {
	raw, ok, err := settingRawErr(ctx, q, key)
	if err != nil {
		return def, err
	}
	if !ok {
		return def, nil
	}
	n, ok := jsonInt(raw)
	if !ok {
		return def, nil
	}
	return n, nil
}

// jsonInt decodes a JSON number that is an exact integer within int's range.
// Anything else (a fraction, 1e300, a string) is not an int: the caller uses
// the default rather than a truncated or overflowed conversion.
func jsonInt(raw json.RawMessage) (int, bool) {
	var n float64
	if json.Unmarshal(raw, &n) != nil || n != math.Trunc(n) || n < math.MinInt || n >= -float64(math.MinInt) {
		return 0, false
	}
	return int(n), true
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
	n, ok := jsonInt(raw)
	if !ok {
		return def
	}
	return n
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
	return d.setSettings(ctx, set, "")
}

// FixedTheme reports whether a ui.theme value is a fixed scheme rather than
// "system" (follow the device, or the schedule when ui.theme_schedule is on).
func FixedTheme(theme string) bool { return theme != "" && theme != "system" }

// ScheduleOffForFixedTheme is the rule "picking a fixed theme turns the theme
// schedule off", shared by every write that can put a theme in force (PATCH
// /api/device, and through SetSettingsFixedTheme PATCH /api/settings and
// make-default, which on its own also never copies a device's fixed theme
// together with a flag that is on): with the schedule flag on in force and a
// write set that does not name ui.theme_schedule, a resulting fixed theme adds
// ui.theme_schedule=false. theme is the ui.theme the write leaves in force; the
// caller resolves a cleared (null) value.
func ScheduleOffForFixedTheme(set map[string]any, theme string, on bool) {
	if _, named := set["ui.theme_schedule"]; named || !on || !FixedTheme(theme) {
		return
	}
	set["ui.theme_schedule"] = false
}

// SetSettingsFixedTheme is SetSettings for an account write that leaves a fixed
// ui.theme in force. Inside the write transaction, against the stored account
// flag (so a concurrent write cannot slip between the read and the write), it
// applies ScheduleOffForFixedTheme to set, and when the write then leaves the
// flag off while it is stored on, every device that shows the account's
// schedule through its own "system" theme first gets the flag on its own
// profile (pinInheritedThemeScheduleTx): the account's theme becoming a fixed
// one does not end that device's schedule. theme is the ui.theme the write
// leaves in force (the caller resolves a cleared value); set may be modified.
func (d *DB) SetSettingsFixedTheme(ctx context.Context, set map[string]any, theme string) error {
	return d.setSettings(ctx, set, theme)
}

// setSettings writes set; theme is the ui.theme the write leaves in force when
// that matters (SetSettingsFixedTheme), else "". Only a fixed theme runs the
// schedule rule.
func (d *DB) setSettings(ctx context.Context, set map[string]any, theme string) error {
	if _, ok := set[SettingFulltextAll]; ok {
		defer d.ftAll.invalidate() // after the commit, whatever its outcome
	}
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if FixedTheme(theme) {
			on, err := settingBoolErr(ctx, tx, "ui.theme_schedule", false)
			if err != nil {
				return err
			}
			ScheduleOffForFixedTheme(set, theme, on)
			if v := set["ui.theme_schedule"]; on && v != true {
				if err := d.pinInheritedThemeScheduleTx(ctx, tx); err != nil {
					return err
				}
			}
		}
		if v, ok := set["stats.enabled"]; ok && (v == nil || v == true) {
			// Turning recording back on ends a stretch with no rows: days up to today are a gap, not quiet.
			if was, err := StatsEnabled(ctx, tx); err != nil {
				return err
			} else if !was {
				if err := RecordStatsGap(ctx, tx, "", d.Clock().Now()); err != nil {
					return err
				}
			}
		}
		return setSettingsTx(ctx, tx, set)
	})
}

// setSettingsTx is SetSettings inside the caller's write transaction.
func setSettingsTx(ctx context.Context, tx *sql.Tx, set map[string]any) error {
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
		if k == SettingSavedSearches {
			// A replaced list: its new or changed scopes must exist (SavedSearchError).
			var raw string
			if err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", k).Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := checkNewSavedSearchScopes(ctx, tx, decodeSavedSearches(raw), decodeSavedSearches(string(b))); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = unixepoch()`, k, string(b)); err != nil {
			return err
		}
	}
	return nil
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
	if ttl, err := settingBoolErr(ctx, d.reader, "fetch.honor_publisher_ttl", true); err != nil {
		return 0, err
	} else if ttl {
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
