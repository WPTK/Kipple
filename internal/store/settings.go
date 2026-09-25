package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// FetchSettings are the settings the fetch layer reads. Defaults live here;
// a settings row exists only for an overridden key.
type FetchSettings struct {
	IntervalMinutes  int    // refresh.interval_minutes, default 30
	RetentionDefault int    // retention.default, default 250 (0 = unlimited)
	RestoreDays      int    // retention.restore_days, default 90
	UserAgent        string // fetch.user_agent, default "" (client default)
	HonorTTL         bool   // fetch.honor_publisher_ttl, default true
}

// LoadFetchSettings reads the fetch-related settings through q.
func LoadFetchSettings(ctx context.Context, q Querier) FetchSettings {
	return FetchSettings{
		IntervalMinutes:  settingInt(ctx, q, "refresh.interval_minutes", 30),
		RetentionDefault: settingInt(ctx, q, "retention.default", 250),
		RestoreDays:      settingInt(ctx, q, "retention.restore_days", 90),
		UserAgent:        settingString(ctx, q, "fetch.user_agent", ""),
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
