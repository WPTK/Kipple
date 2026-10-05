package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
)

// Reachability settings (docs/design.md §7.1f): how people reach this Kipple and
// who may. The API validates and normalizes every value before it is stored;
// package reach turns them into what the server uses.
const (
	// SettingPublicURL is the address Kipple is reached at, a JSON string ("" for none).
	SettingPublicURL = "server.public_url"
	// SettingAllowedHosts is a JSON array of extra host names the Host gate
	// accepts: exact names or "*.suffix".
	SettingAllowedHosts = "security.allowed_hosts"
	// SettingTrustedProxies is a JSON array of the addresses and ranges allowed to
	// say who the client is (X-Forwarded-For and friends).
	SettingTrustedProxies = "security.trusted_proxies"
	// SettingCloudflareAccess is a JSON object: {} when Cloudflare Access
	// validation is off, else {"team_domain": "...", "aud": "..."} (both set; one
	// key holds both, so a half-configured pair cannot be stored).
	SettingCloudflareAccess = "security.cloudflare_access"
)

// ReachKeys are the reachability settings, in a fixed order.
var ReachKeys = []string{SettingPublicURL, SettingAllowedHosts, SettingTrustedProxies, SettingCloudflareAccess}

// AccessConfig is the stored Cloudflare Access setting. Both fields empty means off.
type AccessConfig struct {
	TeamDomain string `json:"team_domain,omitempty"`
	AUD        string `json:"aud,omitempty"`
}

// Security is the reachability settings as stored. A missing or malformed row
// is the default (empty).
type Security struct {
	PublicURL      string
	AllowedHosts   []string
	TrustedProxies []string
	Access         AccessConfig
}

// SecuritySettings reads the reachability settings. A malformed stored value is
// the default; a failed read is an error.
func (d *DB) SecuritySettings(ctx context.Context) (Security, error) {
	var s Security
	var errs []error
	read := func(key string, into any) {
		raw, ok, err := settingRawErr(ctx, d.reader, key)
		if err != nil {
			errs = append(errs, err)
			return
		}
		if ok {
			_ = json.Unmarshal(raw, into) // malformed: the zero value stays
		}
	}
	read(SettingPublicURL, &s.PublicURL)
	read(SettingAllowedHosts, &s.AllowedHosts)
	read(SettingTrustedProxies, &s.TrustedProxies)
	read(SettingCloudflareAccess, &s.Access)
	if s.Access.TeamDomain == "" || s.Access.AUD == "" {
		s.Access = AccessConfig{} // a hand-edited half pair is off, never half on
	}
	return s, errors.Join(errs...)
}

// SeedSettings gives each key of seed its value when the key has no row yet, in
// one transaction: the rule for a setting an environment variable may also name
// (the variable seeds it; from then on the setting is the only owner). It
// returns the keys that already had a row holding a different value, so the
// caller can say that the variable was not used. A nil value seeds nothing.
func (d *DB) SeedSettings(ctx context.Context, seed map[string]any) (ignored []string, err error) {
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		ignored = ignored[:0]
		set := map[string]any{}
		for k, v := range seed {
			if v == nil {
				continue
			}
			want, err := json.Marshal(v)
			if err != nil {
				return err
			}
			var have string
			switch err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", k).Scan(&have); {
			case errors.Is(err, sql.ErrNoRows):
				set[k] = v
			case err != nil:
				return err
			case !sameJSON(have, want):
				ignored = append(ignored, k)
			}
		}
		return setSettingsTx(ctx, tx, set)
	})
	slices.Sort(ignored)
	return ignored, err
}

// StoredSettings reports which of keys have a settings row.
func (d *DB) StoredSettings(ctx context.Context, keys []string) (map[string]bool, error) {
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		_, ok, err := settingRawErr(ctx, d.reader, k)
		if err != nil {
			return nil, err
		}
		out[k] = ok
	}
	return out, nil
}

// sameJSON reports whether two JSON texts hold the same value.
func sameJSON(a string, b []byte) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}
