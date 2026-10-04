package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fuzzNames are few and clash on purpose: case variants, and names holding '/' that equal other
// folders' paths.
var fuzzNames = []string{"A", "a", "B", "A/B", "b/a", "C", "a/b/c"}

// FuzzFolderOps runs random sequences of folder creates, moves, renames, deletes and Reader API path
// resolutions against one database, and checks the writer's invariants after every step: no cycle,
// unique full paths, at most MaxFolderDepth levels, the default folder alone at the top. A refusal is
// fine; any other error is a bug.
func FuzzFolderOps(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 1, 1, 0, 2, 2, 1, 1, 0})
	f.Add([]byte{0, 0, 3, 0, 0, 0, 0, 1, 2, 4, 0, 0})                                              // "A/B" beside A > B
	f.Add([]byte{0, 0, 0, 0, 1, 0, 0, 2, 0, 1, 1, 2, 1, 3, 1})                                     // a chain, then moves that would cycle
	f.Add([]byte{0, 0, 0, 0, 1, 2, 0, 2, 2, 0, 3, 2, 0, 4, 2, 0, 5, 2, 0, 6, 2, 0, 7, 2, 0, 8, 2}) // past the depth cap
	f.Add([]byte{4, 6, 0, 4, 3, 0, 3, 1, 0, 2, 1, 1})

	db, err := Open(context.Background(), Options{Path: filepath.Join(f.TempDir(), "kipple.db")})
	require.NoError(f, err)
	f.Cleanup(func() { _ = db.Close() })

	f.Fuzz(func(t *testing.T, ops []byte) {
		ctx := context.Background()
		_, err := db.writer.ExecContext(ctx, "DELETE FROM folders WHERE is_default = 0")
		require.NoError(t, err)
		if len(ops) > 3*64 {
			ops = ops[:3*64]
		}
		ids := func() []int64 {
			l, err := queryIDs(ctx, db.Reader(), "SELECT id FROM folders ORDER BY id")
			require.NoError(t, err)
			return l
		}
		pick := func(l []int64, b byte, top bool) int64 {
			if top && int(b)%(len(l)+1) == len(l) {
				return 0
			}
			return l[int(b)%len(l)]
		}
		for i := 0; i+2 < len(ops); i += 3 {
			l := ids()
			op, a, b := ops[i]%5, ops[i+1], ops[i+2]
			name := fuzzNames[int(b)%len(fuzzNames)]
			var err error
			switch op {
			case 0: // create name inside folder a (or at the top)
				_, err = db.CreateFolder(ctx, name, pick(l, a, true), -1)
			case 1: // move folder a inside folder b (or to the top)
				parent := pick(l, b, true)
				_, err = db.UpdateFolder(ctx, pick(l, a, false), FolderPatch{Parent: &parent})
			case 2: // rename folder a
				_, err = db.UpdateFolder(ctx, pick(l, a, false), FolderPatch{Name: &name})
			case 3: // delete folder a
				_, err = db.DeleteFolder(ctx, pick(l, a, false))
			case 4: // resolve a Reader API path of up to three names
				parts := []string{fuzzNames[int(a)%len(fuzzNames)], name}
				if a&0x80 != 0 {
					parts = append(parts, fuzzNames[int(a>>3)%len(fuzzNames)])
				}
				err = db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
					_, err := resolveFolderPath(ctx, tx, strings.Join(parts, "/"))
					return err
				})
			}
			if err != nil && !FolderRefused(err) && !errors.Is(err, ErrFolderNotFound) && !errors.Is(err, ErrDefaultFolder) {
				t.Fatalf("op %d (%d %d %d): %v", i/3, op, a, b, err)
			}
			checkFolderInvariants(t, db.Reader())
		}
	})
}
