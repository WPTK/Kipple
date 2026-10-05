package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A database newer than the binary is still refused, and now says which Kipple last opened it and what to do.
func TestDowngradeMessageNamesTheNewerVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kipple.db")

	d, err := Open(ctx, Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, d.RecordVersion(ctx, "v0.9.0"))
	require.Equal(t, "v0.9.0", d.LastVersion(ctx))
	_, err = d.writer.ExecContext(ctx, "PRAGMA user_version = 99")
	require.NoError(t, err)
	require.NoError(t, d.Close())

	_, err = Open(ctx, Options{Path: path, Version: "v0.3.0"})
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "database schema version 99 is newer than this binary (")
	require.Contains(t, msg, "refusing to start")
	require.Contains(t, msg, "The last Kipple that started on this database is v0.9.0.")
	require.Contains(t, msg, fmt.Sprintf("To run Kipple v0.3.0 (schema %[1]d), restore the newest pre-migration-<from>-<to>-<time>.db whose <from> is at most %[1]d", latestSchema(t)))
	require.Contains(t, msg, "Otherwise run v0.9.0 or newer.")
}

// An upgrade that failed partway leaves the schema ahead of the version on record (the newer binary never started,
// so it never recorded itself). The refusal then names the snapshot by this binary's schema and does not tell the
// person to run the version they are running.
func TestDowngradeMessageAfterAPartialUpgrade(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "kipple.db")
	d, err := Open(ctx, Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, d.RecordVersion(ctx, "v0.3.0"))
	_, err = d.writer.ExecContext(ctx, "PRAGMA user_version = 99")
	require.NoError(t, err)
	require.NoError(t, d.Close())
	// An upgrade from two schemas below this binary's that stopped past it, restarted once from one above it, and an
	// older upgrade's snapshot: only the first can be opened by this binary and is the newest such.
	l := latestSchema(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "backup"), 0o700))
	for _, name := range []string{
		fmt.Sprintf("pre-migration-%d-99-100.db", l-2),
		fmt.Sprintf("pre-migration-%d-99-200.db", l+1),
		fmt.Sprintf("pre-migration-%d-99-300.db", l-5),
		fmt.Sprintf("pre-migration-%d-%d-400.db", l-3, l),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "backup", name), nil, 0o600))
	}

	_, err = Open(ctx, Options{Path: path, Version: "v0.3.0"})
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, fmt.Sprintf("restore pre-migration-%d-99-100.db from the backup folder", l-2))
	require.NotContains(t, msg, "run v0.3.0", "never the version being run")
	require.Contains(t, msg, "Otherwise run a Kipple whose schema is 99 or newer.")
}

// With no version on record (a database from before Kipple wrote one) the refusal still explains itself.
func TestDowngradeMessageWithoutARecordedVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kipple.db")
	d, err := Open(ctx, Options{Path: path})
	require.NoError(t, err)
	_, err = d.writer.ExecContext(ctx, "PRAGMA user_version = 99")
	require.NoError(t, err)
	require.NoError(t, d.Close())

	_, err = Open(ctx, Options{Path: path})
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to start")
	require.Contains(t, err.Error(), "To run this binary (schema ")
	require.NotContains(t, err.Error(), "The last Kipple")
}

// latestSchema is the schema this binary migrates to.
func latestSchema(t *testing.T) int {
	t.Helper()
	ms, err := loadMigrations()
	require.NoError(t, err)
	return len(ms)
}

// A development build never overwrites the version a real release recorded.
func TestRecordVersionIgnoresDev(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "kipple.db")})
	require.NoError(t, err)
	defer d.Close()
	require.NoError(t, d.RecordVersion(ctx, "v0.4.0"))
	require.NoError(t, d.RecordVersion(ctx, "dev"))
	require.NoError(t, d.RecordVersion(ctx, ""))
	require.Equal(t, "v0.4.0", d.LastVersion(ctx))
	require.NotEmpty(t, d.SQLiteVersion(ctx))
}
