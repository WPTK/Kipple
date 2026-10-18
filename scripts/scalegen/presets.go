package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// preset is a named library shape for the 1.1 bug hunt (perf and trim tests). Output goes to
// .hunt/scale/<name>, which is gitignored.
type preset struct {
	feeds, items, days int
	starredPct         float64
	statsMult          float64
	note               string
}

var presets = map[string]preset{
	"articles-1k":   {feeds: 50, items: 1_000, days: 30, starredPct: 1.5, statsMult: 1, note: "1,000 articles"},
	"articles-10k":  {feeds: 200, items: 10_000, days: 120, starredPct: 1.5, statsMult: 1, note: "10,000 articles"},
	"articles-100k": {feeds: 500, items: 100_000, days: 400, starredPct: 1.5, statsMult: 1, note: "100,000 articles"},
	"feeds-100":     {feeds: 100, items: 10_000, days: 120, starredPct: 1.5, statsMult: 1, note: "100 feeds"},
	"feeds-1000":    {feeds: 1_000, items: 30_000, days: 120, starredPct: 1.5, statsMult: 1, note: "1,000 feeds"},
	"feeds-10000":   {feeds: 10_000, items: 50_000, days: 60, starredPct: 1.5, statsMult: 1, note: "10,000 feeds (at least one item each)"},
	"starred-heavy": {feeds: 300, items: 50_000, days: 200, starredPct: 40, statsMult: 1, note: "40% of 50,000 articles starred (starred are never trimmed)"},
	"long-history":  {feeds: 200, items: 20_000, days: 3650, starredPct: 1.5, statsMult: 3, note: "ten years of dense reading statistics"},
}

func presetNames() string {
	var n []string
	for k := range presets {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}

// applyPreset fills o from the named preset, except for the flags the user set (set holds flag names).
func applyPreset(o *genOpts, name string, set map[string]bool) error {
	p, ok := presets[name]
	if !ok {
		return fmt.Errorf("gen: unknown preset %q (have %s)", name, presetNames())
	}
	if !set["feeds"] {
		o.feeds = p.feeds
	}
	if !set["items"] {
		o.items = p.items
	}
	if !set["stats-days"] {
		o.days = p.days
	}
	if !set["starred-pct"] {
		o.starredPct = p.starredPct
	}
	if !set["stats-mult"] {
		o.statsMult = p.statsMult
	}
	if !set["dir"] {
		o.dir = filepath.Join(".hunt", "scale", name)
	}
	return nil
}
