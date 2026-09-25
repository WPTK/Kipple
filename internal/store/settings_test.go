package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

func TestRestoreDaysClamped(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"365", MaxRestoreDays},
		{"181", MaxRestoreDays},
		{"180", 180},
		{"90", 90},
		{"0", 0},
		{"-5", 0},
	} {
		e.exec(`INSERT INTO settings(key, value) VALUES ('retention.restore_days', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, tc.raw)
		got := LoadFetchSettings(e.ctx, e.db.writer).RestoreDays
		require.Equal(t, tc.want, got, tc.raw)
	}
}

func TestResolveUserAgent(t *testing.T) {
	mk := func(mode, custom string) FetchSettings { return FetchSettings{UAMode: mode, UserAgent: custom} }
	for _, tc := range []struct {
		name         string
		set          FetchSettings
		feed         string
		fallback     bool
		wantUA, want string
	}{
		{"default mode", mk(UAModeDefault, ""), "", false, "", ""},
		{"default mode ignores stored fallback", mk(UAModeDefault, ""), "", true, "", ""},
		{"on failure, fresh feed", mk(UAModeOnFailure, ""), "", false, "", fetch.BrowserUserAgent},
		{"on failure, remembered", mk(UAModeOnFailure, ""), "", true, fetch.BrowserUserAgent, ""},
		{"always", mk(UAModeAlways, ""), "", false, fetch.BrowserUserAgent, ""},
		{"custom replaces browser string", mk(UAModeAlways, "Mine/1"), "", false, "Mine/1", ""},
		{"custom on failure", mk(UAModeOnFailure, "Mine/1"), "", false, "", "Mine/1"},
		{"feed override beats all", mk(UAModeAlways, "Mine/1"), "Feed/2", true, "Feed/2", ""},
		{"feed override never retries", mk(UAModeOnFailure, ""), "Feed/2", false, "Feed/2", ""},
	} {
		ua, retry := ResolveUserAgent(tc.set, tc.feed, tc.fallback)
		require.Equal(t, tc.wantUA, ua, tc.name)
		require.Equal(t, tc.want, retry, tc.name)
	}
}
