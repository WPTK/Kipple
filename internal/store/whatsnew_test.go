package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A new account starts out as having seen the running version's "What's new", so only an upgrade shows it; an
// account that already exists is not touched, and a development build records nothing.
func TestCreateAccountRecordsTheRunningVersionAsSeen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct{ version, want string }{{"v1.0.0", "v1.0.0"}, {"1.0.0-rc.1", "1.0.0-rc.1"}, {"v1.0.0-rc.1+build.5", "v1.0.0-rc.1+build.5"}, {"dev", ""}, {"", ""}, {"3f9c2ab", ""}, {"v1.0.0-" + strings.Repeat("a", 70), ""}} {
		d, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "kipple.db"), Version: tc.version})
		require.NoError(t, err)
		created, err := d.CreateAccount(ctx, Account{Username: "owner", PasswordHash: "h", Secret: strings.Repeat("ab", 32), CreatedVia: CreatedViaWizard})
		require.NoError(t, err)
		require.True(t, created)
		require.Equal(t, tc.want, settingString(ctx, d.reader, SettingWhatsNewSeen, ""), "version %q", tc.version)
		require.NoError(t, d.Close())
	}

	path := filepath.Join(t.TempDir(), "kipple.db")
	d, err := Open(ctx, Options{Path: path, Version: "v1.0.0"})
	require.NoError(t, err)
	_, err = d.CreateAccount(ctx, Account{Username: "owner", PasswordHash: "h", Secret: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	require.NoError(t, d.Close())
	d, err = Open(ctx, Options{Path: path, Version: "v1.1.0"}) // the upgrade
	require.NoError(t, err)
	created, err := d.CreateAccount(ctx, Account{Username: "other", PasswordHash: "h", Secret: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "v1.0.0", settingString(ctx, d.reader, SettingWhatsNewSeen, ""))
	require.NoError(t, d.Close())
}
