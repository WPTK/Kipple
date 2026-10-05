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

	"github.com/WPTK/kipple/internal/access"
	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/reach"
	"github.com/WPTK/kipple/internal/setup"
)

// Config holds every setting Kipple reads at startup. See .env.example for
// a full description of each field.
type Config struct {
	Addr        string // KIPPLE_ADDR, default DefaultAddr (":1919")
	DataDir     string // KIPPLE_DATA, default "/data"
	Username    string // KIPPLE_USERNAME
	Password    string // KIPPLE_PASSWORD, initial web password
	APIPassword string // KIPPLE_API_PASSWORD, optional initial
	// The reachability values only seed their settings when those were never
	// stored (reach.SeedSettings); the settings decide from then on.
	PublicURL       string         // KIPPLE_PUBLIC_URL, seeds server.public_url
	TrustedProxyIPs []netip.Prefix // KIPPLE_TRUSTED_PROXY_IPS, comma-separated addresses or CIDR ranges; seeds security.trusted_proxies
	TZ              string         // TZ: "" when unset. Only seeds the tz setting of a new install (store.SeedZone)
	AllowedHosts    []string       // KIPPLE_ALLOWED_HOSTS, comma-separated host names or *.suffix (normalized); seeds security.allowed_hosts
	SchedTick       time.Duration  // KIPPLE_SCHED_TICK, default 30s
	FetchWorkers    int            // KIPPLE_FETCH_WORKERS, default 8
	FetchPerHost    int            // KIPPLE_FETCH_PER_HOST, default 2
	LogLevel        slog.Level     // KIPPLE_LOG_LEVEL, default info
	LogGreaderForms bool           // KIPPLE_LOG_GREADER_FORMS, default false
	// Cloudflare Access token validation (optional, both or neither): the team
	// domain as a bare host (normalized) and the application AUD tag. They seed
	// security.cloudflare_access.
	AccessTeamDomain string // KIPPLE_ACCESS_TEAM_DOMAIN
	AccessAUD        string // KIPPLE_ACCESS_AUD
}

// DefaultAddr is the default of KIPPLE_ADDR. There is no other port: a taken
// address is an error the operator fixes by setting KIPPLE_ADDR.
const DefaultAddr = ":1919"

const (
	defaultDataDir      = "/data"
	defaultSchedTick    = 30 * time.Second
	defaultFetchWorkers = 8
	defaultFetchPerHost = 2
	defaultLogLevel     = slog.LevelInfo

	// minSchedTick is the shortest scheduler tick: a smaller one only burns CPU
	// on due-feed queries (feeds are polled every few minutes at the most).
	minSchedTick = time.Second
)

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return load(os.Getenv)
}

// load is Load with an injectable lookup, so tests never touch the real
// environment.
func load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:        orDefault(getenv("KIPPLE_ADDR"), DefaultAddr),
		DataDir:     orDefault(getenv("KIPPLE_DATA"), defaultDataDir),
		Username:    getenv("KIPPLE_USERNAME"),
		Password:    getenv("KIPPLE_PASSWORD"),
		APIPassword: getenv("KIPPLE_API_PASSWORD"),
		PublicURL:   getenv("KIPPLE_PUBLIC_URL"),
		TZ:          getenv("TZ"),
	}

	// KIPPLE_PUBLIC_URL is judged by reach.SeedSettings, and only when it would be
	// stored: it converts an internationalized host first, and a value that is
	// ignored (the setting is already stored) must not stop a start.
	var err error
	if cfg.AllowedHosts, err = setup.ParseAllowedHosts(getenv("KIPPLE_ALLOWED_HOSTS")); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_ALLOWED_HOSTS: %w", err)
	}
	if cfg.TrustedProxyIPs, err = auth.ParseProxies(getenv("KIPPLE_TRUSTED_PROXY_IPS")); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_TRUSTED_PROXY_IPS: %w", err)
	}
	if cfg.SchedTick, err = parseDuration(getenv("KIPPLE_SCHED_TICK"), defaultSchedTick); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_SCHED_TICK: %w", err)
	}
	if cfg.SchedTick < minSchedTick {
		return Config{}, fmt.Errorf("KIPPLE_SCHED_TICK: must be at least %s, got %s", minSchedTick, cfg.SchedTick)
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

	if cfg.AccessTeamDomain, cfg.AccessAUD, err = parseAccess(getenv("KIPPLE_ACCESS_TEAM_DOMAIN"), getenv("KIPPLE_ACCESS_AUD")); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// ReachSeed is the environment's seed for the reachability settings.
func (c Config) ReachSeed() reach.Seed {
	return reach.Seed{PublicURL: c.PublicURL, AllowedHosts: c.AllowedHosts, TrustedProxies: c.TrustedProxyIPs,
		AccessTeam: c.AccessTeamDomain, AccessAUD: c.AccessAUD}
}

// parseAccess validates the Cloudflare Access pair: both unset (the feature is
// off) or both set. One without the other stops startup, so a half-done setup
// is never mistaken for a working one. The values are validated by the access
// package's own rules (the same ones access.New applies).
func parseAccess(team, aud string) (string, string, error) {
	team, aud = strings.TrimSpace(team), strings.TrimSpace(aud)
	switch {
	case team == "" && aud == "":
		return "", "", nil
	case team == "":
		return "", "", fmt.Errorf("KIPPLE_ACCESS_TEAM_DOMAIN: required when KIPPLE_ACCESS_AUD is set (set both or neither)")
	case aud == "":
		return "", "", fmt.Errorf("KIPPLE_ACCESS_AUD: required when KIPPLE_ACCESS_TEAM_DOMAIN is set (set both or neither)")
	}
	host, err := access.NormalizeTeamDomain(team)
	if err != nil {
		return "", "", fmt.Errorf("KIPPLE_ACCESS_TEAM_DOMAIN: %w", err)
	}
	if aud, err = access.CheckAUD(aud); err != nil {
		return "", "", fmt.Errorf("KIPPLE_ACCESS_AUD: %w", err)
	}
	return host, aud, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
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
