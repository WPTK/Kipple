package docscheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	pointer = regexp.MustCompile(`design §(\d+(?:\.\d+[a-z0-9]*)*)`)
	heading = regexp.MustCompile(`(?m)^#{2,3} (\d+(?:\.\d+[a-z0-9]*)*)[. ]`)
)

// Every "design §N" in a comment names a heading that exists in docs/design.md.
func TestDesignPointersResolve(t *testing.T) {
	root := filepath.Join("..", "..")
	doc, err := os.ReadFile(filepath.Join(root, "docs", "design.md"))
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, m := range heading.FindAllStringSubmatch(string(doc), -1) {
		have[m[1]] = true
	}
	if !have["1"] || !have["7.1"] {
		t.Fatalf("headings not parsed: %v", have)
	}
	checked := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist", "history":
				return fs.SkipDir
			}
			return nil
		}
		switch filepath.Ext(p) {
		case ".go", ".ts", ".tsx", ".mjs", ".ps1", ".yml":
		default:
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range pointer.FindAllStringSubmatch(string(b), -1) {
			checked++
			if !have[m[1]] && !strings.HasSuffix(p, "pointers_test.go") {
				t.Errorf("%s: design §%s has no heading in docs/design.md", filepath.ToSlash(p), m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Fatalf("only %d pointers found; the scan is broken", checked)
	}
}
