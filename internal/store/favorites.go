package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// NormalizeFavoriteID parses a favorite's id string as a positive int64 and
// returns its canonical decimal form: "007" becomes "7"; "0", "-1", "+5", "",
// non-digits and values above int64 are rejected. One id has exactly one
// spelling, so the duplicate check and dropFavorite agree.
func NormalizeFavoriteID(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return "", false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return "", false
	}
	return strconv.FormatInt(n, 10), true
}

// normalizeFavorites rewrites a stored list with canonical ids and drops
// entries that are malformed or repeat an earlier (t, id).
func normalizeFavorites(list []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	seen := make(map[[2]string]bool, len(list))
	for _, f := range list {
		t, _ := f["t"].(string)
		s, _ := f["id"].(string)
		id, ok := NormalizeFavoriteID(s)
		if !ok || (t != FavFolder && t != FavFeed) || seen[[2]string{t, id}] {
			continue
		}
		seen[[2]string{t, id}] = true
		out = append(out, map[string]any{"t": t, "id": id})
	}
	return out
}

// dropFavorite removes the favorite of one deleted folder or feed, inside the
// deleting transaction, so the stored list never names a dead id. Ids compare
// in canonical form, so a legacy "007" is cleaned like "7". A missing or
// unparsable row is left alone (nothing to fix or nothing safe to rewrite).
func dropFavorite(ctx context.Context, tx *sql.Tx, kind string, id int64) error {
	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", SettingFavorites).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
		if s, _ := f["id"].(string); f["t"] == kind {
			if n, ok := NormalizeFavoriteID(s); ok && n == want {
				continue
			}
		}
		keep = append(keep, f)
	}
	if len(keep) == len(list) {
		return nil
	}
	b, err := json.Marshal(normalizeFavorites(keep))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE settings SET value = ?, updated_at = unixepoch() WHERE key = ?", string(b), SettingFavorites)
	return err
}

// readFavorites normalizes a decoded library.favorites value for display; a
// value that is not a list of objects is returned unchanged.
func readFavorites(v any) any {
	arr, ok := v.([]any)
	if !ok {
		return v
	}
	list := make([]map[string]any, 0, len(arr))
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			return v
		}
		list = append(list, m)
	}
	out := make([]any, 0, len(list))
	for _, f := range normalizeFavorites(list) {
		out = append(out, f)
	}
	return out
}
