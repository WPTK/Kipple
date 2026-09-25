package api

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

// Setting groups and surfaces (the UI contract of GET /api/settings).
const (
	groupReading  = "reading"
	groupSync     = "sync"
	groupLibrary  = "library"
	groupImages   = "images"
	groupAccount  = "account"
	groupAdvanced = "advanced"

	surfaceReader   = "reader_menu" // the Kindle-style reading appearance menu
	surfaceSettings = "settings"    // the Settings screen
	surfaceHidden   = "hidden"      // validated and PATCH-able, not shown by default
)

// settingOption is one choice of an enum setting. CSS carries values the
// frontend must use as-is instead of inventing its own numbers.
type settingOption struct {
	Value any            `json:"value"`
	Label string         `json:"label"`
	CSS   map[string]any `json:"css,omitempty"`
}

// settingDef describes one user-visible setting: validation plus everything a
// UI needs to render it (design §2.2 whitelist, §7.1).
type settingDef struct {
	Key         string          `json:"key"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Group       string          `json:"group"`
	Kind        string          `json:"kind"` // bool | enum | int | text | json
	Options     []settingOption `json:"options,omitempty"`
	Min         *int            `json:"min,omitempty"`
	Max         *int            `json:"max,omitempty"`
	Step        *int            `json:"step,omitempty"`
	Unit        string          `json:"unit,omitempty"`
	Surface     string          `json:"surface"`

	check func(v any) (any, string)
}

// settingView is a settingDef with its current value and default.
type settingView struct {
	settingDef
	Value   any `json:"value"`
	Default any `json:"default"`
}

const maxLayoutsBytes = 4096

func ip(n int) *int { return &n }

func intIn(lo, hi int) func(any) (any, string) {
	return func(v any) (any, string) {
		f, ok := v.(float64)
		if !ok || f != math.Trunc(f) || f < float64(lo) || f > float64(hi) {
			return nil, fmt.Sprintf("must be an integer from %d to %d", lo, hi)
		}
		return int(f), ""
	}
}

func boolVal(v any) (any, string) {
	b, ok := v.(bool)
	if !ok {
		return nil, "must be true or false"
	}
	return b, ""
}

func oneOf(vals ...string) func(any) (any, string) {
	return func(v any) (any, string) {
		if s, ok := v.(string); ok {
			for _, w := range vals {
				if s == w {
					return s, ""
				}
			}
		}
		q := make([]string, 0, len(vals))
		for _, w := range vals {
			q = append(q, fmt.Sprintf("%q", w))
		}
		return nil, "must be one of " + strings.Join(q, ", ")
	}
}

// ReadingDensityCSS is the one mapping from ui.reading_density to CSS. It is
// served in the option metadata so the frontend never invents numbers.
var ReadingDensityCSS = map[string]map[string]any{
	"compact":     {"line_height": 1.45, "content_width": "620px"},
	"comfortable": {"line_height": 1.6, "content_width": "680px"},
	"relaxed":     {"line_height": 1.8, "content_width": "720px"},
}

var (
	retentionValues = map[int]bool{0: true, 50: true, 100: true, 250: true, 500: true, 1000: true}
	// uiFonts are the bundled and system faces of CLAUDE.md; "" = the platform default.
	uiFonts = []string{"", "Literata", "Charter", "Vollkorn", "Gentium Book Plus", "Source Serif 4", "Arvo",
		"Inter", "Manrope", "Source Sans 3", "JetBrains Mono", "Source Code Pro",
		"New York", "SF Pro", "SF Mono", "Georgia", "Menlo"}
)

func opts(pairs ...string) []settingOption {
	out := make([]settingOption, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, settingOption{Value: pairs[i], Label: pairs[i+1]})
	}
	return out
}

func optValues(os []settingOption) []string {
	out := make([]string, len(os))
	for i, o := range os {
		out[i] = o.Value.(string)
	}
	return out
}

func fontOptions() []settingOption {
	out := make([]settingOption, 0, len(uiFonts))
	for _, f := range uiFonts {
		label := f
		if f == "" {
			label = "Default"
		}
		out = append(out, settingOption{Value: f, Label: label})
	}
	return out
}

func densityOptions() []settingOption {
	out := opts("compact", "Compact", "comfortable", "Comfortable", "relaxed", "Relaxed")
	for i := range out {
		out[i].CSS = ReadingDensityCSS[out[i].Value.(string)]
	}
	return out
}

func retentionOptions() []settingOption {
	out := []settingOption{}
	for _, n := range []int{50, 100, 250, 500, 1000} {
		out = append(out, settingOption{Value: n, Label: fmt.Sprintf("Newest %d", n)})
	}
	return append(out, settingOption{Value: 0, Label: "Keep everything"})
}

var (
	themeOptions = opts("white", "White", "off-white", "Off-white", "sepia", "Sepia",
		"soft-green", "Soft green", "brown", "Brown", "dark", "Dark", "oled", "OLED dark", "system", "Match my device")
	uaModeOptions = opts(
		store.UAModeOnFailure, "Only when a feed refuses to load",
		store.UAModeDefault, "Always identify as Kipple",
		store.UAModeAlways, "Always look like a browser")
	imgModeOptions = opts("http_only", "Only insecure images", "all", "All images")
)

// settingDefs lists every user-writable setting in display order. Defaults come
// from store.DefaultSettings (one source of truth).
var settingDefs = []settingDef{
	// Reading appearance menu.
	{Key: "ui.theme", Label: "Theme", Description: "The color scheme for reading. OLED dark is true black, which saves battery on OLED screens.",
		Group: groupReading, Kind: "enum", Options: themeOptions, Surface: surfaceReader, check: oneOf(optValues(themeOptions)...)},
	{Key: "ui.font_body", Label: "Reading font", Description: "The typeface used for article text.",
		Group: groupReading, Kind: "enum", Options: fontOptions(), Surface: surfaceReader, check: oneOf(uiFonts...)},
	{Key: "ui.font_size", Label: "Text size", Description: "How large article text is.",
		Group: groupReading, Kind: "int", Min: ip(12), Max: ip(32), Step: ip(1), Unit: "px", Surface: surfaceReader, check: intIn(12, 32)},
	{Key: "ui.reading_density", Label: "Spacing", Description: "How tightly lines are spaced and how wide the text column is.",
		Group: groupReading, Kind: "enum", Options: densityOptions(), Surface: surfaceReader, check: oneOf("compact", "comfortable", "relaxed")},

	// Settings screen: reading extras.
	{Key: "ui.font_ui", Label: "Interface font", Description: "The typeface used for menus, lists and buttons.",
		Group: groupReading, Kind: "enum", Options: fontOptions(), Surface: surfaceSettings, check: oneOf(uiFonts...)},
	{Key: "ui.mark_read_on_scroll", Label: "Mark articles read as I scroll", Description: "Articles you scroll past in the list are marked read automatically.",
		Group: groupReading, Kind: "bool", Surface: surfaceSettings, check: boolVal},

	// Sync.
	{Key: "refresh.interval_minutes", Label: "How often to check feeds", Description: "Kipple looks for new articles this often, unless a feed sets its own schedule.",
		Group: groupSync, Kind: "int", Min: ip(5), Max: ip(1440), Step: ip(5), Unit: "minutes", Surface: surfaceSettings, check: intIn(5, 1440)},
	{Key: "fetch.user_agent_mode", Label: "Browser identity for feeds", Description: "Some sites block feed readers; this controls when Kipple presents itself as an ordinary web browser instead.",
		Group: groupSync, Kind: "enum", Options: uaModeOptions, Surface: surfaceSettings, check: oneOf(optValues(uaModeOptions)...)},
	{Key: "fetch.user_agent", Label: "Custom user agent", Description: "Optional. Replaces the built-in browser identity when Kipple needs to look like a browser. Leave empty for the default.",
		Group: groupSync, Kind: "text", Surface: surfaceSettings, check: func(v any) (any, string) {
			s, ok := v.(string)
			if !ok || len(s) > 200 || hasBadHeaderByte(s) {
				return nil, "must be a string of at most 200 characters without line breaks"
			}
			return strings.TrimSpace(s), ""
		}},

	// Library.
	{Key: "retention.default", Label: "Articles to keep per feed", Description: "Older read articles beyond this many are removed; starred articles are always kept.",
		Group: groupLibrary, Kind: "enum", Options: retentionOptions(), Surface: surfaceSettings, check: func(v any) (any, string) {
			if f, ok := v.(float64); ok && f == math.Trunc(f) && retentionValues[int(f)] {
				return int(f), ""
			}
			return nil, "must be 50, 100, 250, 500, 1000 or 0 (unlimited)"
		}},
	{Key: "retention.restore_days", Label: "Days you can restore removed articles", Description: "How long a removed article can be brought back. Zero turns this off.",
		Group: groupLibrary, Kind: "int", Min: ip(0), Max: ip(store.MaxRestoreDays), Step: ip(1), Unit: "days", Surface: surfaceSettings, check: intIn(0, store.MaxRestoreDays)},

	// Account.
	{Key: "tz", Label: "Time zone", Description: "Used for daily statistics and for showing times.",
		Group: groupAccount, Kind: "text", Surface: surfaceSettings, check: func(v any) (any, string) {
			s, ok := v.(string)
			if !ok || s == "" || s == "Local" || len(s) > 64 {
				return nil, "must be an IANA time zone name such as America/New_York"
			}
			if _, err := time.LoadLocation(s); err != nil {
				return nil, "unknown time zone"
			}
			return s, ""
		}},

	// Advanced: shown in an Advanced section of the Settings screen.
	{Key: "fetch.honor_publisher_ttl", Label: "Follow publisher refresh hints", Description: "Wait longer between checks when a site asks readers not to check too often.",
		Group: groupAdvanced, Kind: "bool", Surface: surfaceSettings, check: boolVal},
	{Key: "greader.icon_urls", Label: "Send feed icons to sync apps", Description: "Let apps like Reeder show each feed's icon.",
		Group: groupAdvanced, Kind: "bool", Surface: surfaceSettings, check: boolVal},

	// Hidden plumbing: validated and PATCH-able, never shown by default.
	{Key: "imgproxy.mode", Label: "Images to load through Kipple", Description: "Which article images are fetched by Kipple instead of your device.",
		Group: groupImages, Kind: "enum", Options: imgModeOptions, Surface: surfaceHidden, check: oneOf(optValues(imgModeOptions)...)},
	{Key: "greader.ot_includes_user_changes", Label: "Include your own changes in sync", Description: "Whether changes made in Kipple also appear as new activity to sync apps.",
		Group: groupAdvanced, Kind: "bool", Surface: surfaceHidden, check: boolVal},
	{Key: "greader.subscribe_fetch_now", Label: "Fetch new feeds at once from sync apps", Description: "Fetch a feed immediately when a sync app subscribes to it.",
		Group: groupAdvanced, Kind: "bool", Surface: surfaceHidden, check: boolVal},
	{Key: "stats.api_single_read_is_open", Label: "Count single reads from sync apps", Description: "Treat an article opened in a sync app as opened for reading statistics.",
		Group: groupAdvanced, Kind: "bool", Surface: surfaceHidden, check: boolVal},
	{Key: "ui.layouts", Label: "Remembered list layouts", Description: "The list layout you chose for each folder or feed.",
		Group: groupAdvanced, Kind: "json", Surface: surfaceHidden, check: func(v any) (any, string) {
			m, ok := v.(map[string]any)
			if ok {
				for k, x := range m {
					if s, isStr := x.(string); !isStr || len(k) > 64 || len(s) > 32 {
						ok = false
					}
				}
				if b, _ := json.Marshal(m); len(b) > maxLayoutsBytes {
					ok = false
				}
			}
			if !ok {
				return nil, "must be an object of short string values (at most 4 KB)"
			}
			return m, ""
		}},
}

var settingDefByKey = func() map[string]settingDef {
	m := make(map[string]settingDef, len(settingDefs))
	for _, d := range settingDefs {
		m[d.Key] = d
	}
	return m
}()

// hasBadHeaderByte reports a byte that is not valid in an HTTP header value: any
// control character other than tab, and DEL.
func hasBadHeaderByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 && c != '	' || c == 0x7f {
			return true
		}
	}
	return false
}
