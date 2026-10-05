package config

import (
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(env(nil))
	require.NoError(t, err)

	require.Equal(t, ":1919", cfg.Addr)
	require.Equal(t, "/data", cfg.DataDir)
	require.Equal(t, "", cfg.Username)
	require.Equal(t, "", cfg.Password)
	require.Equal(t, "", cfg.APIPassword)
	require.Equal(t, "", cfg.PublicURL)
	require.Nil(t, cfg.TrustedProxyIPs)
	require.Equal(t, "", cfg.TZ, "unset: the in-app tz setting governs")
	require.Nil(t, cfg.AllowedHosts)
	require.Equal(t, 30*time.Second, cfg.SchedTick)
	require.Equal(t, 8, cfg.FetchWorkers)
	require.Equal(t, 2, cfg.FetchPerHost)
	require.Equal(t, slog.LevelInfo, cfg.LogLevel)
	require.False(t, cfg.LogGreaderForms)
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"KIPPLE_ADDR":              "127.0.0.1:9090",
		"KIPPLE_DATA":              "/var/lib/kipple",
		"KIPPLE_USERNAME":          "owner",
		"KIPPLE_PASSWORD":          "hunter2",
		"KIPPLE_API_PASSWORD":      "api-secret",
		"KIPPLE_PUBLIC_URL":        "https://rss.example.com",
		"KIPPLE_TRUSTED_PROXY_IPS": " 192.0.2.20 , 127.0.0.1",
		"TZ":                       "America/Chicago",
		"KIPPLE_SCHED_TICK":        "1m",
		"KIPPLE_FETCH_WORKERS":     "4",
		"KIPPLE_FETCH_PER_HOST":    "1",
		"KIPPLE_LOG_LEVEL":         "debug",
		"KIPPLE_LOG_GREADER_FORMS": "1",
		"KIPPLE_ALLOWED_HOSTS":     " RSS.example.com. , *.Example.org ",
	}))
	require.NoError(t, err)

	require.Equal(t, "127.0.0.1:9090", cfg.Addr)
	require.Equal(t, []string{"rss.example.com", "*.example.org"}, cfg.AllowedHosts)
	require.Equal(t, "/var/lib/kipple", cfg.DataDir)
	require.Equal(t, "owner", cfg.Username)
	require.Equal(t, "hunter2", cfg.Password)
	require.Equal(t, "api-secret", cfg.APIPassword)
	require.Equal(t, "https://rss.example.com", cfg.PublicURL)
	require.Equal(t, []netip.Prefix{
		netip.MustParsePrefix("192.0.2.20/32"),
		netip.MustParsePrefix("127.0.0.1/32"),
	}, cfg.TrustedProxyIPs)
	require.Equal(t, "America/Chicago", cfg.TZ)
	require.Equal(t, time.Minute, cfg.SchedTick)
	require.Equal(t, 4, cfg.FetchWorkers)
	require.Equal(t, 1, cfg.FetchPerHost)
	require.Equal(t, slog.LevelDebug, cfg.LogLevel)
	require.True(t, cfg.LogGreaderForms)
}

func TestLoadInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"bad trusted proxy IP":    {"KIPPLE_TRUSTED_PROXY_IPS": "not-an-ip"},
		"bad trusted proxy range": {"KIPPLE_TRUSTED_PROXY_IPS": "10.0.0.0/33"},
		"every address trusted":   {"KIPPLE_TRUSTED_PROXY_IPS": "192.0.2.1, 0.0.0.0/0"},
		"bad sched tick":          {"KIPPLE_SCHED_TICK": "soon"},
		"negative sched tick":     {"KIPPLE_SCHED_TICK": "-1s"},
		"bad fetch workers":       {"KIPPLE_FETCH_WORKERS": "many"},
		"zero fetch workers":      {"KIPPLE_FETCH_WORKERS": "0"},
		"bad fetch per host":      {"KIPPLE_FETCH_PER_HOST": "lots"},
		"bad log level":           {"KIPPLE_LOG_LEVEL": "shout"},
		"bad log greader bool":    {"KIPPLE_LOG_GREADER_FORMS": "maybe"},
		"sched tick under 1s":     {"KIPPLE_SCHED_TICK": "500ms"},
		"sched tick 1ns":          {"KIPPLE_SCHED_TICK": "1ns"},
		"public URL no scheme":    {"KIPPLE_PUBLIC_URL": "rss.example.com"},
		"public URL ftp":          {"KIPPLE_PUBLIC_URL": "ftp://rss.example.com"},
		"public URL no host":      {"KIPPLE_PUBLIC_URL": "https://"},
		"public URL query":        {"KIPPLE_PUBLIC_URL": "https://rss.example.com/?x=1"},
		"public URL fragment":     {"KIPPLE_PUBLIC_URL": "https://rss.example.com/#top"},
		"public URL bare ?":       {"KIPPLE_PUBLIC_URL": "https://rss.example.com?"},
		"public URL user info":    {"KIPPLE_PUBLIC_URL": "https://u:p@rss.example.com"},
		"public URL space":        {"KIPPLE_PUBLIC_URL": "https://rss.example.com /x"},
		"public URL trailing":     {"KIPPLE_PUBLIC_URL": "https://rss.example.com "},
		"public URL relative":     {"KIPPLE_PUBLIC_URL": "/rss"},
	}

	for name, envMap := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := load(env(envMap))
			require.Error(t, err)
		})
	}
}

func TestLoadAcceptsPublicURLsAndMinimumTick(t *testing.T) {
	for _, u := range []string{"https://rss.example.com", "https://rss.example.com/", "http://192.0.2.10:8080", "https://example.com/kipple"} {
		cfg, err := load(env(map[string]string{"KIPPLE_PUBLIC_URL": u}))
		require.NoError(t, err, u)
		require.Equal(t, u, cfg.PublicURL)
	}
	cfg, err := load(env(map[string]string{"KIPPLE_SCHED_TICK": "1s"}))
	require.NoError(t, err)
	require.Equal(t, time.Second, cfg.SchedTick)
}

// A trusted proxy written in IPv4-mapped form is unmapped, as the peer address
// it is compared with is (auth.ClientIP, auth.PeerTrusted).
func TestTrustedProxiesAreUnmapped(t *testing.T) {
	cfg, err := load(env(map[string]string{"KIPPLE_TRUSTED_PROXY_IPS": "::ffff:192.0.2.10, 2001:db8::1"}))
	require.NoError(t, err)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("2001:db8::1/128")}, cfg.TrustedProxyIPs)
}

func TestTrustedProxiesAcceptRanges(t *testing.T) {
	cfg, err := load(env(map[string]string{"KIPPLE_TRUSTED_PROXY_IPS": "172.16.0.0/12, 192.0.2.10"}))
	require.NoError(t, err)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.2.10/32")}, cfg.TrustedProxyIPs)
}

func TestLoadRejectsBadAllowedHosts(t *testing.T) {
	for _, v := range []string{"https://rss.example.com", "rss.example.com:443", "*", "a b", "*.", "exa%6dple.com"} {
		_, err := load(env(map[string]string{"KIPPLE_ALLOWED_HOSTS": v}))
		require.ErrorContains(t, err, "KIPPLE_ALLOWED_HOSTS", v)
	}
}
