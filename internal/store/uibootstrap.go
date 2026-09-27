package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
)

// Image proxy defaults. The mode is "all" (every image loads through Kipple, like
// other RSS readers); a stored imgproxy.mode row always wins, so a deployment
// that chose "http_only" keeps it. The cache cap is in MiB; 0 turns the cache off.
const (
	DefaultImgMode    = "all"
	DefaultImgCacheMB = 1024
)

// IntSetting reads an integer setting through the reader pool.
func (d *DB) IntSetting(ctx context.Context, key string, def int) int {
	return settingInt(ctx, d.reader, key, def)
}

// DefaultSettings are the user-visible settings with their defaults (design
// §2.2: a row exists only for an overridden key). System keys (sys.*) are never
// listed.
var DefaultSettings = map[string]any{
	"refresh.interval_minutes":      30,
	"retention.default":             250,
	"retention.restore_days":        90,
	"fetch.user_agent":              "",
	"fetch.user_agent_mode":         UAModeOnFailure,
	"fetch.honor_publisher_ttl":     true,
	"tz":                            "America/New_York",
	"stats.api_single_read_is_open": false,
	"stats.enabled":                 true,
	"stats.week_start":              "sunday",
	"stats.wrapped_enabled":         true,
	"imgproxy.mode":                 DefaultImgMode,
	"imgproxy.cache_mb":             DefaultImgCacheMB,
	"greader.icon_urls":             true,
	"fetch.fulltext_all":            false,
	"library.favorites":             []any{},
	"library.auto_read_days":        0,
	"library.saved_searches":        []any{},
	"links.strip_tracking":          true,

	// Named by design §2.2, read by the Reader API (ot user changes, synchronous first fetch on subscribe).
	"greader.ot_includes_user_changes": false,
	"greader.subscribe_fetch_now":      false,

	// ui.* (design §2.2 names the keys; values and ranges from CLAUDE.md decisions).
	"ui.theme":               "system",
	"ui.theme_day":           "paper",
	"ui.theme_night":         "midnight",
	"ui.theme_night_start":   "21:00",
	"ui.theme_day_start":     "07:00",
	"ui.font_body":           "",
	"ui.font_ui":             "",
	"ui.font_size":           18,
	"ui.reading_density":     "comfortable",
	"ui.list_density":        "standard",
	"ui.layouts":             map[string]any{},
	"ui.mark_read_on_scroll": false,
	// The account defaults of the client-only appearance keys (device profiles,
	// internal/api/devices.go): an object of "client.*" overrides that make-default fills.
	"ui.device_defaults": map[string]any{},
}

// ThemeAliases maps the theme ids of the first UI draft (and the retired Fern and
// Cocoa names) to the ids of the round-2 colour schemes. Stored rows are never
// rewritten: the alias is applied when a value is read.
var ThemeAliases = map[string]string{
	"white": "paper", "off-white": "linen", "sepia": "parchment", "soft-green": "directory",
	"brown": "cocoa-kraft", "dark": "graphite", "oled": "midnight",
	"fern": "directory", "cocoa": "cocoa-kraft",
}

// CanonicalTheme returns the current id for a theme id or alias; anything else is unchanged.
func CanonicalTheme(id string) string {
	if c, ok := ThemeAliases[id]; ok {
		return c
	}
	return id
}

func isThemeKey(k string) bool {
	return k == "ui.theme" || k == "ui.theme_day" || k == "ui.theme_night"
}

// MergedSettings returns DefaultSettings overlaid with the stored rows for
// those keys (a malformed stored value keeps the default).
func (d *DB) MergedSettings(ctx context.Context) (map[string]any, error) {
	out := make(map[string]any, len(DefaultSettings))
	for k, v := range DefaultSettings {
		out[k] = v
	}
	rows, err := d.reader.QueryContext(ctx, "SELECT key, value FROM settings WHERE key NOT LIKE 'sys.%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if _, known := DefaultSettings[k]; !known {
			continue
		}
		var val any
		if json.Unmarshal([]byte(v), &val) == nil {
			if k == SettingFavorites {
				val = readFavorites(val) // legacy spellings ("007") and repeats never reach the UI
			}
			if sv, ok := val.(string); ok && isThemeKey(k) {
				val = CanonicalTheme(sv)
			}
			out[k] = val
		}
	}
	return out, rows.Err()
}

// UIFolder is a folder with its unread count.
type UIFolder struct {
	ID        int64  `json:"id,string"`
	Name      string `json:"name"`
	Position  int64  `json:"position"`
	IsDefault bool   `json:"is_default"`
	Unread    int64  `json:"unread"`
}

// UIFolders lists folders in display order with unread counts.
func (d *DB) UIFolders(ctx context.Context) ([]UIFolder, error) {
	rows, err := d.reader.QueryContext(ctx, `SELECT fo.id, fo.name, fo.position, fo.is_default,
		COALESCE((SELECT count(*) FROM items i JOIN feeds f ON f.id = i.feed_id WHERE f.folder_id = fo.id AND i.read = 0), 0)
		FROM folders fo ORDER BY fo.position, fo.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UIFolder{}
	for rows.Next() {
		var f UIFolder
		var def int
		if err := rows.Scan(&f.ID, &f.Name, &f.Position, &def, &f.Unread); err != nil {
			return nil, err
		}
		f.IsDefault = def == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

// UIFeed is a feed as GET /api/bootstrap lists it. Status is the disabled
// reason, "failing" while consecutive_failures > 0, else "ok".
type UIFeed struct {
	ID       int64   `json:"id,string"`
	FolderID int64   `json:"folder_id,string"`
	Title    string  `json:"title"`
	SiteURL  string  `json:"site_url"`
	Icon     *string `json:"icon"`
	Unread   int64   `json:"unread"`
	Status   string  `json:"status"`
	Fulltext bool    `json:"fulltext"` // the feed's own flag (what the feed dialog edits)
	// FulltextEffective is what new items of this feed get: the feed flag, or true
	// while fetch.fulltext_all is on (items may still override it one by one).
	FulltextEffective bool   `json:"fulltext_effective"`
	Retention         *int64 `json:"retention"`
	IntervalMinutes   *int64 `json:"interval_minutes"`
	// AutoReadDays is the feed's own auto-read override (design 7.1d): null inherits library.auto_read_days, 0 is off for this feed.
	AutoReadDays *int64 `json:"auto_read_days"`
	IsArchive    bool   `json:"is_archive"`
	// StarredCount is what the delete confirm dialog shows: starred items move to
	// the archive feed unless the user chooses to delete them too.
	StarredCount int64 `json:"starred_count"`
}

// UIFeeds lists feeds in display order. The archive feed is listed only while
// it holds items.
func (d *DB) UIFeeds(ctx context.Context, env StatusEnv) ([]UIFeed, error) {
	return d.uiFeeds(ctx, env, "(f.disabled_reason IS NOT 'archive' OR EXISTS (SELECT 1 FROM items WHERE feed_id = f.id)) AND "+notDeletingSQL)
}

// uiFeeds runs the feed list query with a WHERE condition.
func (d *DB) uiFeeds(ctx context.Context, env StatusEnv, where string, args ...any) ([]UIFeed, error) {
	all := d.FulltextAll(ctx)
	rows, err := d.reader.QueryContext(ctx, `
		SELECT f.id, f.folder_id, COALESCE(NULLIF(f.custom_title, ''), NULLIF(f.title, ''), f.url), f.site_url, fi.hash,
		       COALESCE(u.n, 0), f.enabled, f.disabled_reason, f.consecutive_failures, f.fulltext, f.retention, f.interval_minutes, f.auto_read_days,
		       f.host, f.redirect_kind, f.redirect_to, COALESCE(f.last_new_items_at, f.created_at),
		       (SELECT count(*) FROM items WHERE feed_id = f.id AND starred = 1)
		FROM feeds f JOIN folders fo ON fo.id = f.folder_id
		LEFT JOIN feed_icons fi ON fi.feed_id = f.id
		LEFT JOIN (SELECT feed_id, count(*) AS n FROM items WHERE read = 0 GROUP BY feed_id) u ON u.feed_id = f.id
		WHERE `+where+`
		ORDER BY fo.position, fo.name, f.position, lower(COALESCE(NULLIF(f.custom_title, ''), NULLIF(f.title, ''), f.url)), f.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UIFeed{}
	for rows.Next() {
		var f UIFeed
		var hash, reason, host, rKind, rTo sql.NullString
		var lastNew int64
		var enabled, failures, fulltext int64
		var retention, interval, autoRead sql.NullInt64
		if err := rows.Scan(&f.ID, &f.FolderID, &f.Title, &f.SiteURL, &hash, &f.Unread, &enabled, &reason, &failures, &fulltext, &retention, &interval, &autoRead, &host, &rKind, &rTo, &lastNew, &f.StarredCount); err != nil {
			return nil, err
		}
		if hash.Valid {
			icon := "/api/feeds/" + strconv.FormatInt(f.ID, 10) + "/icon?h=" + hash.String
			f.Icon = &icon
		}
		f.IsArchive = reason.Valid && reason.String == "archive"
		f.Status = FeedStatus(StatusRow{DisabledReason: strp(reason), Enabled: enabled == 1, ConsecutiveFailures: failures,
			RedirectKind: strp(rKind), RedirectTo: strp(rTo), LastNewItemsAt: lastNew}, env.HostUntil[host.String], env.Now)
		f.Fulltext = fulltext == 1
		f.FulltextEffective = EffectiveFulltext(nil, f.Fulltext, all) == 1
		f.Retention, f.IntervalMinutes = intp(retention), intp(interval)
		f.AutoReadDays = intp(autoRead)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Counts returns the unread and starred item totals.
func (d *DB) Counts(ctx context.Context) (unread, starred int64, err error) {
	err = d.reader.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM items WHERE read = 0),
		(SELECT count(*) FROM items WHERE starred = 1)`).Scan(&unread, &starred)
	return
}

// SnapshotStatus is the nightly snapshot's last outcome (sys.last_snapshot_*).
type SnapshotStatus struct {
	LastAt    int64  // 0 = never
	LastError string // "" = none
}

// SnapshotStatus reads the snapshot bookkeeping keys.
func (d *DB) SnapshotStatus(ctx context.Context) SnapshotStatus {
	return SnapshotStatus{
		LastAt:    int64(settingInt(ctx, d.reader, "sys.last_snapshot_at", 0)),
		LastError: settingString(ctx, d.reader, "sys.last_snapshot_error", ""),
	}
}

// FeedUnreadCounts returns every feed's unread count, zeros included, so a
// `counts` event can zero a feed that just emptied.
func (d *DB) FeedUnreadCounts(ctx context.Context) (map[int64]int64, error) {
	rows, err := d.reader.QueryContext(ctx, `SELECT f.id, COALESCE(u.n, 0) FROM feeds f
		LEFT JOIN (SELECT feed_id, count(*) AS n FROM items WHERE read = 0 GROUP BY feed_id) u ON u.feed_id = f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
