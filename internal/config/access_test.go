package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccessOffByDefault(t *testing.T) {
	cfg, err := load(env(nil))
	require.NoError(t, err)
	require.Equal(t, "", cfg.ReachSeed().AccessTeam)
	require.Empty(t, cfg.AccessTeamDomain)
	require.Empty(t, cfg.AccessAUD)
}

func TestAccessBothSet(t *testing.T) {
	for _, team := range []string{"myteam.cloudflareaccess.com", "https://MyTeam.cloudflareaccess.com/", " myteam.cloudflareaccess.com "} {
		cfg, err := load(env(map[string]string{"KIPPLE_ACCESS_TEAM_DOMAIN": team, "KIPPLE_ACCESS_AUD": " abc123 "}))
		require.NoError(t, err, team)
		require.Equal(t, "myteam.cloudflareaccess.com", cfg.ReachSeed().AccessTeam)
		require.Equal(t, "myteam.cloudflareaccess.com", cfg.AccessTeamDomain)
		require.Equal(t, "abc123", cfg.AccessAUD)
	}
}

func TestAccessHalfSetStopsStartup(t *testing.T) {
	_, err := load(env(map[string]string{"KIPPLE_ACCESS_TEAM_DOMAIN": "myteam.cloudflareaccess.com"}))
	require.ErrorContains(t, err, "KIPPLE_ACCESS_AUD")
	_, err = load(env(map[string]string{"KIPPLE_ACCESS_AUD": "abc"}))
	require.ErrorContains(t, err, "KIPPLE_ACCESS_TEAM_DOMAIN")
}

func TestAccessBadValues(t *testing.T) {
	for _, team := range []string{"http://myteam.cloudflareaccess.com", "myteam.cloudflareaccess.com/cdn-cgi", "myteam.cloudflareaccess.com:443", "user@myteam.cloudflareaccess.com", "localhost", "my team.example"} {
		_, err := load(env(map[string]string{"KIPPLE_ACCESS_TEAM_DOMAIN": team, "KIPPLE_ACCESS_AUD": "abc"}))
		require.ErrorContains(t, err, "KIPPLE_ACCESS_TEAM_DOMAIN", team)
	}
	_, err := load(env(map[string]string{"KIPPLE_ACCESS_TEAM_DOMAIN": "myteam.cloudflareaccess.com", "KIPPLE_ACCESS_AUD": "a b"}))
	require.ErrorContains(t, err, "KIPPLE_ACCESS_AUD")
}
