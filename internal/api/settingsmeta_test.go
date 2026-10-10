package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	t.Parallel()
	groups := map[string]bool{"reading": true, "sync": true, "library": true, "images": true, "stats": true, "account": true, "connection": true, "advanced": true}
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
	t.Parallel()
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
	rd := by["ui.reading_density"]
	require.Equal(t, "reader_menu", rd["surface"])
	require.Equal(t, "standard", rd["default"])
	for _, gone := range []string{"ui.font_size", "ui.font_ui", "ui.layouts", "stats.api_single_read_is_open"} {
		require.NotContains(t, by, gone)
	}
	require.Equal(t, "How often to check feeds", by["refresh.interval_minutes"]["label"])
	require.Equal(t, "minutes", by["refresh.interval_minutes"]["unit"])
	require.Equal(t, "browser_on_failure", by["fetch.user_agent_mode"]["default"])
	require.Equal(t, "Only when a feed refuses to load", by["fetch.user_agent_mode"]["options"].([]any)[0].(map[string]any)["label"])
	require.Equal(t, "hidden", by["greader.subscribe_fetch_now"]["surface"])
}

// Reading and list spacing share one vocabulary, the five steps; the first-draft names are read as steps.
func TestDensityVocabulary(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, out, _ := h.api(h.login(), "GET", "/api/settings", "")
	for _, x := range out["settings"].([]any) {
		m := x.(map[string]any)
		if k := m["key"]; k != "ui.reading_density" && k != "ui.list_density" {
			continue
		}
		require.Equal(t, "standard", m["default"], m["key"])
		var got []string
		for _, o := range m["options"].([]any) {
			got = append(got, o.(map[string]any)["value"].(string))
		}
		require.Equal(t, densitySteps, got, m["key"])
	}
	h.exec("INSERT INTO settings (key, value) VALUES ('ui.reading_density', '\"comfortable\"'), ('ui.list_density', '\"compact\"'), ('ui.font_body', '\"Inter\"')")
	_, out, _ = h.api(h.login(), "GET", "/api/settings", "")
	require.Equal(t, "standard", vals(out)["ui.reading_density"])
	require.Equal(t, "snug", vals(out)["ui.list_density"])
	require.Equal(t, "inter", vals(out)["ui.font_body"])
}

func TestThemeOptionsAndAliases(t *testing.T) {
	t.Parallel()
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
	require.Len(t, got, len(Schemes)+1)
	require.Equal(t, "system", got[len(got)-1])
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
	// The schedule is its own flag (ui.theme_schedule) next to "system", which older clients pass through.
	_, msg = theme.check("schedule")
	require.NotEmpty(t, msg)
}

// The schedule is a hidden bool, and its times are 24-hour HH:MM, 00:00 to 23:59, and nothing else.
func TestThemeScheduleTimes(t *testing.T) {
	t.Parallel()
	sched := settingDefByKey["ui.theme_schedule"]
	require.Equal(t, "hidden", sched.Surface)
	require.Equal(t, "bool", sched.Kind)
	require.Equal(t, false, store.DefaultSettings["ui.theme_schedule"])
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
	t.Parallel()
	h := newHarness(t)
	h.exec(`INSERT INTO settings (key, value) VALUES ('ui.theme', '"brown"'), ('ui.theme_night', '"oled"')`)
	_, out, _ := h.api(h.login(), "GET", "/api/settings", "")
	require.Equal(t, "cocoa-kraft", vals(out)["ui.theme"])
	require.Equal(t, "midnight", vals(out)["ui.theme_night"])
}

// The Go font list and the web app's fonts.ts must agree (ids, in order).
func TestFontsMatchWeb(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "lib", "fonts.ts"))
	require.NoError(t, err)
	var web []string
	for _, m := range regexp.MustCompile(`(?m)^  \{ id: "([a-z-]+)"`).FindAllStringSubmatch(string(raw), -1) {
		web = append(web, m[1])
	}
	var goIDs []string
	for _, f := range store.Fonts {
		goIDs = append(goIDs, f.ID)
	}
	require.Equal(t, web, goIDs)
}

// The Go scheme list and the web app's schemes.json must agree (ids and names, in order).
func TestSchemesMatchWebJSON(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	h := newHarness(t)
	h.exec("INSERT INTO settings (key, value) VALUES ('ui.line_height', '2'), ('ui.content_width', '900'), ('ui.font_size', '30'), ('ui.font_ui', '\"Inter\"'), ('ui.layouts', '{}'), ('stats.api_single_read_is_open', 'true')")
	_, out, _ := h.api(h.login(), "GET", "/api/settings", "")
	require.NotContains(t, vals(out), "ui.line_height")
	require.NotContains(t, vals(out), "ui.content_width")
	for _, gone := range []string{"ui.font_size", "ui.font_ui", "ui.layouts", "stats.api_single_read_is_open"} {
		require.NotContains(t, vals(out), gone)
	}
	require.Equal(t, "standard", vals(out)["ui.reading_density"])
}

// The help texts state what the code does (audit C6, C7): retention counts unread
// articles too, and the tz setting drives the nightly job as well as statistics.
func TestSettingHelpTexts(t *testing.T) {
	t.Parallel()
	text := map[string]string{}
	for _, d := range settingDefs {
		text[d.Key] = d.Description
	}
	require.Equal(t, "Only the newest N articles per feed are kept, read or unread. Starred articles are always kept.", text["retention.default"])
	require.Equal(t, "Used for daily statistics and the nightly maintenance job.", text["tz"])
}

func TestNewSettingsMetadata(t *testing.T) {
	t.Parallel()
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
