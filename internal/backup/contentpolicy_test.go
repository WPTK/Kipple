package backup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// A backup carries its own record of which sanitize policy its stored HTML was cleaned under. It
// comes from another server, so a restore forgets it and the next start cleans every row again.
func TestRestoreForgetsTheBackupsContentPolicyMark(t *testing.T) {
	dir := t.TempDir()
	library(t, dir, false, map[string]any{})
	stagedLibrary(t, dir, map[string]any{store.SettingContentPolicy: 999})
	live := openLive(t, dir)
	require.NoError(t, live.Close())
	done, err := ApplyStaged(dir, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), time.UTC)
	require.NoError(t, err)
	require.True(t, done.Restored)
	restored := openLive(t, dir)
	var n int
	require.NoError(t, restored.Reader().QueryRowContext(context.Background(), `SELECT count(*) FROM settings WHERE key = ?`, store.SettingContentPolicy).Scan(&n))
	require.Zero(t, n)
}
