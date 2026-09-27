package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

func jsonRoundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var out any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

func TestSettingDefsShape(t *testing.T) {
	groups := map[string]bool{"reading": true, "sync": true, "library": true, "images": true, "stats": true, "account": true, "advanced": true}
	surfaces := map[string]bool{"reader_menu": true, "settings": true, "hidden": true}
	kinds := map[string]bool{"bool": true, "enum": true, "int": true, "text": true, "json": true}
	seen := map[string]bool{}
	for _, d := range settingDefs {
		require.False(t, seen[d.Key], "duplicate key %s", d.Key)
		seen[d.Key] = true
		require.NotEmpty(t, d.Label, d.Key)
		require.NotEmpty(t, d.Description, d.Key)
		require.True(t, groups[d.Group], "%s group %q", d.Key, d.Group)
		require.True(t, surfaces[d.Surface], "%s surface %q", d.Key, d.Surface)
		require.True(t, kinds[d.Kind], "%s kind %q", d.Key, d.Kind)
		require.NotNil(t, d.check, d.Key)
		if d.Kind == "enum" {
			require.NotEmpty(t, d.Options, d.Key)
			for _, o := range d.Options {
				require.NotEmpty(t, o.Label, d.Key)
			}
		}
		if d.Kind == "int" {
			require.NotNil(t, d.Min, d.Key)
			require.NotNil(t, d.Max, d.Key)
			require.NotNil(t, d.Step, d.Key)
		}
		// The default validates under the key's own rule, through JSON like a PATCH body.
		def, ok := store.DefaultSettings[d.Key]
		require.True(t, ok, "%s has no default", d.Key)
		v, msg := d.check(jsonRoundTrip(t, def))
		require.Empty(t, msg, "%s default %v", d.Key, def)
		require.EqualValues(t, def, v, d.Key)
		for _, o := range d.Options {
			_, msg := d.check(jsonRoundTrip(t, o.Value))
			require.Empty(t, msg, "%s option %v", d.Key, o.Value)
		}
	}
	for k := range store.DefaultSettings {
		require.True(t, seen[k], "default %s has no settingDef", k)
	}
	require.NotContains(t, seen, "ui.line_height")
	require.NotContains(t, seen, "ui.content_width")
}

func TestSettingsEndpointMetadata(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.api(h.login(), "GET", "/api/settings", "")
	require.Equal(t, http.StatusOK, code)
	list := out["settings"].([]any)
	require.Len(t, list, len(settingDefs))
	by := map[string]map[string]any{}
	for _, x := range list {
		m := x.(map[string]any)
		for _, f := range []string{"key", "value", "default", "label", "description", "group", "kind", "surface"} {
			require.Contains(t, m, f, m["key"])
		}
		by[m["key"].(string)] = m
	}
	fs := by["ui.font_size"]
	require.EqualValues(t, 12, fs["min"])
	require.EqualValues(t, 32, fs["max"])
	require.EqualValues(t, 1, fs["step"])
	require.Equal(t, "reader_menu", fs["surface"])
	require.Equal(t, "How often to check feeds", by["refresh.interval_minutes"]["label"])
	require.Equal(t, "minutes", by["refresh.interval_minutes"]["unit"])
	require.Equal(t, "browser_on_failure", by["fetch.user_agent_mode"]["default"])
	require.Equal(t, "Only when a feed refuses to load", by["fetch.user_agent_mode"]["options"].([]any)[0].(map[string]any)["label"])
	require.Equal(t, "hidden", by["greader.subscribe_fetch_now"]["surface"])
}

func TestReadingDensityMapping(t *testing.T) {
	h := newHarness(t)
	_, out, _ := h.api(h.login(), "GET", "/api/settings", "")
	var dens map[string]any
	for _, x := range out["settings"].([]any) {
		if m := x.(map[string]any); m["key"] == "ui.reading_density" {
			dens = m
		}
	}
	require.NotNil(t, dens)
	require.Equal(t, "comfortable", dens["default"])
	want := map[string][2]any{"compact": {1.45, "620px"}, "comfortable": {1.6, "680px"}, "relaxed": {1.8, "720px"}}
	labels := map[string]string{"compact": "Compact", "comfortable": "Comfortable", "relaxed": "Relaxed"}
	require.Len(t, dens["options"], 7) // the three originals, then the steps that are not repeats
	for _, o := range dens["options"].([]any) {
		m := o.(map[string]any)
		v := m["value"].(string)
		if _, legacy := want[v]; !legacy {
			continue // the round-2 steps carry no CSS
		}
		css := m["css"].(map[string]any)
		require.Equal(t, labels[v], m["label"])
		require.EqualValues(t, want[v][0], css["line_height"], v)
		require.Equal(t, want[v][1], css["content_width"], v)
	}
}

func TestThemeOptionsAndAliases(t *testing.T) {
	var theme settingDef
	for _, d := range settingDefs {
		if d.Key == "ui.theme" {
			theme = d
		}
	}
	var got []string
	for _, o := range theme.Options {
		got = append(got, o.Value.(string))
	}
	require.Len(t, got, len(Schemes)+2)
	require.Equal(t, []string{"system", "schedule"}, got[len(got)-2:])
	require.Contains(t, theme.Description, "battery")
	for old, want := range store.ThemeAliases {
		v, msg := theme.check(old)
		require.Empty(t, msg, old)
		require.Equal(t, want, v, old)
	}
	_, msg := theme.check("neon")
	require.NotEmpty(t, msg)
	day := settingDefByKey["ui.theme_day"]
	_, msg = day.check("system")
	require.NotEmpty(t, msg, "the day theme is always a scheme")
	_, msg = day.check("schedule")
	require.NotEmpty(t, msg, "the day theme is always a scheme")
	v, msg := theme.check("schedule")
	require.Empty(t, msg)
	require.Equal(t, "schedule", v)
}

// The schedule times are 24-hour HH:MM, 00:00 to 23:59, and nothing else.
func TestThemeScheduleTimes(t *testing.T) {
	for _, k := range []string{"ui.theme_night_start", "ui.theme_day_start"} {
		d := settingDefByKey[k]
		require.Equal(t, "hidden", d.Surface, k)
		for _, good := range []string{"00:00", "07:00", "12:30", "21:00", "23:59"} {
			v, msg := d.check(good)
			require.Empty(t, msg, "%s %q", k, good)
			require.Equal(t, good, v)
		}
		for _, bad := range []any{"24:00", "7:00", "07:60", "07:00:00", "0700", " 07:00", "07:0a", "+7:00", "", nil, 700, true} {
			_, msg := d.check(bad)
			require.NotEmpty(t, msg, "%s %v", k, bad)
		}
	}
}

// A stored row with an old theme id reads back as the current id, and is not rewritten.
func TestStoredOldThemeReadsAsAlias(t *testing.T) {
	h := newHarness(t)
	h.exec(`INSERT INTO settings (key, value) VALUES ('ui.theme', '"brown"'), ('ui.theme_night', '"oled"')`)
	_, out, _ := h.api(h.login(), "GET", "/api/settings", "")
	require.Equal(t, "cocoa-kraft", vals(out)["ui.theme"])
	require.Equal(t, "midnight", vals(out)["ui.theme_night"])
}

// The Go scheme list and the web app's schemes.json must agree (ids and names, in order).
func TestSchemesMatchWebJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "theme", "schemes.json"))
	require.NoError(t, err)
	var web []struct{ ID, Name string }
	require.NoError(t, json.Unmarshal(raw, &web))
	require.Len(t, web, len(Schemes))
	for i, w := range web {
		require.Equal(t, Schemes[i].ID, w.ID, "scheme %d", i)
		require.Equal(t, Schemes[i].Name, w.Name, w.ID)
	}
	for old, cur := range store.ThemeAliases {
		found := false
		for _, sc := range Schemes {
			found = found || sc.ID == cur
		}
		require.True(t, found, "alias %s -> %s is not a scheme", old, cur)
	}
}

func TestOldReadingRowsAreIgnored(t *testing.T) {
	h := newHarness(t)
	h.exec("INSERT INTO settings (key, value) VALUES ('ui.line_height', '2'), ('ui.content_width', '900')")
	_, out, _ := h.api(h.login(), "GET", "/api/settings", "")
	require.NotContains(t, vals(out), "ui.line_height")
	require.NotContains(t, vals(out), "ui.content_width")
	require.Equal(t, "comfortable", vals(out)["ui.reading_density"])
}

// The help texts state what the code does (audit C6, C7): retention counts unread
// articles too, and the tz setting drives the nightly job as well as statistics.
func TestSettingHelpTexts(t *testing.T) {
	text := map[string]string{}
	for _, d := range settingDefs {
		text[d.Key] = d.Description
	}
	require.Equal(t, "Only the newest N articles per feed are kept, read or unread. Starred articles are always kept.", text["retention.default"])
	require.Equal(t, "Used for daily statistics and the nightly maintenance job.", text["tz"])
}

func TestNewSettingsMetadata(t *testing.T) {
	h := newHarness(t)
	_, out, _ := h.api(h.login(), "GET", "/api/settings", "")
	by := map[string]map[string]any{}
	for _, x := range out["settings"].([]any) {
		m := x.(map[string]any)
		by[m["key"].(string)] = m
	}
	ft := by["fetch.fulltext_all"]
	require.Equal(t, "Fetch the full article for every feed", ft["label"])
	require.Equal(t, "library", ft["group"])
	require.Equal(t, "settings", ft["surface"])
	require.Equal(t, "bool", ft["kind"])
	require.Equal(t, false, ft["default"])
	require.Contains(t, ft["description"], "bandwidth")
	require.Contains(t, ft["description"], "fall back")
	fav := by["library.favorites"]
	require.Equal(t, "library", fav["group"])
	require.Equal(t, "hidden", fav["surface"])
	require.Equal(t, "json", fav["kind"])
	require.Equal(t, []any{}, fav["default"])
	require.Equal(t, []any{}, fav["value"])
	require.NotEmpty(t, fav["label"])
	require.NotEmpty(t, fav["description"])
	icons := by["greader.icon_urls"]
	require.Equal(t, true, icons["default"])
	require.Equal(t, true, icons["value"])
}
