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
