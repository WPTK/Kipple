package api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealthReportsHostThrottleSnapshotClockAndSizes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	held := h.storeFeed("https://held.example/feed")
	free := h.storeFeed("https://free.example/feed")
	_ = free
	until := h.clk.Now().Add(20 * time.Minute)
	h.sched.holds = map[string]time.Time{"held.example": until}
	h.exec("INSERT INTO settings (key, value) VALUES ('sys.last_snapshot_at', ?)", strconv.FormatInt(h.clk.Now().Unix()-3600, 10))
	h.exec("INSERT INTO settings (key, value) VALUES ('sys.last_snapshot_error', '\"disk full\"')")

	code, body, _ := h.api(c, "GET", "/api/health/feeds", "")
	require.Equal(t, http.StatusOK, code)
	byID := map[string]map[string]any{}
	for _, f := range body["feeds"].([]any) {
		m := f.(map[string]any)
		byID[m["id"].(string)] = m
	}
	require.Equal(t, "throttled", byID[sid(held)]["status"])
	require.EqualValues(t, until.Unix(), byID[sid(held)]["host_throttled_until"])
	require.Equal(t, "ok", byID[sid(free)]["status"])
	require.Nil(t, byID[sid(free)]["host_throttled_until"])
	require.Contains(t, byID[sid(held)], "redirect_pending")
	require.NotContains(t, byID[sid(held)], "migrated")

	require.Contains(t, body, "reader_last_seen_at")
	require.Nil(t, body["reader_last_seen_at"], "no Reader API client has called")
	seen := h.clk.Now().Add(-90 * time.Second)
	h.srv.opt.ReaderLastSeen = func() time.Time { return seen }
	_, withSeen, _ := h.api(c, "GET", "/api/health/feeds", "")
	require.EqualValues(t, seen.Unix(), withSeen["reader_last_seen_at"])
	h.srv.opt.ReaderLastSeen = func() time.Time { return time.Time{} }
	_, unseen, _ := h.api(c, "GET", "/api/health/feeds", "")
	require.Nil(t, unseen["reader_last_seen_at"], "the zero time is never")

	snap := body["snapshot"].(map[string]any)
	require.EqualValues(t, h.clk.Now().Unix()-3600, snap["last_at"])
	require.Equal(t, "disk full", snap["last_error"])
	require.EqualValues(t, 0, body["clock"].(map[string]any)["ahead_s"])
	db := body["db"].(map[string]any)
	require.Greater(t, db["db_bytes"], float64(0))
	for _, k := range []string{"wal_bytes", "backup_bytes", "imgcache_bytes"} {
		require.Contains(t, db, k)
	}

	// The bootstrap carries the same status.
	_, boot, _ := h.api(c, "GET", "/api/bootstrap", "")
	for _, f := range boot["feeds"].([]any) {
		m := f.(map[string]any)
		if m["id"] == sid(held) {
			require.Equal(t, "throttled", m["status"])
		}
	}
}
