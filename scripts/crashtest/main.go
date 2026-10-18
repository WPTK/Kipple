// Command crashtest kills a Kipple database writer at random moments and checks what a restart finds.
//
//	go run ./scripts/crashtest [-rounds 200] [-dir DIR] [-max-ms 1500] [-seed 1]
//
// Each round starts this program again as a child that opens the database with the store package and commits
// feeds with items (some rounds with a checkpoint in between) in a loop, printing the id of every item once its
// transaction has committed. The parent kills the child after a random time with no warning, then reopens the
// database and checks: integrity_check says ok, the journal mode is wal, no foreign key is violated, every item
// the child reported as committed is there with its text and its feed, and no write the child never started
// appears. The same database file is used for every round, so a restart is also an open after many kills. A
// run is by hand on the dev machine and is not part of CI. The output is one line per failure and a final
// verdict line; the exit status is 1 when any round failed.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

func main() {
	rounds := flag.Int("rounds", 200, "how many kills")
	dir := flag.String("dir", "", "directory of the database (default: a new temp directory, removed on a pass)")
	maxMS := flag.Int("max-ms", 1500, "the longest a child runs before it is killed")
	seed := flag.Int64("seed", 1, "seed of the kill times")
	child := flag.String("child", "", "internal: run as the writer on this database file")
	flag.Parse()
	if *child != "" {
		writer(*child)
		return
	}
	if err := parent(*rounds, *dir, *maxMS, *seed); err != nil {
		fmt.Println("FAIL", err)
		os.Exit(1)
	}
}

func open(path string) (*store.DB, error) {
	return store.Open(context.Background(), store.Options{Path: path, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// writer commits batches until it is killed (it only returns by exiting with status 1 on an error). It prints "ack <first> <last>" after each commit.
func writer(path string) {
	die := func(err error) {
		fmt.Fprintln(os.Stderr, "writer:", err)
		os.Exit(1)
	}
	db, err := open(path)
	if err != nil {
		die(err)
	}
	ctx := context.Background()
	nonce := time.Now().UnixNano() // process ids are reused, so names carry a per-run value
	rng := rand.New(rand.NewSource(nonce))
	out := bufio.NewWriter(os.Stdout)
	for n := 0; ; n++ {
		var first, last int64
		err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			u := fmt.Sprintf("https://crash%d-%d.example/feed", nonce, n)
			r, err := tx.ExecContext(ctx, `INSERT INTO feeds (folder_id, url, url_key, host, title, site_url, next_fetch_at)
				VALUES (1, ?, ?, 'x.example', ?, 'https://x.example/', 4102444800)`, u, u, fmt.Sprintf("Crash %d", n))
			if err != nil {
				return err
			}
			feed, _ := r.LastInsertId()
			for k := 0; k < 1+rng.Intn(40); k++ {
				text := fmt.Sprintf("crash body %d-%d-%d %s", nonce, n, k, strings.Repeat("x", rng.Intn(4000)))
				r, err := tx.ExecContext(ctx, `INSERT INTO items (feed_id, read, starred, published_at, sort_at, word_count, uid, content_hash, text_hash, url, title, author)
					VALUES (?, 0, 0, ?, ?, 1, ?, 'c', 't', ?, ?, 'Ann')`, feed, 1000+n, 1000+n, fmt.Sprintf("g:%d:%d:%d", nonce, n, k),
					fmt.Sprintf("https://x.example/%d/%d/%d", nonce, n, k), "crash "+strconv.Itoa(n))
				if err != nil {
					return err
				}
				id, _ := r.LastInsertId()
				if _, err := tx.ExecContext(ctx, `INSERT INTO item_content (item_id, content_html, content_text) VALUES (?, ?, ?)`, id, "<p>"+text+"</p>", text); err != nil {
					return err
				}
				if first == 0 {
					first = id
				}
				last = id
			}
			return nil
		})
		if err != nil {
			die(err)
		}
		fmt.Fprintf(out, "ack %d %d\n", first, last)
		_ = out.Flush()
		time.Sleep(time.Duration(rng.Intn(20)) * time.Millisecond) // paced so a run stays small and the kill lands between and inside commits
		if n%7 == 3 {
			if _, err := db.CheckpointPassive(ctx); err != nil {
				die(err)
			}
		}
	}
}

func parent(rounds int, dir string, maxMS int, seed int64) error {
	temp := dir == ""
	if temp {
		d, err := os.MkdirTemp("", "kipple-crash-")
		if err != nil {
			return err
		}
		dir = d
	}
	path := filepath.Join(dir, "kipple.db")
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	rng := rand.New(rand.NewSource(seed))
	var acked [][2]int64 // committed id ranges, kept across rounds
	failures := 0
	fail := func(round int, format string, args ...any) {
		failures++
		fmt.Printf("FAIL round %d: %s\n", round, fmt.Sprintf(format, args...))
	}
	for round := 1; round <= rounds; round++ {
		cmd := exec.Command(exe, "-child", path)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan [][2]int64, 1)
		go func() {
			var got [][2]int64
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				var a, b int64
				if _, err := fmt.Sscanf(sc.Text(), "ack %d %d", &a, &b); err == nil {
					got = append(got, [2]int64{a, b})
				}
			}
			done <- got
		}()
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		select {
		case err := <-exited:
			return fmt.Errorf("round %d: the writer ended by itself before it was killed: %v: %s (database kept in %s)", round, err, strings.TrimSpace(stderr.String()), dir)
		case <-time.After(time.Duration(50+rng.Intn(maxMS)) * time.Millisecond):
		}
		_ = cmd.Process.Kill()
		<-exited
		acked = append(acked, <-done...)
		if msg := verify(path, acked, round); msg != "" {
			fail(round, "%s", msg)
			if failures >= 5 {
				break
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "kipple.db*"))
	if failures == 0 && len(acked) == 0 {
		return fmt.Errorf("no writer committed anything in %d rounds, so nothing was checked (database kept in %s)", rounds, dir)
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d rounds failed (database kept in %s)", failures, rounds, dir)
	}
	fmt.Printf("PASS %d kills, %d committed batches found intact (files: %d)\n", rounds, len(acked), len(files))
	if temp {
		_ = os.RemoveAll(dir) // a passing run leaves nothing behind; a failing one keeps the file
	}
	return nil
}

// verify reopens path the way a restart does and returns what is wrong with it, "" when nothing is.
func verify(path string, acked [][2]int64, rounds int) string {
	db, err := open(path)
	if err != nil {
		return "reopen: " + err.Error()
	}
	defer db.Close()
	q := db.Reader()
	var s string
	if err := q.QueryRow("PRAGMA integrity_check").Scan(&s); err != nil || s != "ok" {
		return fmt.Sprintf("integrity_check: %q %v", s, err)
	}
	if err := q.QueryRow("PRAGMA journal_mode").Scan(&s); err != nil || s != "wal" {
		return fmt.Sprintf("journal_mode: %q %v", s, err)
	}
	rows, err := q.Query("PRAGMA foreign_key_check")
	if err != nil {
		return "foreign_key_check: " + err.Error()
	}
	bad := rows.Next()
	rows.Close()
	if bad {
		return "a foreign key is violated"
	}
	for _, r := range acked {
		var n int64
		if err := q.QueryRow(`SELECT count(*) FROM items i JOIN item_content c ON c.item_id = i.id JOIN feeds f ON f.id = i.feed_id WHERE i.id BETWEEN ? AND ?`, r[0], r[1]).Scan(&n); err != nil {
			return "count: " + err.Error()
		}
		if n != r[1]-r[0]+1 {
			return fmt.Sprintf("a committed batch of items %d..%d holds %d rows", r[0], r[1], n)
		}
	}
	// What the table holds beyond the acknowledged batches is at most one batch per round (up to 40 items), the one that was committing when
	// the child died (its acknowledgement was never read; the batch is whole or absent).
	var total, want int64
	if err := q.QueryRow("SELECT count(*) FROM items").Scan(&total); err != nil {
		return "count: " + err.Error()
	}
	for _, r := range acked {
		want += r[1] - r[0] + 1
	}
	if total < want || total > want+40*int64(rounds) {
		return fmt.Sprintf("the table holds %d items, the acknowledged batches hold %d (%d rounds)", total, want, rounds)
	}
	var orphans int64
	if err := q.QueryRow("SELECT count(*) FROM items i WHERE NOT EXISTS (SELECT 1 FROM item_content c WHERE c.item_id = i.id)").Scan(&orphans); err != nil || orphans != 0 {
		return fmt.Sprintf("%d items without content (%v)", orphans, err)
	}
	return ""
}
