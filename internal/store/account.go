package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Account is the single account row (design §2.2). APIPasswordHash is empty
// when the Reader API is disabled.
type Account struct {
	Username        string
	PasswordHash    string
	APIPasswordHash string
	Secret          string
}

// Account loads the account row; ok is false before first-start setup.
func (d *DB) Account(ctx context.Context) (a Account, ok bool, err error) {
	var api sql.NullString
	err = d.reader.QueryRowContext(ctx,
		"SELECT username, password_hash, api_password_hash, secret FROM account WHERE id = 1").
		Scan(&a.Username, &a.PasswordHash, &api, &a.Secret)
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
// inserted one; an existing account is never modified.
func (d *DB) CreateAccount(ctx context.Context, a Account) (created bool, err error) {
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var api any
		if a.APIPasswordHash != "" {
			api = a.APIPasswordHash
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO account (id, username, password_hash, api_password_hash, secret)
			VALUES (1, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`, a.Username, a.PasswordHash, api, a.Secret)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		created = n > 0
		return err
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

// SetPasswordHash replaces the web password hash and, in the same transaction,
// deletes every session except keepSession (the caller's), so a changed
// password signs out every other browser.
func (d *DB) SetPasswordHash(ctx context.Context, hash, keepSession string) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE account SET password_hash = ?, updated_at = unixepoch() WHERE id = 1", hash)
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
