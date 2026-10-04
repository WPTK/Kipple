package main

import (
	"database/sql"
	"encoding/xml"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// runFeeds serves feed N at /feed/N.xml for the generated database. Every request to a feed
// returns 5 items the library has not seen, and the next request moves on by 5, so a refresh of
// all feeds ingests 5 new items per feed and, for a feed at its retention cap, leaves 5 to trim.
func runFeeds(args []string) error {
	fs := flag.NewFlagSet("feeds", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:1932", "listen address")
	dir := fs.String("dir", "", "data directory holding the generated kipple.db (read once at start)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("feeds: -dir is required")
	}
	counts, err := feedCounts(filepath.Join(*dir, "kipple.db"))
	if err != nil {
		return err
	}
	srv := newFeedServer(counts)
	fmt.Printf("serving %d feeds on http://%s\n", len(counts), *addr)
	hs := &http.Server{Addr: *addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	return hs.ListenAndServe()
}

// feedCounts returns the item count of each feed by index (the N of /feed/N.xml).
func feedCounts(db string) (map[int]int, error) {
	h, err := sql.Open("sqlite", "file:"+filepath.ToSlash(db)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer h.Close()
	rows, err := h.Query(`SELECT f.url, count(*) FROM items i JOIN feeds f ON f.id = i.feed_id GROUP BY f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[int]int{}
	for rows.Next() {
		var u string
		var n int
		if err := rows.Scan(&u, &n); err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(u[strings.LastIndex(u, "/")+1:], ".xml")
		if idx, err := strconv.Atoi(name); err == nil {
			counts[idx] = n
		}
	}
	return counts, rows.Err()
}

type feedServer struct {
	mu     sync.Mutex
	counts map[int]int
	hits   map[int]int
	tg     *textGen
	now    time.Time
}

func newFeedServer(counts map[int]int) *feedServer {
	return &feedServer{counts: counts, hits: map[int]int{}, tg: newTextGen(), now: time.Now()}
}

func (s *feedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/feed/"), ".xml")
	n, err := strconv.Atoi(name)
	if err != nil || !strings.HasPrefix(r.URL.Path, "/feed/") {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	base, ok := s.counts[n]
	if !ok {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	newest := base + 5*s.hits[n]
	s.hits[n]++
	s.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel>`)
	fmt.Fprintf(&b, "<title>Feed %d</title><link>https://example.com/site/%d</link><description>Synthetic feed</description>", n, n)
	for seq := newest + 1; seq <= newest+5; seq++ {
		if seq <= 0 {
			continue
		}
		ir := rand.New(rand.NewSource(int64(n)*1_000_003 + int64(seq))) // #nosec G404 -- fixture data
		z := rand.NewZipf(ir, 1.07, 1, uint64(len(s.tg.words)-1))
		_, text, _ := s.tg.article(ir, z, 80+ir.Intn(250), "")
		var x strings.Builder
		_ = xml.EscapeText(&x, []byte("<p>"+text+"</p>"))
		pub := s.now.Add(-time.Duration(newest+5-seq) * time.Hour)
		fmt.Fprintf(&b, "<item><title>%s</title><link>https://example.com/f/%d/%d</link><guid isPermaLink=\"false\">n-%d-%d</guid><pubDate>%s</pubDate><description>%s</description></item>",
			s.tg.title(ir, z), n, seq, n, seq, pub.Format(time.RFC1123Z), x.String())
	}
	b.WriteString(`</channel></rss>`)
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
