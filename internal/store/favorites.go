package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
)

// SettingFavorites is library.favorites: the sidebar favorites, a JSON array of
// {"t":"folder"|"feed","id":"<digits>"} (validated by the API, max 500).
const SettingFavorites = "library.favorites"

// Favorite kinds.
const (
	FavFolder = "folder"
	FavFeed   = "feed"
)

// dropFavorite removes the favorite of one deleted folder or feed, inside the
// deleting transaction, so the stored list never names a dead id. A missing or
// unparsable row is left alone (nothing to fix or nothing safe to rewrite).
func dropFavorite(ctx context.Context, tx *sql.Tx, kind string, id int64) error {
	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", SettingFavorites).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	var list []map[string]any
	if json.Unmarshal([]byte(raw), &list) != nil {
		return nil
	}
	want := strconv.FormatInt(id, 10)
	keep := make([]map[string]any, 0, len(list))
	for _, f := range list {
		if f["t"] == kind && f["id"] == want {
			continue
		}
		keep = append(keep, f)
	}
	if len(keep) == len(list) {
		return nil
	}
	b, err := json.Marshal(keep)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE settings SET value = ?, updated_at = unixepoch() WHERE key = ?", string(b), SettingFavorites)
	return err
}
