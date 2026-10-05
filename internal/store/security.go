package store

import (
	"context"
	"encoding/json"
)

// Security settings (docs/design.md §7.1e).
const (
	// SettingAllowedHosts is a JSON array of extra host names the Host gate
	// accepts: exact names or "*.suffix". The API validates the entries.
	SettingAllowedHosts = "security.allowed_hosts"
)

// Security is the security settings as stored.
type Security struct {
	AllowedHosts []string
}

// SecuritySettings reads the security settings. A malformed stored value is the
// default; a failed read is an error.
func (d *DB) SecuritySettings(ctx context.Context) (Security, error) {
	var s Security
	raw, ok, err := settingRawErr(ctx, d.reader, SettingAllowedHosts)
	if err != nil {
		return s, err
	}
	if ok {
		var hosts []string
		if json.Unmarshal(raw, &hosts) == nil {
			s.AllowedHosts = hosts
		}
	}
	return s, nil
}
