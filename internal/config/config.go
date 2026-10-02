// Package config reads Kipple's KIPPLE_* environment variables (plus the
// standard TZ) into a Config, applying defaults. Nothing else in the
// codebase calls os.Getenv: everything that needs configuration takes it
// from a Config passed down from cmd/kipple.
package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/access"
	"github.com/WPTK/kipple/internal/setup"
)

// Config holds every setting Kipple reads at startup. See .env.example for
// a full description of each field.
type Config struct {
	Addr            string        // KIPPLE_ADDR, default DefaultAddr (":1919")
	AddrSet         bool          // KIPPLE_ADDR was set (the legacy port and the 1138 fallback apply only when it is not)
	DataDir         string        // KIPPLE_DATA, default "/data"
	Username        string        // KIPPLE_USERNAME
	Password        string        // KIPPLE_PASSWORD, initial web password
	APIPassword     string        // KIPPLE_API_PASSWORD, optional initial
	PublicURL       string        // KIPPLE_PUBLIC_URL
	TrustedProxyIPs []netip.Addr  // KIPPLE_TRUSTED_PROXY_IPS, comma-separated
	TZ              string        // TZ: "" when unset. Only seeds the tz setting of a new install (store.SeedZone)
	AllowedHosts    []string      // KIPPLE_ALLOWED_HOSTS, comma-separated host names or *.suffix (normalized)
	SchedTick       time.Duration // KIPPLE_SCHED_TICK, default 30s
	FetchWorkers    int           // KIPPLE_FETCH_WORKERS, default 8
	FetchPerHost    int           // KIPPLE_FETCH_PER_HOST, default 2
	LogLevel        slog.Level    // KIPPLE_LOG_LEVEL, default info
	LogGreaderForms bool          // KIPPLE_LOG_GREADER_FORMS, default false
	// Cloudflare Access token validation (optional, both or neither): the team
	// domain as a bare host (normalized) and the application AUD tag.
	AccessTeamDomain string // KIPPLE_ACCESS_TEAM_DOMAIN
	AccessAUD        string // KIPPLE_ACCESS_AUD
}

// Listen ports. DefaultAddr is the default of KIPPLE_ADDR; LegacyAddr is the
// pre-0.5 default, kept for databases that already had an account (through
// 0.x); FallbackAddr is used when KIPPLE_ADDR is unset and DefaultAddr is taken.
const (
	DefaultAddr  = ":1919"
	LegacyAddr   = ":7080"
	FallbackAddr = ":1138"
)

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
		AddrSet:     getenv("KIPPLE_ADDR") != "",
		DataDir:     orDefault(getenv("KIPPLE_DATA"), defaultDataDir),
		Username:    getenv("KIPPLE_USERNAME"),
		Password:    getenv("KIPPLE_PASSWORD"),
		APIPassword: getenv("KIPPLE_API_PASSWORD"),
		PublicURL:   getenv("KIPPLE_PUBLIC_URL"),
		TZ:          getenv("TZ"),
	}

	var err error
	if err = checkPublicURL(cfg.PublicURL); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_PUBLIC_URL: %w", err)
	}
	if cfg.AllowedHosts, err = setup.ParseAllowedHosts(getenv("KIPPLE_ALLOWED_HOSTS")); err != nil {
		return Config{}, fmt.Errorf("KIPPLE_ALLOWED_HOSTS: %w", err)
	}
	if cfg.TrustedProxyIPs, err = parseIPList(getenv("KIPPLE_TRUSTED_PROXY_IPS")); err != nil {
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

// AccessEnabled reports whether Cloudflare Access token validation is configured.
func (c Config) AccessEnabled() bool { return c.AccessTeamDomain != "" && c.AccessAUD != "" }

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

// checkPublicURL accepts an empty value or an absolute http(s) URL with a host
// and nothing that cannot be a base for other URLs: no user info, query or
// fragment, and no spaces or control characters.
func checkPublicURL(v string) error {
	if v == "" {
		return nil
	}
	if strings.TrimSpace(v) != v || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("%q has spaces or control characters", v)
	}
	u, err := url.Parse(v)
	if err != nil {
		return err
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("%q must start with http:// or https://", v)
	case u.Host == "" || u.Hostname() == "":
		return fmt.Errorf("%q has no host", v)
	case u.User != nil:
		return fmt.Errorf("%q must not contain user info", v)
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(v, "?#"):
		return fmt.Errorf("%q must not have a query or fragment", v)
	case u.Opaque != "":
		return fmt.Errorf("%q is not an absolute URL", v)
	}
	return nil
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
		// Unmapped, like the peer address it is compared with (auth.ClientIP):
		// ::ffff:192.0.2.10 must trust the peer 192.0.2.10.
		ips = append(ips, addr.Unmap())
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
