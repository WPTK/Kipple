package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// undo0010 turns a current database into a schema-9 one: the account table of
// 0001 (no auth_mode, no created_via) and none of the rows 0010 stamps.
const undo0010 = `CREATE TABLE account_old (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  username          TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 64
                                         AND username NOT GLOB '*[^A-Za-z0-9._-]*'),
  password_hash     TEXT NOT NULL,
  api_password_hash TEXT,
  secret            TEXT NOT NULL CHECK (length(secret) = 64),
  created_at        INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at        INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;
INSERT INTO account_old SELECT id, username, password_hash, api_password_hash, secret, created_at, updated_at FROM account;
DROP TABLE account;
ALTER TABLE account_old RENAME TO account;
DELETE FROM settings WHERE key IN ('sys.setup_completed_at', 'sys.legacy_port');
PRAGMA user_version = 9`

// A fixture account key (64 hex characters), not a real one.
var fixtureKey = strings.Repeat("0a", 32)

// schema9 builds a schema-9 database at a fresh path: with an account when
// withAccount, and with a tz row when tz is not empty. It returns the path.
func schema9(t *testing.T, withAccount bool, tz string) string {
	t.Helper()
	e := newEnv(t)
	e.exec(undo0010)
	if withAccount {
		e.exec(`INSERT INTO account (id, username, password_hash, api_password_hash, secret, created_at, updated_at)
			VALUES (1, 'owner', 'web-hash', 'api-hash', ?, 100, 200)`, fixtureKey)
	}
	if tz != "" {
		e.exec(`INSERT INTO settings (key, value) VALUES ('tz', json_quote(?))`, tz)
	}
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())
	return path
}

func reopen(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(context.Background(), Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	v, err := db.Version(context.Background())
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
	requireCleanIntegrity(t, db.Reader())
	return db
}

func settingRow(t *testing.T, db *DB, key string) (string, bool) {
	t.Helper()
	raw, ok, err := settingRawErr(context.Background(), db.Reader(), key)
	require.NoError(t, err)
	return string(raw), ok
}

// An existing install (an account before 0010) keeps its row and hashes, and is
// stamped: onboarding done, the legacy port, and its old default zone.
func TestMigration0010ExistingAccount(t *testing.T) {
	ctx := context.Background()
	db := reopen(t, schema9(t, true, ""))
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(db.path), "backup", "pre-migration-9-*.db"))
	require.Len(t, snaps, 1, "a pre-migration snapshot")

	acct, ok, err := db.Account(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, Account{Username: "owner", PasswordHash: "web-hash", APIPasswordHash: "api-hash", Secret: fixtureKey,
		AuthMode: AuthStandard, CreatedVia: CreatedViaEnv}, acct)
	require.Equal(t, 100, scalar[int](t, db.Reader(), "SELECT created_at FROM account"))
	require.Equal(t, 200, scalar[int](t, db.Reader(), "SELECT updated_at FROM account"))

	pending, err := db.SetupPending(ctx)
	require.NoError(t, err)
	require.False(t, pending, "an existing account never sees onboarding")
	legacy, ok := settingRow(t, db, "sys.legacy_port")
	require.True(t, ok)
	require.Equal(t, "true", legacy, "the row is still stamped; nothing reads it since 0.6.0")
	tz, ok := settingRow(t, db, "tz")
	require.True(t, ok)
	require.Equal(t, `"America/New_York"`, tz)
	require.Equal(t, "America/New_York", Zone(ctx, db.Reader()).String())
}

// A time zone chosen before the upgrade is left exactly as it was.
func TestMigration0010KeepsAnExistingTZ(t *testing.T) {
	db := reopen(t, schema9(t, true, "Europe/Paris"))
	tz, _ := settingRow(t, db, "tz")
	require.Equal(t, `"Europe/Paris"`, tz)
}

// A schema-9 database without an account was never usable: it is treated as
// fresh (no stamps, UTC, the new port, setup pending).
func TestMigration0010WithoutAccount(t *testing.T) {
	ctx := context.Background()
	db := reopen(t, schema9(t, false, ""))
	_, ok, err := db.Account(ctx)
	require.NoError(t, err)
	require.False(t, ok)
	for _, k := range []string{"sys.setup_completed_at", "sys.legacy_port", "tz"} {
		_, ok := settingRow(t, db, k)
		require.False(t, ok, k)
	}
	require.Equal(t, "UTC", Zone(ctx, db.Reader()).String())
}

// The table CHECK keeps open mode passwordless, and the enums closed.
func TestAccountChecks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.db.CreateAccount(ctx, Account{Username: "owner", PasswordHash: "h", Secret: fixtureKey, AuthMode: AuthOpen})
	require.Error(t, err, "open mode with a password hash")
	_, err = e.db.CreateAccount(ctx, Account{Username: "owner", PasswordHash: "", Secret: fixtureKey, AuthMode: "sideways"})
	require.Error(t, err)
	_, err = e.db.CreateAccount(ctx, Account{Username: "owner", PasswordHash: "", Secret: fixtureKey, CreatedVia: "carrier-pigeon"})
	require.Error(t, err)

	created, err := e.db.CreateAccount(ctx, Account{Username: "owner", Secret: fixtureKey, AuthMode: AuthOpen, CreatedVia: CreatedViaWizard})
	require.NoError(t, err)
	require.True(t, created)
	pending, err := e.db.SetupPending(ctx)
	require.NoError(t, err)
	require.True(t, pending, "a wizard account goes through onboarding")
	require.Error(t, e.db.SetPasswordHash(ctx, "h", AuthOpen, ""), "the CHECK refuses a hash in open mode")
	require.NoError(t, e.db.SetPasswordHash(ctx, "h", AuthStandard, ""))
	a, _, _ := e.db.Account(ctx)
	require.Equal(t, AuthStandard, a.AuthMode)
	require.NoError(t, e.db.SetPasswordHash(ctx, "", AuthOpen, ""))
	// `kipple password` (ResetPassword) always leaves open mode.
	require.NoError(t, e.db.ResetPassword(ctx, "h2", fixtureKey))
	a, _, _ = e.db.Account(ctx)
	require.Equal(t, AuthStandard, a.AuthMode)
	require.Equal(t, "h2", a.PasswordHash)
}

// An env-created account is stamped as set up in the same transaction.
func TestEnvAccountIsSetUp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	created, err := e.db.CreateAccount(ctx, Account{Username: "owner", PasswordHash: "h", Secret: fixtureKey})
	require.NoError(t, err)
	require.True(t, created)
	pending, err := e.db.SetupPending(ctx)
	require.NoError(t, err)
	require.False(t, pending)
	again, err := e.db.CreateAccountWith(ctx, Account{Username: "other", PasswordHash: "x", Secret: fixtureKey},
		map[string]any{SettingOpenLAN: true})
	require.NoError(t, err)
	require.False(t, again, "an existing row is never replaced")
	_, ok := settingRow(t, e.db, SettingOpenLAN)
	require.False(t, ok, "the loser's settings are not written either")
}

// Onboarding restart clears the stamp and nothing else; complete is idempotent.
func TestOnboardingStamp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.db.CreateAccount(ctx, Account{Username: "owner", PasswordHash: "h", Secret: fixtureKey})
	require.NoError(t, err)
	before := e.count("SELECT count(*) FROM settings")
	require.NoError(t, e.db.RestartOnboarding(ctx))
	require.Equal(t, before-1, e.count("SELECT count(*) FROM settings"))
	p, _ := e.db.SetupPending(ctx)
	require.True(t, p)
	require.NoError(t, e.db.CompleteOnboarding(ctx))
	v1, _ := settingRow(t, e.db, SettingSetupCompleted)
	e.clk.Advance(3600e9)
	require.NoError(t, e.db.CompleteOnboarding(ctx))
	v2, _ := settingRow(t, e.db, SettingSetupCompleted)
	require.Equal(t, v1, v2, "idempotent: the first stamp is kept")
	a, _, _ := e.db.Account(ctx)
	require.Equal(t, "owner", a.Username)
}
