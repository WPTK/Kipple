package hunt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/WPTK/kipple/internal/backup"
)

// checkRestore runs CheckRestore on a case with a deadline and converts a panic or a hang into an error.
func checkRestore(c RestoreCase, dir string) (err error) {
	p, err := c.WriteTo(dir)
	if err != nil {
		return err
	}
	type res struct{ err error }
	ch := make(chan res, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- res{fmt.Errorf("PANIC: %v", r)}
			}
		}()
		_, e := backup.CheckRestore(ctx, p)
		ch <- res{e}
	}()
	select {
	case r := <-ch:
		if r.err != nil && len(r.err.Error()) >= 5 && r.err.Error()[:5] == "PANIC" {
			return r.err
		}
		return &verdict{r.err}
	case <-time.After(45 * time.Second):
		return fmt.Errorf("HANG: CheckRestore did not return in 45s")
	}
}

// verdict wraps CheckRestore's own result (nil means accepted) so a panic or hang stays distinguishable.
type verdict struct{ err error }

func (v *verdict) Error() string { return "verdict" }

func runCase(t *testing.T, c RestoreCase) {
	t.Helper()
	err := checkRestore(c, t.TempDir())
	v, ok := err.(*verdict)
	if !ok {
		t.Fatalf("%s: %v", c.Name, err)
	}
	switch c.Want {
	case WantAccept:
		if v.err != nil {
			t.Fatalf("%s: want accepted, got %v", c.Name, v.err)
		}
	case WantReject:
		if v.err == nil {
			t.Fatalf("%s: want rejected, was accepted", c.Name)
		}
	}
}

// TestRestoreCases: CheckRestore rejects every damaged file with an error, accepts the healthy latest, and
// never panics or hangs. Cases that do panic or hang are listed in knownBad (with where they are recorded).
func TestRestoreCases(t *testing.T) {
	cs, err := RestoreCases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		t.Run(c.Name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestRestoreSeedsOnDisk checks the committed seed files in testdata/restore the same way (they are the
// header-sized ones; any CheckRestore verdict is fine as long as it returns).
func TestRestoreSeedsOnDisk(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "restore", "*.seed"))
	if len(files) == 0 {
		t.Skip("no seed files")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(f), func(t *testing.T) {
			runCase(t, RestoreCase{Name: filepath.Base(f), DB: b, Want: WantReject})
		})
	}
}

// FuzzCheckRestoreStructured mutates the seed and knobs of RestoreFromSeed, not raw bytes.
func FuzzCheckRestoreStructured(f *testing.F) {
	for m := range uint8(8) {
		f.Add(uint64(m)+1, uint8(18), uint8(3), m, uint8(3))
	}
	f.Fuzz(func(t *testing.T, seed uint64, version, pageSize, mutation, flips uint8) {
		c, err := RestoreFromSeed(context.Background(), seed, RestoreKnobs{version, pageSize, mutation, flips})
		if err != nil {
			t.Fatal(err)
		}
		runCase(t, c)
	})
}

// TestRestoreFromSeedDeterministic: the same inputs give the same bytes.
func TestRestoreFromSeedDeterministic(t *testing.T) {
	k := RestoreKnobs{Version: 5, PageSize: 3, Mutation: 3, Flips: 9}
	a, err := RestoreFromSeed(context.Background(), 42, k)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RestoreFromSeed(context.Background(), 42, k)
	if string(a.DB) != string(b.DB) || a.Name != b.Name {
		t.Fatal("not deterministic")
	}
}

// FuzzCheckRestoreRaw mutates raw database bytes, starting from the committed front-of-file seeds.
func FuzzCheckRestoreRaw(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "restore", "*.seed"))
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b, false)
			f.Add(b, true)
		}
	}
	f.Fuzz(func(t *testing.T, db []byte, asWAL bool) {
		c := RestoreCase{Name: "raw", DB: db, Want: WantAny}
		if asWAL {
			c.WAL = db
		}
		runCase(t, c)
	})
}
