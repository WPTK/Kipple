package reach

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSeedOnceThenTheSettingDecides(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	seed := Seed{
		PublicURL: "https://rss.example.com", AllowedHosts: []string{"nas.local"},
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("2001:db8::/32")},
		AccessTeam:     "myteam.cloudflareaccess.com", AccessAUD: "aud",
	}
	ignored, err := SeedSettings(ctx, db, seed)
	require.NoError(t, err)
	require.Empty(t, ignored)
	sec, err := db.SecuritySettings(ctx)
	require.NoError(t, err)
	require.Equal(t, store.Security{
		PublicURL: "https://rss.example.com", AllowedHosts: []string{"nas.local"},
		TrustedProxies: []string{"192.0.2.10", "2001:db8::/32"},
		Access:         store.AccessConfig{TeamDomain: "myteam.cloudflareaccess.com", AUD: "aud"},
	}, sec)

	// The same seed again changes nothing and is not reported.
	ignored, err = SeedSettings(ctx, db, seed)
	require.NoError(t, err)
	require.Empty(t, ignored)

	// Settings changed two of them (one to "off"): a later seed is not used and is named.
	require.NoError(t, db.SetSettings(ctx, map[string]any{store.SettingPublicURL: "", store.SettingAllowedHosts: []any{"box.lan"}}))
	ignored, err = SeedSettings(ctx, db, seed)
	require.NoError(t, err)
	require.Equal(t, []string{store.SettingAllowedHosts, store.SettingPublicURL}, ignored)
	sec, err = db.SecuritySettings(ctx)
	require.NoError(t, err)
	require.Equal(t, "", sec.PublicURL)
	require.Equal(t, []string{"box.lan"}, sec.AllowedHosts)

	// An empty seed seeds and reports nothing.
	ignored, err = SeedSettings(ctx, openDB(t), Seed{})
	require.NoError(t, err)
	require.Empty(t, ignored)
}

// KIPPLE_PUBLIC_URL is judged only when it would be stored: an internationalized
// host is converted and stored, a bad value stops the start, and either one is
// only compared (and named as not used, when it differs) once the setting is
// stored.
func TestPublicURLSeedIsJudgedOnlyWhenStored(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	_, err := SeedSettings(ctx, db, Seed{PublicURL: "https://rss.bücher.example"})
	require.NoError(t, err)
	sec, err := db.SecuritySettings(ctx)
	require.NoError(t, err)
	require.Equal(t, "https://rss.xn--bcher-kva.example", sec.PublicURL)
	ignored, err := SeedSettings(ctx, db, Seed{PublicURL: "https://rss.bücher.example"})
	require.NoError(t, err)
	require.Empty(t, ignored, "the same address in either form is not reported")

	ignored, err = SeedSettings(ctx, db, Seed{PublicURL: "ftp://rss.example.com"})
	require.NoError(t, err, "a value that would be ignored never stops a start")
	require.Equal(t, []string{store.SettingPublicURL}, ignored)

	_, err = SeedSettings(ctx, openDB(t), Seed{PublicURL: "ftp://rss.example.com"})
	require.ErrorContains(t, err, "KIPPLE_PUBLIC_URL")
}

// An install from before the seed rule answered the variable's names and the
// stored ones together. Its first start under the seed rule adds the variable's
// names to the stored list once, so no name is lost; a name later removed in
// Settings is not added back.
func TestUpgradeMergesAllowedHostsOnce(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	// The row as an older Kipple stored it, with no merge recorded.
	require.NoError(t, db.SetSettings(ctx, map[string]any{store.SettingAllowedHosts: []any{"rss.example.com"}}))
	seed := Seed{AllowedHosts: []string{"nas.local", "rss.example.com"}}

	_, err := SeedSettings(ctx, db, seed)
	require.NoError(t, err)
	sec, err := db.SecuritySettings(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"rss.example.com", "nas.local"}, sec.AllowedHosts, "the union, once")

	require.NoError(t, db.SetSettings(ctx, map[string]any{store.SettingAllowedHosts: []any{"rss.example.com"}}))
	ignored, err := SeedSettings(ctx, db, seed)
	require.NoError(t, err)
	require.Equal(t, []string{store.SettingAllowedHosts}, ignored, "the variable is named as not used")
	sec, err = db.SecuritySettings(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"rss.example.com"}, sec.AllowedHosts, "a removal in Settings sticks")

	// A new install: the seed stores the variable, and a later removal sticks too.
	fresh := openDB(t)
	_, err = SeedSettings(ctx, fresh, seed)
	require.NoError(t, err)
	require.NoError(t, fresh.SetSettings(ctx, map[string]any{store.SettingAllowedHosts: []any{}}))
	_, err = SeedSettings(ctx, fresh, seed)
	require.NoError(t, err)
	sec, err = fresh.SecuritySettings(ctx)
	require.NoError(t, err)
	require.Empty(t, sec.AllowedHosts)
}

func TestOpenBuildsTheState(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	require.NoError(t, db.SetSettings(ctx, map[string]any{
		store.SettingPublicURL:      "https://RSS.example.com/kipple",
		store.SettingAllowedHosts:   []any{"nas", "*.example.org", "https://not-a-host"},
		store.SettingTrustedProxies: []any{"192.0.2.10", "198.51.100.0/24", "garbage"},
	}))
	l, err := Open(ctx, db, Options{NoPrefetch: true})
	require.NoError(t, err)
	st := l.Get()
	require.Equal(t, "https://RSS.example.com/kipple", st.PublicURL)
	require.Equal(t, []string{"nas", "*.example.org"}, st.HostNames, "hand-edited bad entries are dropped")
	require.Equal(t, "rss.example.com", st.PublicHost)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("198.51.100.0/24")}, st.Trusted)
	require.Nil(t, st.Access)

	// A hand-edited half Access pair, or a bad public URL, is off.
	require.NoError(t, db.SetSettings(ctx, map[string]any{
		store.SettingCloudflareAccess: map[string]any{"team_domain": "myteam.cloudflareaccess.com"},
		store.SettingPublicURL:        "rss.example.com",
	}))
	require.NoError(t, l.Reload(ctx))
	require.Nil(t, l.Access())
	require.Equal(t, "", l.PublicURL())
}

func TestUpdateAppliesTheWrittenValues(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	l, err := Open(ctx, db, Options{NoPrefetch: true})
	require.NoError(t, err)
	write := func(set map[string]any) func() error {
		return func() error { return db.SetSettings(ctx, set) }
	}

	on := map[string]any{store.SettingCloudflareAccess: map[string]any{"team_domain": "myteam.cloudflareaccess.com", "aud": "aud"},
		store.SettingTrustedProxies: []any{"192.0.2.10"}, "refresh.interval_minutes": 60}
	require.NoError(t, l.Update(on, nil, write(on)))
	v := l.Access()
	require.NotNil(t, v)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}, l.Trusted())

	// A write that leaves Access alone keeps the same verifier (its key set stays loaded).
	url := map[string]any{store.SettingPublicURL: "https://rss.example.com"}
	require.NoError(t, l.Update(url, nil, write(url)))
	require.Same(t, v, l.Access())
	require.Equal(t, "rss.example.com", l.Get().PublicHost)

	// A refused check and a failed write change nothing.
	off := map[string]any{store.SettingCloudflareAccess: nil, store.SettingPublicURL: ""}
	refused := errors.New("refused")
	require.ErrorIs(t, l.Update(off, func(cur *State) error {
		require.NotNil(t, cur.Access)
		return refused
	}, write(off)), refused)
	require.Same(t, v, l.Access())
	require.ErrorIs(t, l.Update(off, nil, func() error { return refused }), refused)
	require.Equal(t, "https://rss.example.com", l.PublicURL())

	// A reset (nil) is the default.
	require.NoError(t, l.Update(off, nil, write(off)))
	require.Nil(t, l.Access())
	require.Equal(t, "", l.PublicURL())
	require.Empty(t, l.Get().PublicHost)

	// What Update put in force is what a fresh read of the database gives.
	again, err := Open(ctx, db, Options{NoPrefetch: true})
	require.NoError(t, err)
	require.Equal(t, l.Get().Stored, again.Get().Stored)
}

// A public URL at a LAN name is used, and its host is the state's public host
// like any other (the Host gate decides per mode; see setup.OpenHostAllowed).
// The allowed names stay what was listed.
func TestPublicHostIsKeptApartFromListedNames(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	l, err := Open(ctx, db, Options{NoPrefetch: true})
	require.NoError(t, err)
	for u, h := range map[string]string{"http://nas.local:1919": "nas.local", "http://unraid:1919": "unraid", "https://rss.home.arpa": "rss.home.arpa", "http://kipple.fritz.box:1919": "kipple.fritz.box"} {
		set := map[string]any{store.SettingPublicURL: u}
		require.NoError(t, l.Update(set, nil, func() error { return db.SetSettings(ctx, set) }))
		require.Equal(t, u, l.PublicURL())
		require.Equal(t, h, l.Get().PublicHost)
		require.Empty(t, l.HostNames(), u)
	}
	set := map[string]any{store.SettingPublicURL: "https://rss.example.com", store.SettingAllowedHosts: []any{"nas.local"}}
	require.NoError(t, l.Update(set, nil, func() error { return db.SetSettings(ctx, set) }))
	require.Equal(t, []string{"nas.local"}, l.HostNames())
	require.Equal(t, "rss.example.com", l.Get().PublicHost)
}

func TestNilAndFixed(t *testing.T) {
	var l *Live
	require.Equal(t, "", l.PublicURL())
	require.Nil(t, l.Trusted())
	require.Nil(t, l.Access())
	f := Fixed(State{PublicURL: "https://rss.example.com"})
	require.Equal(t, "https://rss.example.com", f.PublicURL())
}

func TestHelpers(t *testing.T) {
	require.Equal(t, "rss.example.com", Host("https://RSS.example.com:8443/x"))
	require.Equal(t, "", Host(""))
	require.Equal(t, "192.0.2.10", FormatProxy(netip.MustParsePrefix("192.0.2.10/32")))
	require.Equal(t, "2001:db8::1", FormatProxy(netip.MustParsePrefix("2001:db8::1/128")))
	require.Equal(t, "198.51.100.0/24", FormatProxy(netip.MustParsePrefix("198.51.100.0/24")))
	for _, ok := range []string{"", "https://rss.example.com", "http://192.0.2.1:1919/kipple/"} {
		require.NoError(t, CheckPublicURL(ok), ok)
	}
	for _, bad := range []string{"rss.example.com", " https://rss.example.com", "https://", "https://u:p@x.example", "https://x.example/#a", "https://x.example/?", "mailto:x@example.com"} {
		require.Error(t, CheckPublicURL(bad), bad)
	}
}

// A seed about to be stored passes the rules of a settings write, and a failure
// says what to do; a seed that would be ignored (its setting is stored) is never
// judged, so a stale variable cannot stop a start.
func TestSeedPolicyOnlyWhenStored(t *testing.T) {
	ctx := context.Background()
	wide := Seed{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}}
	_, err := SeedSettings(ctx, openDB(t), wide)
	require.ErrorContains(t, err, "KIPPLE_TRUSTED_PROXY_IPS")
	require.ErrorContains(t, err, "list only the address your proxy connects from (docs/reverse-proxy.md), or remove the variable")
	_, err = SeedSettings(ctx, openDB(t), Seed{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("2000::/3")}})
	require.ErrorContains(t, err, "too wide")
	// A LAN name is a fine public URL, seeded like any other.
	_, err = SeedSettings(ctx, openDB(t), Seed{PublicURL: "http://nas.local:1919"})
	require.NoError(t, err)

	db := openDB(t)
	require.NoError(t, db.SetSettings(ctx, map[string]any{store.SettingTrustedProxies: []any{"192.0.2.10"}, store.SettingPublicURL: ""}))
	ignored, err := SeedSettings(ctx, db, Seed{PublicURL: "https://seed.example.com", TrustedProxies: wide.TrustedProxies})
	require.NoError(t, err, "ignored variables are not judged")
	require.Equal(t, []string{store.SettingTrustedProxies, store.SettingPublicURL}, ignored)

	// An internationalized seed is stored in its xn-- form.
	db = openDB(t)
	_, err = SeedSettings(ctx, db, Seed{PublicURL: "https://bücher.example"})
	require.NoError(t, err)
	sec, err := db.SecuritySettings(ctx)
	require.NoError(t, err)
	require.Equal(t, "https://xn--bcher-kva.example", sec.PublicURL)
}

func TestNormalizePublicURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "",
		"https://rss.example.com":       "https://rss.example.com",
		"https://bücher.example:8443/x": "https://xn--bcher-kva.example:8443/x",
		"http://192.168.1.20:1919":      "http://192.168.1.20:1919",
		"http://[2001:db8::1]:1919/":    "http://[2001:db8::1]:1919/",
		"https://box.tail1234.ts.net":   "https://box.tail1234.ts.net",
		"http://localhost:1919":         "http://localhost:1919",
	} {
		got, err := NormalizePublicURL(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"rss.example.com", "https://", "ftp://x.example", "https://u@x.example", "http://*.example.com", "http://*.home", "https://a_b%2A.example"} {
		_, err := NormalizePublicURL(bad)
		require.Error(t, err, bad)
	}
	require.ErrorContains(t, CheckPublicURL("https://bücher.example"), "xn--")
	require.ErrorContains(t, CheckPublicURL("http://*.example.com"), "one name or IP address")
	require.Equal(t, "", Host("http://*.example.com"), "never a wildcard entry")
}

// Reads never see a half-applied state while writes swap it: every State read
// is one of the written ones, whole (its public URL, host names and proxies
// belong together). Meaningful without -race (the invariant), and CI runs it
// with -race too.
func TestConcurrentUpdatesAndReads(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	l, err := Open(ctx, db, Options{NoPrefetch: true})
	require.NoError(t, err)
	states := []map[string]any{
		{store.SettingPublicURL: "https://a.example.com", store.SettingTrustedProxies: []any{"192.0.2.1"}},
		{store.SettingPublicURL: "https://b.example.com", store.SettingTrustedProxies: []any{"192.0.2.2"}},
	}
	want := map[string]string{"https://a.example.com": "192.0.2.1/32", "https://b.example.com": "192.0.2.2/32"}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var reads atomic.Int64
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				st := l.Get()
				reads.Add(1)
				if st.PublicURL == "" {
					continue
				}
				if len(st.Trusted) != 1 || st.Trusted[0].String() != want[st.PublicURL] || "https://"+st.PublicHost != st.PublicURL || st.Stored.PublicURL != st.PublicURL {
					t.Errorf("a torn state: %+v", st)
					return
				}
			}
		}()
	}
	var writers sync.WaitGroup
	for w := 0; w < 2; w++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; i < 100; i++ {
				set := states[(i+w)%2]
				if err := l.Update(set, nil, func() error { return db.SetSettings(ctx, set) }); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	writers.Wait()
	close(stop)
	wg.Wait()
	require.Positive(t, reads.Load())
	// The state in force is the one last written, as a fresh read of the database says.
	again, err := Open(ctx, db, Options{NoPrefetch: true})
	require.NoError(t, err)
	require.Equal(t, again.Get().Stored, l.Get().Stored)
}
