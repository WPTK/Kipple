package store

import (
	"context"
	"fmt"
	"time"
)

// DefaultTZ is the `tz` setting's default for a new install (owner decision 5).
// Existing installs keep America/New_York through the row migration 0010 writes.
const DefaultTZ = "UTC"

// SettingTZ is the in-app time zone setting (an IANA name).
const SettingTZ = "tz"

// LoadZone resolves an IANA zone name for Kipple: never "" or "Local" (which
// would silently mean the process's own zone), at most 64 bytes, and known to
// the embedded tzdata.
func LoadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" || len(name) > 64 {
		return nil, fmt.Errorf("time zone %q: must be an IANA name such as Europe/Paris", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("time zone %q: %w", name, err)
	}
	return loc, nil
}

// ZoneName is the `tz` setting's name, else DefaultTZ. It is not resolved: the
// caller decides what an unknown stored name means.
func ZoneName(ctx context.Context, q Querier) string {
	return settingString(ctx, q, SettingTZ, DefaultTZ)
}

// Zone is the one time zone resolver: the `tz` setting (read per call, so a
// change applies to the next stats write, summary and maintenance tick without
// a restart), else UTC. A stored name that does not resolve is UTC.
func Zone(ctx context.Context, q Querier) *time.Location {
	loc, err := LoadZone(ZoneName(ctx, q))
	if err != nil {
		return time.UTC
	}
	return loc
}

// StoredZoneName is the `tz` setting as stored: ok is false when no row exists
// (the default applies).
func StoredZoneName(ctx context.Context, q Querier) (string, bool, error) {
	s, err := settingStringErr(ctx, q, SettingTZ, "")
	return s, err == nil && s != "", err
}

// SeedZone gives a new install the zone of its TZ environment variable: when no
// `tz` row exists yet it stores name (an IANA name) as the setting. From then on
// the setting is the only owner of the zone; an install that already has a row,
// from the wizard, Settings or migration 0010, keeps it and TZ is not read again.
// An empty name seeds nothing; a name that does not resolve is an error, and only
// when it would have been stored.
func (d *DB) SeedZone(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	if _, stored, err := StoredZoneName(ctx, d.reader); err != nil || stored {
		return err
	}
	if _, err := LoadZone(name); err != nil {
		return err
	}
	return d.SetSettings(ctx, map[string]any{SettingTZ: name})
}
