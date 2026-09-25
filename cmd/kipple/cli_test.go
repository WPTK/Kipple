package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

func TestCLIRefusesToCreateOrMigrateTheDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KIPPLE_DATA", dir)
	opmlFile := filepath.Join(dir, "in.opml")
	require.NoError(t, os.WriteFile(opmlFile, []byte(`<opml><body><outline text="A" xmlUrl="http://a.test/rss"/></body></opml>`), 0o600))

	// no database yet: both subcommands refuse and leave nothing behind
	require.ErrorContains(t, runAPIPassword(nil), "start `kipple serve`")
	require.ErrorContains(t, runImport([]string{opmlFile}), "start `kipple serve`")
	_, err := os.Stat(filepath.Join(dir, "kipple.db"))
	require.True(t, os.IsNotExist(err), "a CLI never creates the database")

	// once serve has initialised it, import works and leaves the server's WAL alone
	server, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	require.NoError(t, runImport([]string{opmlFile}))
	st, err := os.Stat(filepath.Join(dir, "kipple.db-wal"))
	require.NoError(t, err)
	require.Positive(t, st.Size(), "the CLI's close did not truncate the WAL")
	var n int
	require.NoError(t, server.Reader().QueryRow("SELECT count(*) FROM feeds WHERE url = 'http://a.test/rss'").Scan(&n))
	require.Equal(t, 1, n)
}
