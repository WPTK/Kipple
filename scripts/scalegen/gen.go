package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

// Throwaway credentials of the generated database. They guard nothing: the data is synthetic and
// the server in the benchmark listens on the loopback address only.
const (
	benchUser     = "bench"
	benchPassword = "bench-password-1"
	benchAPIPass  = "bench-api-password-0123456789"
)

type genOpts struct {
	dir      string
	feeds    int
	items    int
	seed     int64
	schema   int // 0 = latest; otherwise rewind the finished database to this schema version (6 or 10)
	feedBase string
	days     int // days of stats history
}

type itemPlan struct {
	id      int64
	feed    int
	seq     int
	pub     int64
	read    bool
	starred bool
	idx     int
}

type feedPlan struct {
	id        int64
	title     string
	retention *int
	count     int
}

func runGen(args []string) error {
	var o genOpts
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	fs.StringVar(&o.dir, "dir", "", "data directory to create (kipple.db is written inside)")
	fs.IntVar(&o.feeds, "feeds", 500, "number of feeds")
	fs.IntVar(&o.items, "items", 150000, "number of items")
	fs.Int64Var(&o.seed, "seed", 1, "random seed (same seed, same data)")
	fs.IntVar(&o.schema, "schema", 0, "rewind the finished database to this schema version: 10 (the previous one) or 6 (an older shape); 0 keeps the latest")
	fs.StringVar(&o.feedBase, "feed-base", "http://127.0.0.1:1932", "base URL of `scalegen feeds`; feed N is <base>/feed/N.xml")
	fs.IntVar(&o.days, "stats-days", 400, "days of reading-statistics history")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.dir == "" {
		return fmt.Errorf("gen: -dir is required")
	}
	return generate(context.Background(), o)
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func generate(ctx context.Context, o genOpts) error {
	if err := os.MkdirAll(o.dir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(o.dir, "kipple.db")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("gen: %s exists; refusing to overwrite", path)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	db, err := store.Open(ctx, store.Options{Path: path, Logger: log})
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()
	r := rand.New(rand.NewSource(o.seed)) // #nosec G404 -- deterministic fixture data, not security
	now := time.Now().Unix()
	started := time.Now()
	progress := func(f string, a ...any) {
		fmt.Fprintf(os.Stderr, "[%6.1fs] %s\n", time.Since(started).Seconds(), fmt.Sprintf(f, a...))
	}

	if _, _, err := setup.CreateAccount(ctx, db, setup.NewAccount{Username: benchUser, Password: benchPassword,
		APIPassword: benchAPIPass, AuthMode: store.AuthStandard, CreatedVia: store.CreatedViaEnv}); err != nil {
		return err
	}

	// Folders: 24 plus the default one.
	folderIDs := []int64{1}
	for i := 0; i < 24; i++ {
		f, err := db.CreateFolder(ctx, fmt.Sprintf("Folder %02d", i+1), int64(i+1))
		if err != nil {
			return err
		}
		folderIDs = append(folderIDs, f.ID)
	}

	// Feed plan. Counts sit at the retention cap for most feeds (a steady-state library) and
	// below it for the rest; the last class (unlimited) absorbs the difference to the item total.
	plans := make([]feedPlan, o.feeds)
	total := 0
	for i := range plans {
		var capN, count int
		switch c := float64(i) / float64(o.feeds); {
		case c < 0.66:
			capN = 250
		case c < 0.86:
			capN = 500
			v := 500
			plans[i].retention = &v
		case c < 0.96:
			capN = 1000
			v := 1000
			plans[i].retention = &v
		default:
			capN = 0
			v := 0
			plans[i].retention = &v
		}
		switch {
		case capN == 0:
			count = 200 + r.Intn(600)
		case r.Float64() < 0.6:
			count = capN
		default:
			count = 15 + r.Intn(capN-15)
		}
		plans[i].count = count
		total += count
	}
	// Scale the unlimited feeds so the library has exactly o.items items.
	var unl []int
	rest := 0
	for i, p := range plans {
		if p.retention != nil && *p.retention == 0 {
			unl = append(unl, i)
		} else {
			rest += p.count
		}
	}
	if len(unl) > 0 && o.items > rest {
		sum := 0
		for _, i := range unl {
			sum += plans[i].count
		}
		left := o.items - rest
		got := 0
		for _, i := range unl {
			plans[i].count = plans[i].count * left / sum
			got += plans[i].count
		}
		plans[unl[0]].count += left - got
	} else {
		// Fewer items requested than the shape: scale every feed down.
		scale := float64(o.items) / float64(total)
		got := 0
		for i := range plans {
			plans[i].count = max(1, int(float64(plans[i].count)*scale))
			got += plans[i].count
		}
		plans[0].count += o.items - got
	}
	tg := newTextGen()
	farFuture := now + 30*86400
	for i := range plans {
		plans[i].title = fmt.Sprintf("Feed %03d %s", i+1, tg.words[r.Intn(2000)])
		folder := folderIDs[0]
		if r.Float64() < 0.85 {
			folder = folderIDs[1+int(math.Min(23, math.Abs(r.NormFloat64())*7))]
		}
		id, err := db.AddFeed(ctx, store.NewFeed{URL: fmt.Sprintf("%s/feed/%d.xml", o.feedBase, i+1),
			FolderID: folder, Retention: plans[i].retention, NextFetchAt: farFuture})
		if err != nil {
			return err
		}
		plans[i].id = id
	}
	if err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, p := range plans {
			if _, err := tx.ExecContext(ctx, `UPDATE feeds SET title = ?, site_url = ?, last_fetch_at = ?, last_success_at = ?,
				last_new_items_at = ?, last_status = 200 WHERE id = ?`,
				p.title, "https://example.com/site/"+fmt.Sprint(p.id), now-3600, now-3600, now-7200, p.id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	progress("%d feeds in 25 folders", len(plans))

	// Item plan: metadata only; content is generated per item from its own seed at insert time.
	nowMicro := time.Now().UnixMicro()
	var items []itemPlan
	used := map[int64]bool{}
	for fi, p := range plans {
		mean := math.Exp(math.Log(2*3600) + r.Float64()*(math.Log(5*86400)-math.Log(2*3600))) // seconds between items
		mean = math.Min(mean, 3*365*86400/float64(p.count))
		t := float64(now) - r.Float64()*48*3600
		for s := p.count; s >= 1; s-- {
			pub := int64(t)
			id := pub*1_000_000 + int64(r.Intn(1_000_000))
			for used[id] {
				id++
			}
			used[id] = true
			if id > nowMicro {
				id = nowMicro - int64(len(used))
			}
			age := now - pub
			readP := 0.35
			if age > 30*86400 {
				readP = 0.92
			}
			items = append(items, itemPlan{id: id, feed: fi, seq: s, pub: pub,
				read: r.Float64() < readP, starred: r.Float64() < 0.015})
			t -= r.ExpFloat64() * mean
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })
	for i := range items {
		items[i].idx = i
	}
	rareAt := map[int]bool{}
	for len(rareAt) < rareDocs {
		rareAt[r.Intn(len(items))] = true
	}
	progress("planned %d items", len(items))

	const batch = 1000
	for lo := 0; lo < len(items); lo += batch {
		hi := min(lo+batch, len(items))
		err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			ins, err := tx.PrepareContext(ctx, `INSERT INTO items (id, feed_id, read, starred, read_at, starred_at, state_changed_at,
				published_at, updated_at, sort_at, word_count, uid, content_hash, text_hash, url, title, author, image_url)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			defer ins.Close()
			cins, err := tx.PrepareContext(ctx, `INSERT INTO item_content (item_id, content_html, content_text) VALUES (?,?,?)`)
			if err != nil {
				return err
			}
			defer cins.Close()
			for _, it := range items[lo:hi] {
				ir := rand.New(rand.NewSource(o.seed*1_000_003 + int64(it.idx))) // #nosec G404 -- fixture data
				z := rand.NewZipf(ir, 1.07, 1, uint64(len(tg.words)-1))
				var words int
				switch x := ir.Float64(); {
				case x < 0.5:
					words = 30 + ir.Intn(170)
				case x < 0.9:
					words = 300 + ir.Intn(600)
				default:
					words = 1000 + ir.Intn(2500)
				}
				plant := ""
				if rareAt[it.idx] {
					plant = rareTerm
				} else if ir.Intn(midEvery) == 0 {
					plant = midTerm
				}
				html, text, n := tg.article(ir, z, words, plant)
				title := tg.title(ir, z)
				p := plans[it.feed]
				var img any
				if ir.Float64() < 0.7 {
					img = fmt.Sprintf("https://example.com/img/%d.jpg", it.id)
				}
				var readAt, starAt, changed any
				if it.read {
					v := min(it.pub+int64(ir.Intn(86400*3)), now)
					readAt, changed = v, v
				}
				if it.starred {
					v := min(it.pub+int64(ir.Intn(86400*5)), now)
					starAt, changed = v, v
				}
				if _, err := ins.ExecContext(ctx, it.id, p.id, boolI(it.read), boolI(it.starred), readAt, starAt, changed,
					it.pub, nil, it.pub, n, "g:"+fetch.H(fmt.Sprintf("n-%d-%d", it.feed+1, it.seq)),
					fetch.ContentHash(title, fmt.Sprintf("https://example.com/%d/%d", p.id, it.id), "", html), fetch.TextHash(title, text),
					fmt.Sprintf("https://example.com/%d/%d", p.id, it.id), title, "", img); err != nil {
					return err
				}
				if _, err := cins.ExecContext(ctx, it.id, html, text); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if (lo/batch)%25 == 24 {
			progress("%d/%d items", hi, len(items))
		}
	}
	progress("items done")

	// Trimmed-item ledger with restorable content (retention.restore_days): 4% of the item count.
	nTrim := len(items) / 25
	if err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for i := 0; i < nTrim; i++ {
			p := plans[r.Intn(len(plans))]
			id := int64(i+1) * 1000 // far below every item id (those are microsecond timestamps)
			pub := now - 400*86400 - int64(i)
			if _, err := tx.ExecContext(ctx, `INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at) VALUES (?,?,?,?,?,?)`,
				id, p.id, fmt.Sprintf("t-%d", i), r.Intn(2), now-int64(r.Intn(80*86400)), now-int64(r.Intn(80*86400))); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO trimmed_content (id, published_at, sort_at, word_count, content_hash, text_hash,
				url, title, author, content_html, content_text) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				id, pub, pub, 120, hash(fmt.Sprint(i)), hash(fmt.Sprint(i, "t")), "https://example.com/t/"+fmt.Sprint(i),
				"Trimmed "+fmt.Sprint(i), "Author", "<p>"+fmt.Sprint(i)+"</p>", fmt.Sprint(i)); err != nil {
				return err
			}
		}
		// fetch_log: 50 rows per feed (the retention floor).
		for _, p := range plans {
			for k := 0; k < 50; k++ {
				if _, err := tx.ExecContext(ctx, `INSERT INTO fetch_log (feed_id, trigger, started_at, duration_ms, outcome, http_status, new_items, bytes)
					VALUES (?, 'scheduled', ?, ?, 'ok', 200, ?, ?)`, p.id, now-int64(k)*1800, 80+r.Intn(400), r.Intn(3), 20000+r.Intn(60000)); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	progress("ledger and fetch log")

	// Reading-statistics history.
	kinds := []struct {
		k string
		w float64
	}{{"open", .40}, {"read_time", .35}, {"scroll", .15}, {"open_original", .05}, {"star", .03}, {"unstar", .01}, {"share", .01}}
	clients := []string{"web", "web", "web", "pwa", "reeder", "netnewswire", "api"}
	nStats := 0
	if err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, inferred,
			item_id, feed_id, feed_title, folder_id, folder_name, item_title, item_url, value, session_key) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for d := o.days; d >= 1; d-- {
			day := time.Unix(now, 0).UTC().AddDate(0, 0, -d)
			n := 40 + r.Intn(160)
			for e := 0; e < n; e++ {
				ts := time.Date(day.Year(), day.Month(), day.Day(), 6+r.Intn(17), r.Intn(60), r.Intn(60), 0, time.UTC)
				x, kind := r.Float64(), "open"
				for _, k := range kinds {
					if x -= k.w; x < 0 {
						kind = k.k
						break
					}
				}
				it := items[r.Intn(len(items))]
				var val any
				switch kind {
				case "read_time":
					val = 5 + r.Intn(300)
				case "scroll":
					val = 10 + r.Intn(91)
				}
				if _, err := st.ExecContext(ctx, ts.Unix(), ts.Format("2006-01-02"), ts.Hour(), int(ts.Weekday()), kind,
					clients[r.Intn(len(clients))], 0, it.id, plans[it.feed].id, plans[it.feed].title, nil, nil,
					"Item "+fmt.Sprint(it.idx), "https://example.com/"+fmt.Sprint(it.id), val,
					fmt.Sprintf("s%d-%d", d, e/12)); err != nil {
					return err
				}
				nStats++
			}
		}
		return nil
	}); err != nil {
		return err
	}
	progress("%d stats events", nStats)

	if err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "ANALYZE")
		return err
	}); err != nil {
		return err
	}
	closed = true
	if err := db.Close(); err != nil { // checkpoints and truncates the WAL
		return err
	}
	if o.schema != 0 {
		if err := rewind(path, o.schema); err != nil {
			return err
		}
		progress("rewound to schema %d", o.schema)
	}
	st, _ := os.Stat(path)
	progress("done: %s, %.0f MB", path, float64(st.Size())/1e6)
	return nil
}

func boolI(b bool) int {
	if b {
		return 1
	}
	return 0
}

// rewind turns a finished database into one the migrations from schema v up produce the latest
// shape from. 10 only restores the settings rows 0011 deletes; 6 also drops what 0007 to 0009
// added, so those migrations do their real work (a full items UPDATE and three stats indexes).
func rewind(path string, v int) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(ON)")
	if err != nil {
		return err
	}
	defer db.Close()
	var stmts []string
	switch v {
	case 10:
		stmts = []string{
			`INSERT OR IGNORE INTO settings (key, value) VALUES ('security.open_lan', 'true'), ('ui.font_size', '16'), ('sys.legacy_port', 'true')`,
		}
	case 6:
		stmts = []string{
			`DROP INDEX idx_items_state_changed`, `ALTER TABLE items DROP COLUMN state_changed_at`,
			`DROP INDEX idx_stats_event`, `ALTER TABLE stats_events DROP COLUMN event_id`,
			`DROP INDEX idx_stats_open_cov`, `DROP INDEX idx_stats_rt_cov`, `DROP INDEX idx_stats_scroll_cov`,
		}
	default:
		return fmt.Errorf("gen: -schema must be 6 or 10")
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("rewind %q: %w", s, err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		return err
	}
	_, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// runRewind is `scalegen rewind -db FILE -schema N`: the same rewind gen -schema does, on a copy of a database.
func runRewind(args []string) error {
	fs := flag.NewFlagSet("rewind", flag.ExitOnError)
	db := fs.String("db", "", "database file to rewind in place")
	v := fs.Int("schema", 10, "schema version to rewind to (6 or 10)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return fmt.Errorf("rewind: -db is required")
	}
	return rewind(*db, *v)
}
