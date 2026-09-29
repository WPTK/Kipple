package store

import (
	"context"
	"encoding/json"
	"errors"
)

// Security settings (docs/setup-wizard-design.md 5.2, 5.4).
const (
	// SettingAllowedHosts is a JSON array of extra host names the Host gate
	// accepts: exact names or "*.suffix". The API validates the entries.
	SettingAllowedHosts = "security.allowed_hosts"
	// SettingOpenLAN lets open mode accept devices on the local network (RFC 1918
	// and ULA peers) as well as loopback and Tailscale. Default off.
	SettingOpenLAN = "security.open_lan"
)

// Security is the security settings as stored.
type Security struct {
	AllowedHosts []string
	OpenLAN      bool
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
	var err2 error
	s.OpenLAN, err2 = settingBoolErr(ctx, d.reader, SettingOpenLAN, false)
	return s, errors.Join(err, err2)
}
