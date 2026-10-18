// Package testseed loads fuzz seed files for tests. Go's fuzzer has no dictionary flag, so token lists live as
// plain text files under a package's testdata and each fuzz target adds them with f.Add.
//
// Line format: one seed per line. Blank lines and lines starting with "#" are skipped. A line starting with a
// double quote is a Go string literal (so control bytes and newlines can be written as \x00 or \n); any other
// line is used verbatim.
package testseed

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Lines reads a seed list. A missing file yields no seeds, so a package still tests when its list was removed.
func Lines(path string) []string {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	var out []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if l[0] == '"' {
			if u, err := strconv.Unquote(l); err == nil {
				l = u
			}
		}
		out = append(out, l)
	}
	return out
}

// Files returns the contents of every regular file in dir (not recursive), in name order, skipping README files.
func Files(dir string) [][]byte {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out [][]byte
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(strings.ToLower(e.Name()), "readme") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			out = append(out, b)
		}
	}
	return out
}
