package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// Account auth modes (account.auth_mode, migration 0010). AuthStandard is a web
// password, or none with Cloudflare Access (design §7.0); AuthOpen is no
// password at all, reachable only through the open gate.
const (
	AuthStandard = "standard"
	AuthOpen     = "open"
)

// How the account row was created (account.created_via).
const (
	CreatedViaEnv    = "env"
	CreatedViaWizard = "wizard"
)

// SettingSetupCompleted is the instant onboarding finished (or was skipped), as
// unix seconds. Absent means the signed-in app routes to the onboarding steps.
const SettingSetupCompleted = "sys.setup_completed_at"

// Account is the single account row (design §2.2). APIPasswordHash is empty
// when the Reader API is disabled.
type Account struct {
	Username        string
	PasswordHash    string
	APIPasswordHash string
	Secret          string
	// AuthMode is AuthStandard or AuthOpen; empty means AuthStandard on create.
	AuthMode string
	// CreatedVia is CreatedViaEnv or CreatedViaWizard; empty means CreatedViaEnv
	// on create.
	CreatedVia string
}

// Account loads the account row; ok is false before first-start setup.
func (d *DB) Account(ctx context.Context) (a Account, ok bool, err error) {
	var api sql.NullString
	err = d.reader.QueryRowContext(ctx,
		"SELECT username, password_hash, api_password_hash, secret, auth_mode, created_via FROM account WHERE id = 1").
		Scan(&a.Username, &a.PasswordHash, &api, &a.Secret, &a.AuthMode, &a.CreatedVia)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	a.APIPasswordHash = api.String
	return a, true, nil
}

// CreateAccount inserts the account row if none exists. It reports whether it
// inserted one; an existing account is never modified. Two concurrent calls
// have exactly one winner (ON CONFLICT DO NOTHING under the single writer). An
// account created from the environment is stamped as set up in the same
// transaction, so scripted deploys never see onboarding.
func (d *DB) CreateAccount(ctx context.Context, a Account) (created bool, err error) {
	mode, via := a.AuthMode, a.CreatedVia
	if mode == "" {
		mode = AuthStandard
	}
	if via == "" {
		via = CreatedViaEnv
	}
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var api any
		if a.APIPasswordHash != "" {
			api = a.APIPasswordHash
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO account (id, username, password_hash, api_password_hash, secret, auth_mode, created_via)
			VALUES (1, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`, a.Username, a.PasswordHash, api, a.Secret, mode, via)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		created = n > 0
		if !created {
			return nil
		}
		if via == CreatedViaEnv {
			return stampSetupCompletedTx(ctx, tx, d.clock.Now().Unix())
		}
		return nil
	})
	return created, err
}

// SetAPIPasswordHash replaces the Reader API password hash (an empty hash
// disables the API). Because the Reader token is derived from the hash, this
// revokes every signed-in client.
func (d *DB) SetAPIPasswordHash(ctx context.Context, hash string) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var v any
		if hash != "" {
			v = hash
		}
		res, err := tx.ExecContext(ctx, "UPDATE account SET api_password_hash = ?, updated_at = unixepoch() WHERE id = 1", v)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("store: no account row")
		}
		return nil
	})
}

// SetPasswordHash replaces the web password hash and the auth mode (AuthOpen
// requires an empty hash: the table CHECK refuses anything else) and, in the
// same transaction, deletes every session except keepSession (the caller's),
// so a changed password or mode signs out every other browser.
func (d *DB) SetPasswordHash(ctx context.Context, hash, mode, keepSession string) error {
	if mode != AuthStandard && mode != AuthOpen {
		return fmt.Errorf("store: unknown auth mode %q", mode)
	}
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE account SET password_hash = ?, auth_mode = ?, updated_at = unixepoch() WHERE id = 1", hash, mode)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("store: no account row")
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE id <> ?", keepSession)
		return err
	})
}

// AccountSecret is the account secret alone: a single-row point read cheap
// enough for every image request, so a rotation by `kipple password` (another
// process) is seen at once.
func (d *DB) AccountSecret(ctx context.Context) (secret string, ok bool, err error) {
	err = d.reader.QueryRowContext(ctx, "SELECT secret FROM account WHERE id = 1").Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return secret, err == nil, err
}

func stampSetupCompletedTx(ctx context.Context, tx *sql.Tx, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`,
		SettingSetupCompleted, strconv.FormatInt(now, 10))
	return err
}

// SetupPending reports whether onboarding has not been finished or skipped yet
// (no sys.setup_completed_at row). A failed read is an error, not "pending".
func (d *DB) SetupPending(ctx context.Context) (bool, error) {
	_, ok, err := settingRawErr(ctx, d.reader, SettingSetupCompleted)
	return !ok, err
}

// CompleteOnboarding records that onboarding finished (or was skipped). It is
// idempotent: an existing stamp is kept.
func (d *DB) CompleteOnboarding(ctx context.Context) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return stampSetupCompletedTx(ctx, tx, d.clock.Now().Unix())
	})
}

// RestartOnboarding clears sys.setup_completed_at ("Run setup again"), which
// routes the signed-in app back to the onboarding steps. It touches nothing
// else: not the account, not setup mode.
func (d *DB) RestartOnboarding(ctx context.Context) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", SettingSetupCompleted)
		return err
	})
}
