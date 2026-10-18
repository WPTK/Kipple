package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/WPTK/kipple/internal/fetch"
)

func TestSetIsReproducibleAndParses(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if _, err := write(a, 7, small); err != nil {
		t.Fatal(err)
	}
	if _, err := write(b, 7, small); err != nil {
		t.Fatal(err)
	}
	c := t.TempDir()
	if _, err := write(c, 8, small); err != nil {
		t.Fatal(err)
	}
	differs := false
	for _, f := range feeds() {
		x, _ := os.ReadFile(filepath.Join(a, f.Name))
		y, _ := os.ReadFile(filepath.Join(b, f.Name))
		z, _ := os.ReadFile(filepath.Join(c, f.Name))
		if len(x) == 0 || !bytes.Equal(x, y) {
			t.Fatalf("%s: empty or not reproducible", f.Name)
		}
		differs = differs || !bytes.Equal(x, z)
		fd, err := fetch.ParseFeed(x, fetch.ParseOptions{FeedURL: "https://feeds.example.test/" + f.Name})
		if f.Parses && (err != nil || fd == nil || len(fd.Items) == 0) {
			t.Errorf("%s should parse with items: err=%v", f.Name, err)
		}
		if !f.Parses && err == nil && fd != nil && len(fd.Items) > 0 {
			t.Errorf("%s should not parse as a feed", f.Name)
		}
	}
	if !differs {
		t.Error("a different seed produced identical files")
	}
}
