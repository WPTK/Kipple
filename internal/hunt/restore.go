// Package hunt builds test inputs for the 1.1 bug hunt: SQLite files for the restore check and
// valid-but-weird OPML, Atom and RSS documents. Each generator is a pure function of a seed, so a
// fuzz target can mutate the seed (and a few knobs) instead of raw bytes and still reach deep code.
// Nothing here is imported by the kipple binary; it is for tests and scripts/huntgen only.
package hunt

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/WPTK/kipple/internal/store"

	_ "modernc.org/sqlite" // the driver the store package registers
)

// Want is what CheckRestore must do with a case.
type Want int

const (
	WantAny    Want = iota // no verdict asserted, only that it returns (no panic, no hang)
	WantAccept             // a healthy database of a version this binary knows
	WantReject             // an error, never a panic or a hang
)

// RestoreCase is one file for `kipple restore` to check. WAL, when set, is written beside it as <name>-wal.
type RestoreCase struct {
	Name string
	DB   []byte
	WAL  []byte
	Want Want
}

// WriteTo writes the case into dir as kipple.db (and kipple.db-wal) and returns the database path.
func (c RestoreCase) WriteTo(dir string) (string, error) {
	p := filepath.Join(dir, "kipple.db")
	if err := os.WriteFile(p, c.DB, 0o600); err != nil {
		return "", err
	}
	if c.WAL != nil {
		if err := os.WriteFile(p+"-wal", c.WAL, 0o600); err != nil {
			return "", err
		}
	}
	return p, nil
}

type baseKey struct {
	version, pageSize int
	edit              string
}

var (
	baseMu    sync.Mutex
	baseCache = map[baseKey][]byte{}
)

// ValidDB returns the bytes of a database at schema version with the given page size, as a Kipple of that
// version leaves it (journal mode DELETE, so a single file).
func ValidDB(ctx context.Context, version, pageSize int) ([]byte, error) {
	return buildDB(ctx, version, pageSize, "", nil)
}

// buildDB is ValidDB with an edit applied to the open database before it is read back. name keys the cache.
func buildDB(ctx context.Context, version, pageSize int, name string, edit func(*sql.DB) error) ([]byte, error) {
	k := baseKey{version, pageSize, name}
	baseMu.Lock()
	b, ok := baseCache[k]
	baseMu.Unlock()
	if ok {
		return append([]byte(nil), b...), nil
	}
	dir, err := os.MkdirTemp("", "kipple-hunt-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "k.db")
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=page_size(%d)&_pragma=journal_mode(DELETE)&_pragma=foreign_keys(OFF)", filepath.ToSlash(path), pageSize))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	err = store.BuildSchema(ctx, db, version)
	if err == nil && edit != nil {
		err = edit(db)
	}
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	b, err = os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	baseMu.Lock()
	baseCache[k] = b
	baseMu.Unlock()
	return append([]byte(nil), b...), nil
}

// Fields of the SQLite file header (https://sqlite.org/fileformat2.html#the_database_header).
const (
	offPageSize    = 16
	offChangeCount = 24
	offPageCount   = 28
	offEncoding    = 56
	offUserVersion = 60
	offAppID       = 68
	offValidFor    = 92
)

func put32(b []byte, off int, v uint32) {
	if len(b) >= off+4 {
		binary.BigEndian.PutUint32(b[off:], v)
	}
}

// shadowTables lists the FTS5 shadow tables of the items search index.
func shadowTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'items\_fts\_%' ESCAPE '\'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// insertFeedAndItems adds a feed with n items, so the search index and the item tables are not empty.
func insertFeedAndItems(db *sql.DB, n int) error {
	if _, err := db.Exec(`INSERT INTO feeds (url, url_key, host, title) VALUES ('https://rss.example.com/f', 'rss.example.com/f', 'rss.example.com', 'F')`); err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO items (feed_id, published_at, sort_at, uid, content_hash, text_hash, title) VALUES (1, ?, ?, ?, 'h', 't', ?)`,
			1000+i, 1000+i, fmt.Sprintf("u%d", i), fmt.Sprintf("title %d about gardens", i)); err != nil {
			return err
		}
	}
	return nil
}

// RestoreCases is the fixed set for the restore check: a valid database at each schema version and the
// mutations of the latest one that the plan lists (truncated, header-corrupt, WAL-only, wrong page size,
// huge page count, FTS shadow table damage, foreign-key violations, future schema version).
func RestoreCases(ctx context.Context) ([]RestoreCase, error) {
	latest := store.LatestVersion()
	var out []RestoreCase
	add := func(name string, want Want, db []byte, wal []byte) {
		out = append(out, RestoreCase{Name: name, DB: db, WAL: wal, Want: want})
	}
	for v := 1; v <= latest; v++ {
		b, err := ValidDB(ctx, v, 4096)
		if err != nil {
			return nil, err
		}
		w := WantAny
		if v == latest {
			w = WantAccept
		}
		add(fmt.Sprintf("valid-v%d", v), w, b, nil)
	}
	good, err := ValidDB(ctx, latest, 4096)
	if err != nil {
		return nil, err
	}
	mut := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}

	for _, n := range []int{0, 1, 99, 100, 4095, 4096, 4097, len(good) / 2, len(good) - 1} {
		w := WantReject
		if n == len(good)-1 {
			w = WantAny // SQLite reads a short last page as zeros; the integrity check decides, not the size
		}
		add(fmt.Sprintf("truncated-%d", n), w, good[:n], nil)
	}
	add("truncated-page-aligned", WantReject, good[:len(good)-4096], nil)
	add("header-zeroed", WantReject, mut(func(b []byte) { clear(b[:100]) }), nil)
	add("header-magic", WantReject, mut(func(b []byte) { copy(b, "SQLite format 4\x00") }), nil)
	add("header-appid", WantReject, mut(func(b []byte) { put32(b, offAppID, 0) }), nil)
	for _, ps := range []uint16{0, 3, 256, 511, 513, 1000} {
		add(fmt.Sprintf("header-pagesize-%d", ps), WantReject, mut(func(b []byte) { binary.BigEndian.PutUint16(b[offPageSize:], ps) }), nil)
	}
	add("header-encoding-9", WantAny, mut(func(b []byte) { put32(b, offEncoding, 9) }), nil)
	add("header-text-garbage", WantReject, mut(func(b []byte) {
		for i := 100; i < 200; i++ {
			b[i] ^= 0xA5
		}
	}), nil)
	add("page-size-claims-8192", WantReject, mut(func(b []byte) { binary.BigEndian.PutUint16(b[offPageSize:], 8192) }), nil)
	add("future-schema", WantReject, mut(func(b []byte) { put32(b, offUserVersion, uint32(latest+1)) }), nil)
	add("schema-zero", WantReject, mut(func(b []byte) { put32(b, offUserVersion, 0) }), nil)
	add("schema-negative", WantReject, mut(func(b []byte) { put32(b, offUserVersion, 0xFFFFFFFF) }), nil)
	add("huge-page-count", WantReject, mut(func(b []byte) {
		put32(b, offChangeCount, 7)
		put32(b, offValidFor, 7) // SQLite trusts the page count only when these two agree
		put32(b, offPageCount, 0x7FFFFFFF)
	}), nil)
	add("zero-page-count", WantAny, mut(func(b []byte) { put32(b, offPageCount, 0) }), nil)
	for _, ps := range []int{512, 65536} {
		b, err := ValidDB(ctx, latest, ps)
		if err != nil {
			return nil, err
		}
		add(fmt.Sprintf("valid-pagesize-%d", ps), WantAny, b, nil)
	}

	// WAL-only: no database, a WAL with a valid header; and a healthy database with a WAL beside it.
	wal := make([]byte, 32+24+4096)
	binary.BigEndian.PutUint32(wal, 0x377f0682)
	binary.BigEndian.PutUint32(wal[4:], 3007000)
	binary.BigEndian.PutUint32(wal[8:], 4096)
	add("wal-only", WantReject, nil, wal)
	add("wal-only-junk", WantReject, []byte{}, []byte("not a wal"))
	add("valid-with-wal", WantAny, good, wal)

	// FTS shadow table damage and foreign-key violations need SQL.
	fts := func(name string, edit func(db *sql.DB, table string) error) error {
		b, err := buildDB(ctx, latest, 4096, name, func(db *sql.DB) error {
			if err := insertFeedAndItems(db, 20); err != nil {
				return err
			}
			tabs, err := shadowTables(db)
			if err != nil {
				return err
			}
			for _, t := range tabs {
				if err := edit(db, t); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		add(name, WantReject, b, nil)
		return nil
	}
	if err := fts("fts-data-garbage", func(db *sql.DB, t string) error {
		if !strings.HasSuffix(t, "_data") {
			return nil
		}
		_, err := db.Exec(fmt.Sprintf(`UPDATE %q SET block = randomblob(40) WHERE id > 1`, t))
		return err
	}); err != nil {
		return nil, err
	}
	if err := fts("fts-shadow-emptied", func(db *sql.DB, t string) error {
		if strings.HasSuffix(t, "_config") {
			return nil
		}
		_, err := db.Exec(fmt.Sprintf(`DELETE FROM %q`, t))
		return err
	}); err != nil {
		return nil, err
	}
	b, err := buildDB(ctx, latest, 4096, "fk-violation", func(db *sql.DB) error {
		if err := insertFeedAndItems(db, 3); err != nil {
			return err
		}
		_, err := db.Exec(`UPDATE items SET feed_id = 424242 WHERE uid = 'u1'`)
		return err
	})
	if err != nil {
		return nil, err
	}
	add("fk-violation", WantReject, b, nil)
	return out, nil
}

// RestoreKnobs are the inputs of RestoreFromSeed besides the seed: which schema version, page size and
// mutation, each reduced into range, so any values a fuzzer picks are valid.
type RestoreKnobs struct {
	Version  uint8
	PageSize uint8
	Mutation uint8
	Flips    uint8
}

var pageSizes = []int{512, 1024, 2048, 4096, 8192, 16384, 32768, 65536}

// RestoreFromSeed builds one restore file from a seed: a valid database at a version and page size the knobs
// pick, then Mutation chooses a truncation, header flips, flips anywhere, a version or page count edit, a
// noise page or a WAL beside it. The same inputs give the same bytes.
func RestoreFromSeed(ctx context.Context, seed uint64, k RestoreKnobs) (RestoreCase, error) {
	r := rand.New(rand.NewSource(int64(seed))) // #nosec G404 -- deterministic test data
	v := 1 + int(k.Version)%store.LatestVersion()
	ps := pageSizes[int(k.PageSize)%len(pageSizes)]
	b, err := ValidDB(ctx, v, ps)
	if err != nil {
		return RestoreCase{}, err
	}
	c := RestoreCase{Name: fmt.Sprintf("seed-%d-v%d-ps%d-m%d", seed, v, ps, k.Mutation%8), Want: WantAny}
	switch k.Mutation % 8 {
	case 0: // untouched
	case 1:
		b = b[:r.Intn(len(b)+1)]
	case 2: // flips in the 100-byte header
		for i := 0; i <= int(k.Flips)%16; i++ {
			b[r.Intn(100)] ^= byte(1 + r.Intn(255))
		}
	case 3: // flips anywhere
		for i := 0; i <= int(k.Flips)%64; i++ {
			b[r.Intn(len(b))] ^= byte(1 + r.Intn(255))
		}
	case 4:
		put32(b, offUserVersion, r.Uint32())
	case 5:
		put32(b, offPageCount, r.Uint32())
	case 6: // a page overwritten with noise
		p := r.Intn(len(b)/ps) * ps
		for i := 0; i < ps && p+i < len(b); i++ {
			b[p+i] = byte(r.Intn(256))
		}
	case 7: // junk WAL beside it
		c.WAL = make([]byte, r.Intn(8192))
		r.Read(c.WAL)
	}
	c.DB = b
	return c, nil
}

// TinySeedBytes is how much of the front of a file a committed seed keeps: the 100-byte header and the start
// of the schema page, enough for the fuzzer to mutate from without storing a whole database.
const TinySeedBytes = 1024

// TinySeeds returns the committed raw seeds (name to bytes, each at most TinySeedBytes): the front of a
// healthy file and of the header-damaged and version-damaged ones. Regenerate with `go run ./scripts/huntgen seeds`.
func TinySeeds(ctx context.Context) (map[string][]byte, error) {
	cs, err := RestoreCases(ctx)
	if err != nil {
		return nil, err
	}
	keep := map[string]string{
		fmt.Sprintf("valid-v%d", store.LatestVersion()): "valid-latest", "valid-v1": "valid-v1", "header-zeroed": "header-zeroed",
		"header-magic": "header-magic", "header-appid": "header-appid", "header-pagesize-511": "header-pagesize-511",
		"future-schema": "future-schema", "huge-page-count": "huge-page-count", "header-text-garbage": "header-text-garbage",
	}
	out := map[string][]byte{}
	for _, c := range cs {
		if n, ok := keep[c.Name]; ok {
			out[n] = c.DB[:min(len(c.DB), TinySeedBytes)]
		}
	}
	return out, nil
}
