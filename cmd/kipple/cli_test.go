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

// The refusals of the web import hold for the command line too: a file of folders only, an empty file and a
// file naming an undefined entity are refused, with the server running and nothing created (issue #518).
func TestCLIImportRefusesWhatTheWebImportRefuses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KIPPLE_DATA", dir)
	server, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	for name, tc := range map[string]struct{ body, want string }{
		"folders only": {`<opml><body><outline text="Empty"/></body></opml>`, "no feeds"},
		"empty":        {``, "empty"},
		"entity":       {`<opml><body><outline text="&x;"><outline xmlUrl="https://a.test/f"/></outline></body></opml>`, "&x;"},
	} {
		f := filepath.Join(dir, "in.opml")
		require.NoError(t, os.WriteFile(f, []byte(tc.body), 0o600))
		require.ErrorContains(t, runImport([]string{f}), tc.want, name)
	}
	var n int
	require.NoError(t, server.Reader().QueryRow("SELECT count(*) FROM folders WHERE name = 'Empty'").Scan(&n))
	require.Zero(t, n)
}
