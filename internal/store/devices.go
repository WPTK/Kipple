package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
)

// Per-device appearance profiles (backend-additions-round2 section 4): one row per
// browser or installed app, keyed by the HttpOnly kipple_device cookie value. The
// settings column holds overrides only. The cookie grants nothing; it selects a profile.

const (
	// MaxDevices is the row cap; the least recently seen device is evicted above it.
	MaxDevices = 50
	// DeviceMaxAgeDays is how long an unseen device is kept (the cookie's Max-Age).
	DeviceMaxAgeDays = 400
	// MaxDeviceProfileBytes is the size limit of the stored overrides (also a CHECK in 0004).
	MaxDeviceProfileBytes = 8192
	// DeviceTouchInterval is the minimum gap between last_seen_at updates, in seconds.
	DeviceTouchInterval = 24 * 60 * 60
)

// ErrDeviceProfileTooLarge is returned when a profile would exceed MaxDeviceProfileBytes.
var ErrDeviceProfileTooLarge = errors.New("store: device profile too large")

// ErrDeviceNotFound is returned for an unknown device id.
var ErrDeviceNotFound = errors.New("store: no such device")

var deviceIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,32}$`)

// ValidDeviceID reports whether s has the shape of a device id (a cookie value is
// checked with this before it reaches SQL; it is not a lookup).
func ValidDeviceID(s string) bool { return deviceIDRe.MatchString(s) }

// Device is one stored profile.
type Device struct {
	ID         string
	Name       string
	Profile    map[string]any
	UserAgent  string
	Client     string
	CreatedAt  int64
	LastSeenAt int64
}

const deviceCols = "id, name, settings, user_agent, client, created_at, last_seen_at"

func scanDevice(sc interface{ Scan(...any) error }) (Device, error) {
	var dv Device
	var raw string
	if err := sc.Scan(&dv.ID, &dv.Name, &raw, &dv.UserAgent, &dv.Client, &dv.CreatedAt, &dv.LastSeenAt); err != nil {
		return dv, err
	}
	dv.Profile = map[string]any{}
	if json.Unmarshal([]byte(raw), &dv.Profile) != nil || dv.Profile == nil {
		dv.Profile = map[string]any{}
	}
	return dv, nil
}

// GetDevice returns one device. ok is false for an unknown id.
func (d *DB) GetDevice(ctx context.Context, id string) (Device, bool, error) {
	dv, err := scanDevice(d.reader.QueryRowContext(ctx, "SELECT "+deviceCols+" FROM devices WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, false, nil
	}
	return dv, err == nil, err
}

// ListDevices lists devices, most recently seen first.
func (d *DB) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := d.reader.QueryContext(ctx, "SELECT "+deviceCols+" FROM devices ORDER BY last_seen_at DESC, created_at DESC, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		dv, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dv)
	}
	return out, rows.Err()
}

// TouchDevice loads a registered device, bumping last_seen_at (and the user agent and
// client) when the stored value is a day old. found is false for an unknown id.
// touched is true when the row was bumped (the caller slides the cookie).
func (d *DB) TouchDevice(ctx context.Context, id, userAgent, client string, now int64) (dv Device, found, touched bool, err error) {
	client = deviceClient(client)
	ua := truncate(userAgent, 300)
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		found, touched = false, false
		row, e := scanDevice(tx.QueryRowContext(ctx, "SELECT "+deviceCols+" FROM devices WHERE id = ?", id))
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		} else if e != nil {
			return e
		}
		dv, found = row, true
		if now-dv.LastSeenAt < DeviceTouchInterval {
			return nil
		}
		if _, e = tx.ExecContext(ctx, "UPDATE devices SET last_seen_at = ?, user_agent = ?, client = ? WHERE id = ?",
			now, ua, client, id); e != nil {
			return e
		}
		dv.LastSeenAt, dv.UserAgent, dv.Client, touched = now, ua, client, true
		return nil
	})
	return dv, found, touched, err
}

func deviceClient(c string) string {
	if c == "pwa" {
		return c
	}
	return "web"
}

// RegisterDevice creates a device with an empty profile and evicts the least recently
// seen ones above MaxDevices (never the new one). The id must come from the server.
func (d *DB) RegisterDevice(ctx context.Context, id, userAgent, client string, now int64) (Device, error) {
	dv := Device{ID: id, Profile: map[string]any{}, UserAgent: truncate(userAgent, 300), Client: deviceClient(client), CreatedAt: now, LastSeenAt: now}
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `INSERT INTO devices (id, name, settings, user_agent, client, created_at, last_seen_at)
			VALUES (?, '', '{}', ?, ?, ?, ?)`, id, dv.UserAgent, dv.Client, now, now); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `DELETE FROM devices WHERE id IN (
			SELECT id FROM devices WHERE id != ?1 ORDER BY last_seen_at DESC, created_at DESC, id
			LIMIT -1 OFFSET ?2)`, id, MaxDevices-1)
		return e
	})
	return dv, err
}

func marshalProfile(p map[string]any) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	if len(b) > MaxDeviceProfileBytes {
		return "", ErrDeviceProfileTooLarge
	}
	return string(b), nil
}

// PatchDeviceProfile merges set into the device's overrides (a nil value removes
// the key) in one transaction and returns the new profile. The caller validates
// keys and values.
func (d *DB) PatchDeviceProfile(ctx context.Context, id string, set map[string]any) (map[string]any, error) {
	var out map[string]any
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		dv, e := scanDevice(tx.QueryRowContext(ctx, "SELECT "+deviceCols+" FROM devices WHERE id = ?", id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrDeviceNotFound
		} else if e != nil {
			return e
		}
		for k, v := range set {
			if v == nil {
				delete(dv.Profile, k)
			} else {
				dv.Profile[k] = v
			}
		}
		s, e := marshalProfile(dv.Profile)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE devices SET settings = ? WHERE id = ?", s, id); e != nil {
			return e
		}
		out = dv.Profile
		return nil
	})
	return out, err
}

// ReplaceDeviceProfile replaces the device's overrides wholesale.
func (d *DB) ReplaceDeviceProfile(ctx context.Context, id string, profile map[string]any) error {
	s, err := marshalProfile(profile)
	if err != nil {
		return err
	}
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, e := tx.ExecContext(ctx, "UPDATE devices SET settings = ? WHERE id = ?", s, id)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrDeviceNotFound
		}
		return nil
	})
}

// SetDeviceName renames a device (at most 64 characters; the caller trims).
func (d *DB) SetDeviceName(ctx context.Context, id, name string) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, e := tx.ExecContext(ctx, "UPDATE devices SET name = ? WHERE id = ?", name, id)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrDeviceNotFound
		}
		return nil
	})
}

// DeleteDevice removes a device; ok is false for an unknown id.
func (d *DB) DeleteDevice(ctx context.Context, id string) (bool, error) {
	var n int64
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, e := tx.ExecContext(ctx, "DELETE FROM devices WHERE id = ?", id)
		if e != nil {
			return e
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n > 0, err
}

// PurgeDevices deletes up to limit devices unseen for DeviceMaxAgeDays.
func (d *DB) PurgeDevices(ctx context.Context, now int64, limit int) (int64, error) {
	return d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE id IN (
			SELECT id FROM devices WHERE last_seen_at < ?1 LIMIT ?2)`, now-int64(DeviceMaxAgeDays)*86400, limit)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
}
