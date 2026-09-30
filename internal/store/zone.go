package store

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// DefaultTZ is the `tz` setting's default for a new install (owner decision 5).
// Existing installs keep America/New_York through the row migration 0010 writes.
const DefaultTZ = "UTC"

// SettingTZ is the in-app time zone setting (an IANA name).
const SettingTZ = "tz"

type envZone struct {
	name string
	loc  *time.Location
}

// envTZ is the process's TZ environment variable, when set (SetEnvZone). It is
// process-wide on purpose: TZ is one value for the whole process, loaded once
// at start before anything reads a zone.
var envTZ atomic.Pointer[envZone]

// SetEnvZone records the TZ environment variable (an IANA name) as the zone
// that overrides the `tz` setting everywhere (docs/setup-wizard-design.md 7a).
// An empty name clears it. A name that does not resolve is an error and changes
// nothing.
func SetEnvZone(name string) error {
	if name == "" {
		envTZ.Store(nil)
		return nil
	}
	loc, err := LoadZone(name)
	if err != nil {
		return err
	}
	envTZ.Store(&envZone{name: name, loc: loc})
	return nil
}

// EnvZone is the TZ environment variable's zone name and whether it is set.
func EnvZone() (string, bool) {
	if z := envTZ.Load(); z != nil {
		return z.name, true
	}
	return "", false
}

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

// ZoneName is the effective zone's name as configured: the TZ environment
// variable when set, else the `tz` setting, else DefaultTZ. It is not resolved:
// the caller decides what an unknown stored name means.
func ZoneName(ctx context.Context, q Querier) string {
	if name, ok := EnvZone(); ok {
		return name
	}
	return settingString(ctx, q, SettingTZ, DefaultTZ)
}

// Zone is the one time zone resolver (docs/setup-wizard-design.md 7a): the TZ
// environment variable when set, else the `tz` setting (read per call, so a
// change applies to the next stats write, summary and maintenance tick without
// a restart), else UTC. A stored name that does not resolve is UTC.
func Zone(ctx context.Context, q Querier) *time.Location {
	if z := envTZ.Load(); z != nil {
		return z.loc
	}
	loc, err := LoadZone(settingString(ctx, q, SettingTZ, DefaultTZ))
	if err != nil {
		return time.UTC
	}
	return loc
}

// StoredZoneName is the `tz` setting as stored, ignoring TZ: ok is false when
// no row exists (the default applies).
func StoredZoneName(ctx context.Context, q Querier) (string, bool, error) {
	s, err := settingStringErr(ctx, q, SettingTZ, "")
	return s, err == nil && s != "", err
}
