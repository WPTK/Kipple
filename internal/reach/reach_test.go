package reach

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
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
	require.Equal(t, []string{"nas", "*.example.org", "rss.example.com"}, st.HostNames, "hand-edited bad entries are dropped")
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
	require.Equal(t, []string{"rss.example.com"}, l.HostNames())

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
	require.Empty(t, l.HostNames())

	// What Update put in force is what a fresh read of the database gives.
	again, err := Open(ctx, db, Options{NoPrefetch: true})
	require.NoError(t, err)
	require.Equal(t, l.Get().Stored, again.Get().Stored)
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
