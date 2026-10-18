package testseed

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.txt")
	if err := os.WriteFile(p, []byte("# c\n\nplain\n\"a\\x00b\\n\"\n\"bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Lines(p)
	want := []string{"plain", "a\x00b\n", "\"bad"}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	if Lines(filepath.Join(t.TempDir(), "none")) != nil {
		t.Fatal("missing file should yield nil")
	}
}
