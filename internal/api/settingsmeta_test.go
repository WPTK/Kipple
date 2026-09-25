package api

import (
	"encoding/json"
	"net/http"
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
	groups := map[string]bool{"reading": true, "sync": true, "library": true, "images": true, "account": true, "advanced": true}
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
	require.Len(t, dens["options"], 3)
	for _, o := range dens["options"].([]any) {
		m := o.(map[string]any)
		v := m["value"].(string)
		css := m["css"].(map[string]any)
		require.Equal(t, labels[v], m["label"])
		require.EqualValues(t, want[v][0], css["line_height"], v)
		require.Equal(t, want[v][1], css["content_width"], v)
	}
}

func TestOLEDTheme(t *testing.T) {
	var theme settingDef
	for _, d := range settingDefs {
		if d.Key == "ui.theme" {
			theme = d
		}
	}
	var got []string
	for _, o := range theme.Options {
		got = append(got, o.Value.(string))
		if o.Value == "oled" {
			require.Equal(t, "OLED dark", o.Label)
		}
	}
	require.Equal(t, []string{"white", "off-white", "sepia", "soft-green", "brown", "dark", "oled", "system"}, got)
	require.Contains(t, theme.Description, "battery")
}

func TestOldReadingRowsAreIgnored(t *testing.T) {
	h := newHarness(t)
	h.exec("INSERT INTO settings (key, value) VALUES ('ui.line_height', '2'), ('ui.content_width', '900')")
	_, out, _ := h.api(h.login(), "GET", "/api/settings", "")
	require.NotContains(t, vals(out), "ui.line_height")
	require.NotContains(t, vals(out), "ui.content_width")
	require.Equal(t, "comfortable", vals(out)["ui.reading_density"])
}
