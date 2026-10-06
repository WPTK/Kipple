package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
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

// MaxAllowedHosts bounds security.allowed_hosts.
const MaxAllowedHosts = 64

// settingAllowedHostsMerged records that MergeAllowedHostsOnce ran (its value is
// the time). Contract: it can go, with MergeAllowedHostsOnce, once an upgrade
// from 0.8.0-beta.2 or older is no longer supported.
const settingAllowedHostsMerged = "sys.allowed_hosts_env_merged"

// MergeAllowedHostsOnce is the one-time step from the rule of 0.8.0-beta.2 and
// older (the Host gate answered KIPPLE_ALLOWED_HOSTS and security.allowed_hosts
// together) to the seed rule (the variable only gives the setting its first
// value). On the first start that runs it, the names it lacks are added to a
// stored security.allowed_hosts, so an upgrade loses no allowed name; then the
// marker is stored, and from then on only the seed rule applies, so a name
// removed in Settings stays removed. With no stored row it only stores the
// marker (the seed stores the variable as usual). names are normalized entries
// (setup.CheckHostEntry).
//
// A stored row that is not a list of names (only a hand edit makes one) already
// reads as empty everywhere, so it is replaced by names: that is the union with
// what was in force. Names past MaxAllowedHosts are not added and are logged at
// ERROR, by name, so they can be added in Settings after removing others; the
// marker is stored anyway, since another start cannot make them fit either.
func (d *DB) MergeAllowedHostsOnce(ctx context.Context, names []string) error {
	var dropped []string
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		dropped = nil
		if _, done, err := settingRawErr(ctx, tx, settingAllowedHostsMerged); err != nil || done {
			return err
		}
		set := map[string]any{settingAllowedHostsMerged: d.clock.Now().Unix()}
		raw, ok, err := settingRawErr(ctx, tx, SettingAllowedHosts)
		if err != nil {
			return err
		}
		if ok && len(names) > 0 {
			var have []string
			if json.Unmarshal(raw, &have) != nil {
				have = nil
			}
			union := slices.Clone(have)
			for _, n := range names {
				switch {
				case slices.Contains(union, n):
				case len(union) >= MaxAllowedHosts:
					dropped = append(dropped, n)
				default:
					union = append(union, n)
				}
			}
			if have == nil || len(union) > len(have) {
				set[SettingAllowedHosts] = union
			}
		}
		return setSettingsTx(ctx, tx, set)
	})
	if err == nil && len(dropped) > 0 {
		d.log.Error("store: KIPPLE_ALLOWED_HOSTS names not added to the allowed host names: the list holds at most 64; remove some in Settings, Account & Devices, Address and access, then add these",
			"names", dropped)
	}
	return err
}

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

// sameJSON reports whether two JSON texts hold the same value. The seeded lists
// (allowed host names, trusted proxies) are sets: a top-level array matches
// another with the same members in any order, so a list that Settings or the
// one-time merge ordered differently is not reported as a different value.
func sameJSON(a string, b []byte) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return canonical(x) == canonical(y)
}

// canonical is v as compact JSON, a top-level array as its sorted distinct members.
func canonical(v any) string {
	arr, ok := v.([]any)
	if !ok {
		b, _ := json.Marshal(v)
		return string(b)
	}
	members := make([]string, 0, len(arr))
	for _, e := range arr {
		b, _ := json.Marshal(e)
		members = append(members, string(b))
	}
	slices.Sort(members)
	return "[" + strings.Join(slices.Compact(members), ",") + "]"
}
