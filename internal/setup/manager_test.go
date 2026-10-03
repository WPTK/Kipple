package setup

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestNotPendingByDefault(t *testing.T) {
	var nilMgr *Manager
	if nilMgr.Pending() {
		t.Fatal("a nil Manager is pending")
	}
	nilMgr.Finish() // must not panic
	if (&Manager{}).Pending() {
		t.Fatal("a zero Manager is pending")
	}
}

func TestFinishIsOneWayAndRunsTheCallbackOnce(t *testing.T) {
	var ran atomic.Int32
	m := NewPending(func() { ran.Add(1) })
	if !m.Pending() {
		t.Fatal("NewPending is not pending")
	}
	if ran.Load() != 0 {
		t.Fatal("the callback ran before Finish")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); m.Finish() }()
	}
	wg.Wait()
	m.Finish()
	if m.Pending() {
		t.Fatal("still pending after Finish")
	}
	if got := ran.Load(); got != 1 {
		t.Fatalf("callback ran %d times, want 1", got)
	}
}

func TestRemoveStaleTokenFile(t *testing.T) {
	dir := t.TempDir()
	if err := RemoveStaleTokenFile(dir); err != nil {
		t.Fatalf("a missing file is fine: %v", err)
	}
	p := filepath.Join(dir, "setup-token")
	if err := os.WriteFile(p, []byte("AAAA-BBBB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveStaleTokenFile(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("the stale token file is still there: %v", err)
	}
}
