package store

import (
	"context"
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
	require.Contains(t, msg, "last opened by Kipple v0.9.0 (schema 99)")
	require.Contains(t, msg, "this is Kipple v0.3.0")
	require.Contains(t, msg, "Run v0.9.0 or newer")
	require.Contains(t, msg, "pre-migration snapshot")
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
	require.Contains(t, err.Error(), "written by a newer Kipple than this binary")
	require.NotContains(t, err.Error(), "last opened by")
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
