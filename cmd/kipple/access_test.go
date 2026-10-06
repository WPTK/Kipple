package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/store"
)

func TestEnsureAccountNeverPasswordlessOnFirstStart(t *testing.T) {
	noPrefetch(t)
	ctx := context.Background()
	cfg := config.Config{Username: "owner", AccessTeamDomain: "myteam.cloudflareaccess.com", AccessAUD: "aud"}

	// An empty KIPPLE_PASSWORD (the example file's default) creates nothing,
	// with or without Access: removing the password is a deliberate later step.
	for _, c := range []config.Config{{Username: "owner"}, cfg} {
		db := openDB(t)
		_, err := openReach(ctx, db, c, quiet)
		require.NoError(t, err)
		require.NoError(t, ensureAccount(ctx, db, c, quiet))
		_, ok, err := db.Account(ctx)
		require.NoError(t, err)
		require.False(t, ok)
	}

	// An account whose password was removed later (Settings, through Access).
	db := openDB(t)
	require.NoError(t, ensureAccount(ctx, db, config.Config{Username: "owner", Password: "web-pw"}, quiet))
	require.NoError(t, db.SetPasswordHash(ctx, "", store.AuthStandard, ""))

	// A start with Access off warns that web sign-in is impossible.
	var buf bytes.Buffer
	loud := slog.New(slog.NewTextHandler(&buf, nil))
	require.NoError(t, ensureAccount(ctx, db, config.Config{}, loud))
	require.Contains(t, buf.String(), "no web password")
	// With the Access setting on (seeded here by the variables), it does not.
	buf.Reset()
	_, err := openReach(ctx, db, cfg, quiet)
	require.NoError(t, err)
	require.NoError(t, ensureAccount(ctx, db, cfg, loud))
	require.NotContains(t, buf.String(), "no web password")
}

// The variables seed each reachability setting once; after that the setting
// decides, and a variable that disagrees is named in a warning.
func TestOpenReachSeedsOnceThenSettingsDecide(t *testing.T) {
	noPrefetch(t)
	ctx := context.Background()
	db := openDB(t)
	cfg := config.Config{
		PublicURL: "https://rss.example.com", AllowedHosts: []string{"nas.local"},
		TrustedProxyIPs:  []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("198.51.100.0/24")},
		AccessTeamDomain: "myteam.cloudflareaccess.com", AccessAUD: "aud",
	}
	live, err := openReach(ctx, db, cfg, quiet)
	require.NoError(t, err)
	st := live.Get()
	require.Equal(t, "https://rss.example.com", st.PublicURL)
	require.Equal(t, []string{"nas.local"}, st.HostNames)
	require.Equal(t, "rss.example.com", st.PublicHost)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("198.51.100.0/24")}, st.Trusted)
	require.NotNil(t, st.Access)
	sec, err := db.SecuritySettings(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"192.0.2.10", "198.51.100.0/24"}, sec.TrustedProxies)

	// Settings changed the public URL and turned Access off; the old variables stay set.
	require.NoError(t, db.SetSettings(ctx, map[string]any{store.SettingPublicURL: "https://reader.example.org", store.SettingCloudflareAccess: map[string]any{}}))
	var buf bytes.Buffer
	loud := slog.New(slog.NewTextHandler(&buf, nil))
	live, err = openReach(ctx, db, cfg, loud)
	require.NoError(t, err)
	require.Equal(t, "https://reader.example.org", live.PublicURL())
	require.Nil(t, live.Access())
	require.Contains(t, buf.String(), "KIPPLE_PUBLIC_URL")
	require.Contains(t, buf.String(), "KIPPLE_ACCESS_TEAM_DOMAIN")
	require.NotContains(t, buf.String(), "KIPPLE_TRUSTED_PROXY_IPS", "a variable equal to its setting is not mentioned")

	// No variables: nothing seeded, nothing warned; the INFO line says what is in force.
	buf.Reset()
	_, err = openReach(ctx, openDB(t), config.Config{}, loud)
	require.NoError(t, err)
	require.NotContains(t, buf.String(), "not used")
	require.Contains(t, buf.String(), "address and access settings in force")
}

// noPrefetch keeps an Access verifier built in a test off the network.
func noPrefetch(t *testing.T) {
	prefetchAccessKeys = false
	t.Cleanup(func() { prefetchAccessKeys = true })
}
