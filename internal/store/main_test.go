package store

import (
	"log/slog"
	"os"
	"testing"
)

// TestMain silences the process-wide default logger for this package. Most tests
// and the benchmarks open a store without Options.Logger, so Open falls back to
// slog.Default, and every migration it applies writes an INFO line to stderr.
// go test streams that output with -bench, where it lands between result lines
// and breaks benchstat. A test that checks what the store logs passes its own
// Logger, which this does not touch.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}
