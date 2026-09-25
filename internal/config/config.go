// Package config reads Kipple's KIPPLE_* environment variables (plus the
// standard TZ) into a Config, applying defaults. Nothing else in the
// codebase calls os.Getenv: everything that needs configuration takes it
// from a Config passed down from cmd/kipple.
package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting Kipple reads at startup. See .env.example for
// a full description of each field.
type Config struct {
	Addr            string        // KIPPLE_ADDR, default ":7080"
	DataDir         string        // KIPPLE_DATA, default "/data"
	Username        string        // KIPPLE_USERNAME
	Password        string        // KIPPLE_PASSWORD, initial web password
	APIPassword     string        // KIPPLE_API_PASSWORD, optional initial
	PublicURL       string        // KIPPLE_PUBLIC_URL
	TrustedProxyIPs []netip.Addr  // KIPPLE_TRUSTED_PROXY_IPS, comma-separated
	TZ              string        // TZ, default "America/New_York"
	SchedTick       time.Duration // KIPPLE_SCHED_TICK, default 30s
	FetchWorkers    int           // KIPPLE_FETCH_WORKERS, default 8
	FetchPerHost    int           // KIPPLE_FETCH_PER_HOST, default 2
	LogLevel        slog.Level    // KIPPLE_LOG_LEVEL, default info
	LogGreaderForms bool          // KIPPLE_LOG_GREADER_FORMS, default false
	CFAccessTeam    string        // KIPPLE_CF_ACCESS_TEAM, optional
	CFAccessAUD     string        // KIPPLE_CF_ACCESS_AUD, optional
}

const (
	defaultAddr         = ":7080"
	defaultDataDir      = "/data"
	defaultTZ           = "America/New_York"
	defaultSchedTick    = 30 * time.Second
	defaultFetchWorkers = 8
	defaultFetchPerHost = 2
	defaultLogLevel     = slog.LevelInfo
)

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return load(os.Getenv)
}

// load is Load with an injectable lookup, so tests never touch the real
// environment.
func load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:         orDefault(getenv("KIPPLE_ADDR"), defaultAddr),
		DataDir:      orDefault(getenv("KIPPLE_DATA"), defaultDataDir),
		Username:     getenv("KIPPLE_USERNAME"),
		Password:     getenv("KIPPLE_PASSWORD"),
		APIPassword:  getenv("KIPPLE_API_PASSWORD"),
		PublicURL:    getenv("KIPPLE_PUBLIC_URL"),
		TZ:           orDefault(getenv("TZ"), defaultTZ),
		CFAccessTeam: getenv("KIPPLE_CF_ACCESS_TEAM"),
		CFAccessAUD:  getenv("KIPPLE_CF_ACCESS_AUD"),
	}

	var err error
	if cfg.TrustedProxyIPs, err = parseIPList(getenv("KIPPLE_TRUSTED_PROXY_IPS")); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_TRUSTED_PROXY_IPS: %w", err)
	}
	if cfg.SchedTick, err = parseDuration(getenv("KIPPLE_SCHED_TICK"), defaultSchedTick); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_SCHED_TICK: %w", err)
	}
	if cfg.FetchWorkers, err = parsePositiveInt(getenv("KIPPLE_FETCH_WORKERS"), defaultFetchWorkers); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_FETCH_WORKERS: %w", err)
	}
	if cfg.FetchPerHost, err = parsePositiveInt(getenv("KIPPLE_FETCH_PER_HOST"), defaultFetchPerHost); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_FETCH_PER_HOST: %w", err)
	}
	if cfg.LogLevel, err = parseLevel(getenv("KIPPLE_LOG_LEVEL"), defaultLogLevel); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_LOG_LEVEL: %w", err)
	}
	if cfg.LogGreaderForms, err = parseBool(getenv("KIPPLE_LOG_GREADER_FORMS")); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_LOG_GREADER_FORMS: %w", err)
	}

	return cfg, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func parseIPList(v string) ([]netip.Addr, error) {
	if v == "" {
		return nil, nil
	}
	var ips []netip.Addr
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		addr, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("invalid IP %q: %w", part, err)
		}
		ips = append(ips, addr)
	}
	return ips, nil
}

func parseDuration(v string, def time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive, got %s", d)
	}
	return d, nil
}

func parsePositiveInt(v string, def int) (int, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be positive, got %d", n)
	}
	return n, nil
}

func parseBool(v string) (bool, error) {
	if v == "" {
		return false, nil
	}
	return strconv.ParseBool(v)
}

func parseLevel(v string, def slog.Level) (slog.Level, error) {
	if v == "" {
		return def, nil
	}
	switch strings.ToLower(v) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown level %q (want debug, info, warn or error)", v)
	}
}
