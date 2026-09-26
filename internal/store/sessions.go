package store

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"
)

// CreateSession stores a web session under id (the hex sha256 of the cookie
// value; the cookie itself is never stored) and purges expired ones.
func (d *DB) CreateSession(ctx context.Context, id string, now, expires int64, userAgent, remoteIP string) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, created_at, last_seen_at, expires_at, user_agent, remote_ip)
			VALUES (?, ?, ?, ?, ?, ?)`, id, now, now, expires, truncate(userAgent, 300), remoteIP)
		return err
	})
}

// SessionState says what CheckSession found.
type SessionState int

// Session lookup outcomes.
const (
	SessionNone    SessionState = iota // unknown or expired
	SessionValid                       // valid, not touched
	SessionRenewed                     // valid and slid forward: re-issue the cookie
)

// CheckSession looks a session up. When it was last seen more than an hour ago
// the expiry slides to newExpires (design §7: last_seen_at updated at most hourly).
func (d *DB) CheckSession(ctx context.Context, id string, now, newExpires int64) (SessionState, error) {
	var lastSeen, expires int64
	err := d.reader.QueryRowContext(ctx, "SELECT last_seen_at, expires_at FROM sessions WHERE id = ?", id).Scan(&lastSeen, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionNone, nil
	}
	if err != nil {
		return SessionNone, err
	}
	if expires <= now {
		return SessionNone, nil
	}
	if now-lastSeen < 3600 {
		return SessionValid, nil
	}
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?", now, newExpires, id)
		return err
	})
	if err != nil {
		return SessionValid, nil // the read said valid; a failed slide is not a logout
	}
	return SessionRenewed, nil
}

// DeleteSession removes one session (logout).
func (d *DB) DeleteSession(ctx context.Context, id string) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
		return err
	})
}

// truncate cuts s to at most n bytes on a rune boundary, so a stored user
// agent never ends in half a UTF-8 character. Bytes that are not valid UTF-8
// are cut at n (at most utf8.UTFMax-1 bytes are given back looking for a
// boundary).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for i := n; i > 0 && i > n-utf8.UTFMax; i-- {
		if utf8.RuneStart(s[i]) {
			return s[:i]
		}
	}
	return s[:n]
}

// SessionActive reports whether the session exists and has not expired. It is
// read-only (no sliding), for long-lived streams that re-check their session.
func (d *DB) SessionActive(ctx context.Context, id string, now int64) (bool, error) {
	var expires int64
	err := d.reader.QueryRowContext(ctx, "SELECT expires_at FROM sessions WHERE id = ?", id).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && expires > now, err
}
