package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/WPTK/kipple/internal/store"
)

// A rewound database opens: every migration after the rewind point runs on it again, so rewind must
// undo what a run-once migration created. The upgrade measurement in docs/performance.md depends on it.
func TestRewindThenOpenMigrates(t *testing.T) {
	ctx := context.Background()
	for _, v := range []int{6, 10, 11} {
		path := filepath.Join(t.TempDir(), "kipple.db")
		db, err := store.Open(ctx, store.Options{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := rewind(path, v); err != nil {
			t.Fatalf("rewind to %d: %v", v, err)
		}
		db, err = store.Open(ctx, store.Options{Path: path})
		if err != nil {
			t.Fatalf("open after rewind to %d: %v", v, err)
		}
		got, err := db.Version(ctx)
		_ = db.Close()
		if err != nil || got != store.LatestVersion() {
			t.Fatalf("after rewind to %d: version %d, %v", v, got, err)
		}
	}
}

func TestPresetsResolve(t *testing.T) {
	for name, p := range presets {
		var o genOpts
		if err := applyPreset(&o, name, nil); err != nil {
			t.Fatal(err)
		}
		if o.feeds != p.feeds || o.items != p.items || o.dir != filepath.Join(".hunt", "scale", name) {
			t.Errorf("%s: %+v", name, o)
		}
		if p.feeds < 1 || p.items < p.feeds/2 || p.starredPct <= 0 || p.starredPct > 100 {
			t.Errorf("%s: odd shape %+v", name, p)
		}
	}
	o := genOpts{feeds: 7, dir: "x"}
	if err := applyPreset(&o, "articles-1k", map[string]bool{"feeds": true, "dir": true}); err != nil || o.feeds != 7 || o.dir != "x" || o.items != 1000 {
		t.Errorf("explicit flags must win: %+v %v", o, err)
	}
	if err := applyPreset(&o, "nope", nil); err == nil {
		t.Error("unknown preset accepted")
	}
}

// A small generated library has the requested counts and starred share, and opens at the latest schema.
func TestGenerateSmallShape(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	o := genOpts{dir: dir, feeds: 20, items: 400, seed: 3, feedBase: "http://127.0.0.1:1932", days: 3, starredPct: 50, statsMult: 0.2}
	if err := generate(ctx, o); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(dir, "kipple.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var feeds, items, starred int
	r := db.Reader()
	for q, dst := range map[string]*int{"SELECT count(*) FROM feeds": &feeds, "SELECT count(*) FROM items": &items, "SELECT count(*) FROM items WHERE starred = 1": &starred} {
		if err := r.QueryRowContext(ctx, q).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if feeds != 20 || items != 400 || starred < 140 || starred > 260 {
		t.Fatalf("feeds %d items %d starred %d", feeds, items, starred)
	}
}
