// Package reach holds how people reach this Kipple and who may (docs/design.md
// §7.1f): the public URL, the allowed host names, the trusted proxies and
// Cloudflare Access. They are settings, changed in the setup wizard and in
// Settings and applied at once, with no restart. The environment variables of
// the same names only seed a setting that has never been stored (Seed).
//
// Live is the one copy the server reads. It is built from the database at start
// and updated by the settings write that changes one of them (Update), so every
// reader (the Host gate, client addresses, the Secure cookie flag, the open
// gate, Reader API icons, the outgoing User-Agent, Access sign-in) sees the
// same values.
package reach

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/net/idna"

	"github.com/WPTK/kipple/internal/access"
	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

// State is one consistent set of the reachability settings, ready to use.
type State struct {
	// Stored is the settings as stored (normalized by the API's validators).
	Stored store.Security
	// PublicURL is the address Kipple is reached at, "" when none.
	PublicURL string
	// Trusted are the proxies allowed to say who the client is.
	Trusted []netip.Prefix
	// Access verifies Cloudflare Access tokens; nil when validation is off.
	Access *access.Verifier
	// HostNames are the allowed host names, normalized by setup.CheckHostEntry.
	HostNames []string
	// PublicHost is the host of PublicURL, "" when none. The Host gate answers it
	// in setup mode, and in open mode unless any LAN device can answer it
	// (setup.HostAllowed, setup.OpenHostAllowed).
	PublicHost string
}

// Options configure Open.
type Options struct {
	Logger *slog.Logger
	// Access are the options every Access verifier is built with (tests point
	// them at a fake key set). Its Logger defaults to Logger.
	Access access.Options
	// NoPrefetch leaves a new verifier's key set to the first request (tests).
	NoPrefetch bool
}

// Live is the reachability settings in force.
type Live struct {
	db  *store.DB
	opt Options
	log *slog.Logger

	mu  sync.Mutex // serializes Update (its write and the swap) and Reload
	cur atomic.Pointer[State]
}

// Open reads the settings from db and builds the state in force.
func Open(ctx context.Context, db *store.DB, opt Options) (*Live, error) {
	l := &Live{db: db, opt: opt, log: opt.Logger}
	if l.log == nil {
		l.log = slog.Default()
	}
	if l.opt.Access.Logger == nil {
		l.opt.Access.Logger = l.log
	}
	if err := l.Reload(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// Fixed is a Live that always holds st (tests, and callers without a database).
func Fixed(st State) *Live {
	l := &Live{log: slog.Default()}
	l.cur.Store(&st)
	return l
}

// Reload reads the settings from the database again.
func (l *Live) Reload(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	sec, err := l.db.SecuritySettings(ctx)
	if err != nil {
		return fmt.Errorf("reading the reachability settings: %w", err)
	}
	l.install(sec)
	return nil
}

// Update runs write (the settings write, which commits set) and then puts the
// reachability keys of set into force, under one lock so two writes cannot
// swap in the wrong order. set holds the values exactly as written (a nil value
// is a reset to the default). It never reads the database again: the values
// just committed are the stored ones, as this process is their only writer.
// check, when not nil, runs first under the same lock with the state in force
// and may refuse the write.
func (l *Live) Update(set map[string]any, check func(cur *State) error, write func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := l.Get()
	if check != nil {
		if err := check(cur); err != nil {
			return err
		}
	}
	if err := write(); err != nil {
		return err
	}
	sec := cur.Stored
	changed := false
	for _, k := range store.ReachKeys {
		v, ok := set[k]
		if !ok {
			continue
		}
		changed = true
		overlay(&sec, k, v)
	}
	if changed {
		l.install(sec)
	}
	return nil
}

// Locked runs fn with the state in force under the lock Update holds, so a
// write elsewhere that depends on these settings (removing the web password
// needs Access on) cannot interleave with a settings write that changes them.
func (l *Live) Locked(fn func(cur *State) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return fn(l.Get())
}

// overlay puts one written value (nil: the default) into sec.
func overlay(sec *store.Security, key string, v any) {
	switch key {
	case store.SettingPublicURL:
		s, _ := v.(string)
		sec.PublicURL = s
	case store.SettingAllowedHosts:
		sec.AllowedHosts = strings1(v)
	case store.SettingTrustedProxies:
		sec.TrustedProxies = strings1(v)
	case store.SettingCloudflareAccess:
		m, _ := v.(map[string]any)
		team, _ := m["team_domain"].(string)
		aud, _ := m["aud"].(string)
		sec.Access = store.AccessConfig{TeamDomain: team, AUD: aud}
		if team == "" || aud == "" {
			sec.Access = store.AccessConfig{}
		}
	}
}

// strings1 is the strings of a written JSON list.
func strings1(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// install builds the state for sec and puts it in force. The Access verifier
// in force is kept when its team and audience did not change (its key set stays
// loaded). Caller holds mu.
func (l *Live) install(sec store.Security) {
	st := &State{Stored: sec}
	if u, err := NormalizePublicURL(sec.PublicURL); err == nil && u == sec.PublicURL {
		st.PublicURL = u
	} else {
		l.log.Error("the stored public URL is not valid and is not used", "err", err)
	}
	for _, e := range sec.TrustedProxies {
		ps, err := auth.ParseProxies(e)
		if err != nil || len(ps) != 1 || auth.ProxyTooWide(ps[0]) {
			// Only a hand-edited row gets here (the API validates with the same rules).
			l.log.Error("a stored trusted proxy is not valid and is not trusted", "entry", e)
			continue
		}
		st.Trusted = append(st.Trusted, ps[0])
	}
	for _, e := range sec.AllowedHosts {
		if n, err := setup.CheckHostEntry(e); err == nil {
			st.HostNames = append(st.HostNames, n)
		}
	}
	st.PublicHost = Host(st.PublicURL)
	old := l.cur.Load()
	switch a := sec.Access; {
	case a.TeamDomain == "":
	case old != nil && old.Access != nil && old.Stored.Access == a:
		st.Access = old.Access
	default:
		v, err := access.New(a.TeamDomain, a.AUD, l.opt.Access)
		if err != nil {
			// Only a hand-edited row gets here (the API validates with the same rules).
			l.log.Error("Cloudflare Access validation stays off: the stored setting is not valid", "err", err)
			break
		}
		st.Access = v
		l.log.Info("Cloudflare Access token validation on", "issuer", v.Issuer())
		if !l.opt.NoPrefetch {
			// Bounded by the verifier's own fetch timeout; a failure is logged there.
			go func() { _ = v.Prefetch(context.Background()) }()
		}
	}
	if old != nil && old.Access != nil && st.Access == nil {
		l.log.Info("Cloudflare Access token validation off")
	}
	l.cur.Store(st)
}

// Get is the state in force. A nil Live is the empty state.
func (l *Live) Get() *State {
	if l == nil {
		return &State{}
	}
	if st := l.cur.Load(); st != nil {
		return st
	}
	return &State{}
}

// PublicURL is the public URL in force, "" when none.
func (l *Live) PublicURL() string { return l.Get().PublicURL }

// Trusted is the trusted proxies in force.
func (l *Live) Trusted() []netip.Prefix { return l.Get().Trusted }

// Access is the Access verifier in force, nil when validation is off.
func (l *Live) Access() *access.Verifier { return l.Get().Access }

// HostNames is the allowed host names in force.
func (l *Live) HostNames() []string { return l.Get().HostNames }

// Host is the host of a public URL as an allowed-host entry, "" when it has none.
func Host(publicURL string) string {
	if publicURL == "" {
		return ""
	}
	u, err := url.Parse(publicURL)
	if err != nil {
		return ""
	}
	h, ok := setup.NormalizeHost(u.Host)
	if !ok {
		return ""
	}
	return h
}

// CheckPublicURL accepts an empty value or an absolute http(s) URL with a host
// and nothing that cannot be a base for other URLs: no user info, query or
// fragment, and no spaces or control characters.
func CheckPublicURL(v string) error {
	if v == "" {
		return nil
	}
	if strings.TrimSpace(v) != v || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("%q has spaces or control characters", v)
	}
	u, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("%q is not a URL", v)
	}
	if strings.ContainsFunc(u.Host, func(r rune) bool { return r > 0x7e }) {
		return fmt.Errorf("%q: write the host in its ASCII (xn--) form", v)
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
	// The host is one exact name or address, the way a browser sends it: never a
	// wildcard or anything else the Host gate would read as more than one name.
	if _, ok := setup.NormalizeHost(u.Host); !ok {
		return fmt.Errorf("%q: the host must be one name or IP address, such as rss.example.com (no wildcards)", v)
	}
	return nil
}

// NormalizePublicURL is the rule for a public URL that is put in force (the
// setting, or a seed about to be stored): CheckPublicURL, with an
// internationalized host written in its ASCII (xn--) form so the Host gate and
// the User-Agent name the same host. "" is "no public URL". A name any device
// on the local network can answer (http://nas.local:1919) is a fine public URL;
// install keeps it out of the Host gate's names (see there).
func NormalizePublicURL(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if strings.TrimSpace(v) != v || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", fmt.Errorf("%q has spaces or control characters", v)
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return "", CheckPublicURL(v)
	}
	host := u.Hostname()
	if strings.ContainsFunc(host, func(r rune) bool { return r > 0x7e }) {
		a, err := idna.Lookup.ToASCII(host)
		if err != nil {
			return "", fmt.Errorf("%q: the host is not a valid name", v)
		}
		if port := u.Port(); port != "" {
			u.Host = net.JoinHostPort(a, port)
		} else {
			u.Host = a
		}
		v = u.String()
	}
	if err := CheckPublicURL(v); err != nil {
		return "", err
	}
	return v, nil
}

// FormatProxy is a trusted proxy as stored and shown: an address alone for a
// range of one, else the range.
func FormatProxy(p netip.Prefix) string {
	if p.Bits() == p.Addr().BitLen() {
		return p.Addr().String()
	}
	return p.String()
}

// Seed is the environment's values for the reachability settings, each one
// already validated by package config (an empty one seeds nothing).
type Seed struct {
	PublicURL      string
	AllowedHosts   []string
	TrustedProxies []netip.Prefix
	AccessTeam     string
	AccessAUD      string
}

// EnvNames maps each setting to the variable that seeds it, for messages.
var EnvNames = map[string]string{
	store.SettingPublicURL:        "KIPPLE_PUBLIC_URL",
	store.SettingAllowedHosts:     "KIPPLE_ALLOWED_HOSTS",
	store.SettingTrustedProxies:   "KIPPLE_TRUSTED_PROXY_IPS",
	store.SettingCloudflareAccess: "KIPPLE_ACCESS_TEAM_DOMAIN and KIPPLE_ACCESS_AUD",
}

// SeedSettings stores each value of seed as its setting when that setting has
// never been stored, and returns the settings that were stored already with a
// different value (their variables were not used). This is the one rule: the
// setting is the only source; a variable only gives a setting its first value.
// A value about to be stored must pass the same rules as a settings write (a
// trusted range that is too wide, a public URL host that is not a valid name),
// and one that does not stops the start with what to do; a variable that would
// be ignored is never judged.
func SeedSettings(ctx context.Context, db *store.DB, seed Seed) ([]string, error) {
	// Before the seed rule: an upgrade from the rule that answered the variable's
	// names and the stored ones together keeps every name (once).
	if err := db.MergeAllowedHostsOnce(ctx, seed.AllowedHosts); err != nil {
		return nil, err
	}
	stored, err := db.StoredSettings(ctx, store.ReachKeys)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if seed.PublicURL != "" {
		u := seed.PublicURL
		if !stored[store.SettingPublicURL] {
			if u, err = NormalizePublicURL(u); err != nil {
				return nil, fmt.Errorf("KIPPLE_PUBLIC_URL: %w (fix it, or remove the variable)", err)
			}
		}
		m[store.SettingPublicURL] = u
	}
	if len(seed.AllowedHosts) > 0 {
		m[store.SettingAllowedHosts] = seed.AllowedHosts
	}
	if len(seed.TrustedProxies) > 0 {
		l := make([]string, len(seed.TrustedProxies))
		for i, p := range seed.TrustedProxies {
			if !stored[store.SettingTrustedProxies] && auth.ProxyTooWide(p) {
				return nil, fmt.Errorf("KIPPLE_TRUSTED_PROXY_IPS: %s is too wide to trust (any client in it could choose its own address): list only the address your proxy connects from (docs/reverse-proxy.md), or remove the variable", p)
			}
			l[i] = FormatProxy(p)
		}
		m[store.SettingTrustedProxies] = l
	}
	if seed.AccessTeam != "" && seed.AccessAUD != "" {
		m[store.SettingCloudflareAccess] = store.AccessConfig{TeamDomain: seed.AccessTeam, AUD: seed.AccessAUD}
	}
	return db.SeedSettings(ctx, m)
}
