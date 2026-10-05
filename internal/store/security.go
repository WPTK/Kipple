package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
)

// Security settings (docs/setup-wizard-design.md 5.2, 5.4).
const (
	// SettingAllowedHosts is a JSON array of extra host names the Host gate
	// accepts: exact names or "*.suffix". The API validates the entries.
	SettingAllowedHosts = "security.allowed_hosts"

	// MaxAllowedHosts bounds security.allowed_hosts.
	MaxAllowedHosts = 64
)

// ErrAllowedHostsFull is allowHostTx's answer when security.allowed_hosts
// already holds MaxAllowedHosts entries and the name is not one of them.
var ErrAllowedHostsFull = errors.New("store: security.allowed_hosts is full")

// Security is the security settings as stored.
type Security struct {
	AllowedHosts []string
}

// SecuritySettings reads the security settings. A malformed stored value is the
// default; a failed read is an error.
func (d *DB) SecuritySettings(ctx context.Context) (Security, error) {
	hosts, err := allowedHosts(ctx, d.reader)
	return Security{AllowedHosts: hosts}, err
}

// allowedHosts reads security.allowed_hosts (nil when unset or malformed).
func allowedHosts(ctx context.Context, q Querier) ([]string, error) {
	raw, ok, err := settingRawErr(ctx, q, SettingAllowedHosts)
	if err != nil || !ok {
		return nil, err
	}
	var hosts []string
	if json.Unmarshal(raw, &hosts) != nil {
		return nil, nil
	}
	return hosts, nil
}

// allowHostTx adds host (a normalized entry, as setup.CheckHostEntry returns
// it) to security.allowed_hosts unless it is already there. A list already at
// MaxAllowedHosts is ErrAllowedHostsFull.
func allowHostTx(ctx context.Context, tx *sql.Tx, host string) error {
	hosts, err := allowedHosts(ctx, tx)
	if err != nil {
		return err
	}
	if slices.Contains(hosts, host) {
		return nil
	}
	if len(hosts) >= MaxAllowedHosts {
		return ErrAllowedHostsFull
	}
	return setSettingsTx(ctx, tx, map[string]any{SettingAllowedHosts: append(hosts, host)})
}
