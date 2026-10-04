package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

type benchOpts struct {
	bin      string
	src      string // directory holding the generated kipple.db at the latest schema
	work     string
	runs     int
	port     int
	feedPort int
}

// metrics collects one value list per name, in first-seen order.
type metrics struct {
	order []string
	vals  map[string][]float64
	unit  map[string]string
}

func newMetrics() *metrics { return &metrics{vals: map[string][]float64{}, unit: map[string]string{}} }

func (m *metrics) add(name, unit string, v float64) {
	if _, ok := m.vals[name]; !ok {
		m.order = append(m.order, name)
		m.unit[name] = unit
	}
	m.vals[name] = append(m.vals[name], v)
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func (m *metrics) report(w io.Writer) {
	fmt.Fprintln(w, "| metric | unit | median | runs |")
	fmt.Fprintln(w, "|---|---|---:|---|")
	for _, n := range m.order {
		var parts []string
		for _, v := range m.vals[n] {
			parts = append(parts, strconv.FormatFloat(v, 'f', 1, 64))
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s |\n", n, m.unit[n], strconv.FormatFloat(median(m.vals[n]), 'f', 1, 64), strings.Join(parts, ", "))
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
func mb(b uint64) float64        { return float64(b) / (1 << 20) }

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fsize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func runBench(args []string) (err error) {
	var o benchOpts
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	fs.StringVar(&o.bin, "bin", "", "the kipple binary to measure")
	fs.StringVar(&o.src, "src", "", "directory holding the database made by `scalegen gen` (kept untouched)")
	fs.StringVar(&o.work, "work", "", "scratch directory (needs about 4x the database size free); removed at the end")
	fs.IntVar(&o.runs, "runs", 3, "runs; each metric is reported as the median")
	fs.IntVar(&o.port, "port", 1931, "port of the Kipple under test (port+2 is used for the restored copy)")
	fs.IntVar(&o.feedPort, "feed-port", 1932, "port of the synthetic feed server (the one `gen` wrote into the feed URLs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.bin == "" || o.src == "" || o.work == "" {
		return fmt.Errorf("bench: -bin, -src and -work are required")
	}
	if err := os.MkdirAll(o.work, 0o750); err != nil {
		return err
	}
	defer func() {
		if err == nil { // keep the scratch directory (and the server logs in it) when a run fails
			_ = os.RemoveAll(o.work)
		}
	}()

	m := newMetrics()
	src := filepath.Join(o.src, "kipple.db")
	m.add("db.file.after_generate", "MB", mb(uint64(fsize(src))))

	// Migration timing is in process: store.Open is what `kipple serve` runs first.
	for run := 1; run <= o.runs; run++ {
		for _, v := range []int{0, 11, 6} {
			if err := benchOpen(o, m, src, v, run); err != nil {
				return err
			}
		}
	}
	for run := 1; run <= o.runs; run++ {
		fmt.Fprintf(os.Stderr, "== run %d of %d\n", run, o.runs)
		if err := benchServer(o, m, src, run); err != nil {
			return err
		}
	}
	fmt.Printf("\nGo %s, %s/%s, %d logical CPUs\n\n", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	m.report(os.Stdout)
	return nil
}

// benchOpen times store.Open on a copy of the database: v = 0 is the current schema (the open
// itself), 11 the previous one and 6 an older one (see rewind).
func benchOpen(o benchOpts, m *metrics, src string, v, run int) error {
	dir := filepath.Join(o.work, fmt.Sprintf("open-%d-%d", v, run))
	path := filepath.Join(dir, "kipple.db")
	if err := copyFile(path, src); err != nil {
		return err
	}
	if v != 0 {
		if err := rewind(path, v); err != nil {
			return err
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	t := time.Now()
	db, err := store.Open(context.Background(), store.Options{Path: path, Logger: log})
	if err != nil {
		return err
	}
	open := time.Since(t)
	ver, _ := db.Version(context.Background())
	t = time.Now()
	if err := db.Close(); err != nil {
		return err
	}
	closeT := time.Since(t)
	name := "open.current_schema"
	if v != 0 {
		name = fmt.Sprintf("migrate.from_schema_%d_to_%d", v, ver)
	}
	m.add(name+".open_ms", "ms", ms(open))
	m.add(name+".close_ms", "ms", ms(closeT))
	if v != 0 {
		var snap int64
		matches, _ := filepath.Glob(filepath.Join(dir, "backup", "pre-migration-*.db"))
		for _, p := range matches {
			snap += fsize(p)
		}
		m.add(name+".snapshot_mb", "MB", mb(uint64(snap)))
	}
	return os.RemoveAll(dir)
}

// ---- running server ----

type server struct {
	cmd  *exec.Cmd
	dir  string
	log  *os.File
	http *http.Client
	base string
	auth string // Reader API token
}

func startServer(o benchOpts, dir string, port int) (*server, time.Duration, error) {
	lf, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		return nil, 0, err
	}
	cmd := exec.Command(o.bin, "serve") // #nosec G204 -- the binary under test, given by the operator
	cmd.Env = append(os.Environ(), fmt.Sprintf("KIPPLE_ADDR=127.0.0.1:%d", port), "KIPPLE_DATA="+dir)
	cmd.Stdout, cmd.Stderr = lf, lf
	jar, _ := cookiejar.New(nil)
	s := &server{cmd: cmd, dir: dir, log: lf, base: fmt.Sprintf("http://127.0.0.1:%d", port),
		http: &http.Client{Jar: jar, Timeout: 5 * time.Minute}}
	t := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	for {
		resp, err := http.Get(s.base + "/healthz") // #nosec G107 -- loopback address built from a flag
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return s, time.Since(t), nil
			}
		}
		if time.Since(t) > 20*time.Minute {
			_ = s.stop()
			return nil, 0, fmt.Errorf("server did not become ready")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (s *server) stop() error {
	_ = s.cmd.Process.Kill()
	_, _ = s.cmd.Process.Wait()
	_ = s.log.Close()
	return nil
}

func (s *server) req(method, path string, body []byte, ctype string, hdr map[string]string) (int, []byte, time.Duration, error) {
	r, err := http.NewRequest(method, s.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, 0, err
	}
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Kipple-Client", "web")
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	t := time.Now()
	resp, err := s.http.Do(r)
	if err != nil {
		return 0, nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, time.Since(t), err
}

func (s *server) login() error {
	code, b, _, err := s.req("POST", "/api/auth/login", []byte(`{"username":"`+benchUser+`","password":"`+benchPassword+`"}`), "application/json", nil)
	if err != nil || code/100 != 2 {
		return fmt.Errorf("web login: %d %s %v", code, b, err)
	}
	form := url.Values{"Email": {benchUser}, "Passwd": {benchAPIPass}}.Encode()
	code, b, _, err = s.req("POST", "/api/greader.php/accounts/ClientLogin", []byte(form), "application/x-www-form-urlencoded", nil)
	if err != nil || code != 200 {
		return fmt.Errorf("reader login: %d %s %v", code, b, err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "Auth="); ok {
			s.auth = strings.TrimSpace(v)
		}
	}
	return nil
}

// sampler records the peak resident size of a process and the largest WAL file while it runs.
type sampler struct {
	stop chan struct{}
	wg   sync.WaitGroup
	peak uint64
	wal  int64
}

func (s *server) sample() *sampler {
	sp := &sampler{stop: make(chan struct{})}
	sp.wg.Add(1)
	go func() {
		defer sp.wg.Done()
		for {
			if cur, _, err := rss(s.cmd.Process.Pid); err == nil && cur > sp.peak {
				sp.peak = cur
			}
			if w := fsize(filepath.Join(s.dir, "kipple.db-wal")); w > sp.wal {
				sp.wal = w
			}
			select {
			case <-sp.stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	return sp
}

func (sp *sampler) done() (peakBytes uint64, walBytes int64) {
	close(sp.stop)
	sp.wg.Wait()
	return sp.peak, sp.wal
}

func (s *server) rssNow() uint64 {
	cur, _, _ := rss(s.cmd.Process.Pid)
	return cur
}

type endpoint struct {
	name   string
	method string
	path   string
	body   string
	reader bool // Reader API call: authorised by header, not by cookie
}

const greader = "/api/greader.php/reader/api/0/"

// timeEndpoint calls an endpoint once (first: cold relative to this server process) and five more times (warm).
func (s *server) timeEndpoint(m *metrics, e endpoint) error {
	var times []time.Duration
	var size, refused int
	for i := 0; i < 6; i++ {
		hdr := map[string]string{}
		ctype := ""
		if e.reader {
			hdr["Authorization"] = "GoogleLogin auth=" + s.auth
		}
		if e.method == "POST" {
			ctype = "application/x-www-form-urlencoded"
		}
		code, b, d, err := s.req(e.method, e.path, []byte(e.body), ctype, hdr)
		if err != nil {
			return err
		}
		if code == 422 && strings.HasPrefix(e.name, "search.") {
			refused++ // search_too_broad: counted below, the time still counts
		} else if code != 200 {
			return fmt.Errorf("%s: HTTP %d %s", e.name, code, trunc(b))
		}
		size = len(b)
		times = append(times, d)
	}
	m.add("api."+e.name+".first_ms", "ms", ms(times[0]))
	var warm []float64
	for _, d := range times[1:] {
		warm = append(warm, ms(d))
	}
	m.add("api."+e.name+".warm_ms", "ms", median(warm))
	m.add("api."+e.name+".kb", "KB", float64(size)/1024)
	if strings.HasPrefix(e.name, "search.") {
		m.add("api."+e.name+".too_broad_of_6", "count", float64(refused))
	}
	return nil
}

func trunc(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

func jsonGet[T any](s *server, path string) (T, error) {
	var v T
	code, b, _, err := s.req("GET", path, nil, "", nil)
	if err != nil {
		return v, err
	}
	if code != 200 {
		return v, fmt.Errorf("%s: HTTP %d %s", path, code, trunc(b))
	}
	return v, json.Unmarshal(b, &v)
}

func countRows(dir, query string) int64 {
	h, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "kipple.db"))+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return -1
	}
	defer h.Close()
	var n int64
	if err := h.QueryRow(query).Scan(&n); err != nil {
		return -1
	}
	return n
}

// waitIdle polls /api/status until no run or fetch is active.
func (s *server) waitIdle(minWait time.Duration) error {
	t := time.Now()
	for {
		time.Sleep(200 * time.Millisecond)
		st, err := jsonGet[struct {
			Runs     []any `json:"runs"`
			Inflight int   `json:"inflight"`
		}](s, "/api/status")
		if err != nil {
			return err
		}
		if len(st.Runs) == 0 && st.Inflight == 0 && time.Since(t) > minWait {
			return nil
		}
		if time.Since(t) > 30*time.Minute {
			return fmt.Errorf("still busy after 30 minutes")
		}
	}
}

func benchServer(o benchOpts, m *metrics, src string, run int) error {
	dir := filepath.Join(o.work, fmt.Sprintf("serve-%d", run))
	if err := copyFile(filepath.Join(dir, "kipple.db"), src); err != nil {
		return err
	}
	// The feed server for the refresh, on the address the feed URLs name.
	counts, err := feedCounts(filepath.Join(dir, "kipple.db"))
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", o.feedPort))
	if err != nil {
		return err
	}
	fsrv := &http.Server{Handler: newFeedServer(counts), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = fsrv.Serve(ln) }()
	defer fsrv.Close()

	// Cold start: the database was just copied, so the OS file cache is warm; see docs/performance.md.
	s, startup, err := startServer(o, dir, o.port)
	if err != nil {
		return err
	}
	defer s.stop()
	m.add("start.ready_ms", "ms", ms(startup))
	if err := s.login(); err != nil {
		return err
	}
	time.Sleep(10 * time.Second)
	m.add("rss.idle_mb", "MB", mb(s.rssNow()))
	m.add("db.wal.idle_mb", "MB", mb(uint64(fsize(filepath.Join(dir, "kipple.db-wal")))))

	boot, err := jsonGet[struct {
		Feeds []struct {
			ID string `json:"id"`
		} `json:"feeds"`
		Folders []struct {
			ID        string  `json:"id"`
			ParentID  *string `json:"parent_id"`
			Name      string  `json:"name"`
			IsDefault bool    `json:"is_default"`
		} `json:"folders"`
	}](s, "/api/bootstrap")
	if err != nil {
		return err
	}
	// The folder tree: a top-level folder, and the deepest one with its full path (a Reader API label).
	byID := map[string]int{}
	for i, f := range boot.Folders {
		byID[f.ID] = i
	}
	pathOf := func(i int) (string, int) {
		var names []string
		for j := i; ; {
			names = append([]string{boot.Folders[j].Name}, names...)
			p := boot.Folders[j].ParentID
			if p == nil {
				break
			}
			j = byID[*p]
		}
		return strings.Join(names, "/"), len(names)
	}
	topIdx, deepIdx, deepDepth := -1, 0, 0
	for i, f := range boot.Folders {
		if topIdx < 0 && f.ParentID == nil && !f.IsDefault {
			topIdx = i
		}
		if _, d := pathOf(i); d > deepDepth {
			deepIdx, deepDepth = i, d
		}
	}
	topPath, _ := pathOf(topIdx)
	deepPath, _ := pathOf(deepIdx)
	m.add("library.folders", "count", float64(len(boot.Folders)))
	m.add("library.folder_max_depth", "levels", float64(deepDepth))
	feedID, folderID := boot.Feeds[3].ID, boot.Folders[len(boot.Folders)/2].ID
	topID, deepID := boot.Folders[topIdx].ID, boot.Folders[deepIdx].ID
	label := func(p string) string {
		return greader + "stream/contents/user/-/label/" + url.PathEscape(p) + "?n=50&xt=user/-/state/com.google/read&output=json"
	}

	// Item ids for the detail and Reader contents calls.
	first, err := jsonGet[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		Next *string `json:"next_cursor"`
	}](s, "/api/items?limit=50")
	if err != nil {
		return err
	}
	var ids []string
	form := url.Values{}
	for _, it := range first.Items {
		ids = append(ids, it.ID)
		form.Add("i", it.ID)
	}

	sp := s.sample()
	web := []endpoint{
		{name: "web.bootstrap", method: "GET", path: "/api/bootstrap"},
		{name: "web.status", method: "GET", path: "/api/status"},
		{name: "web.list_unread_page1", method: "GET", path: "/api/items?limit=50"},
		{name: "web.list_all_page1", method: "GET", path: "/api/items?view=all&limit=50"},
		{name: "web.list_oldest_page1", method: "GET", path: "/api/items?order=oldest&limit=50"},
		{name: "web.list_starred", method: "GET", path: "/api/items?view=starred&limit=50"},
		{name: "web.list_feed", method: "GET", path: "/api/items?view=all&feed=" + feedID + "&limit=50"},
		{name: "web.list_folder", method: "GET", path: "/api/items?folder=" + folderID + "&limit=50"},
		{name: "web.list_folder_top_subtree", method: "GET", path: "/api/items?folder=" + topID + "&limit=50"},
		{name: "web.list_folder_deepest", method: "GET", path: "/api/items?folder=" + deepID + "&limit=50"},
		{name: "web.list_folder_top_all", method: "GET", path: "/api/items?view=all&folder=" + topID + "&limit=50"},
		{name: "reader.label_top_n50", method: "GET", reader: true, path: label(topPath)},
		{name: "reader.label_deepest_n50", method: "GET", reader: true, path: label(deepPath)},
		{name: "web.item_detail", method: "GET", path: "/api/items/" + ids[0]},
		{name: "web.stats_summary_year", method: "GET", path: "/api/stats/summary?range=year"},
		{name: "web.stats_summary_all", method: "GET", path: "/api/stats/summary?range=all"},
		{name: "web.health_feeds", method: "GET", path: "/api/health/feeds"},
		{name: "web.opml_export", method: "GET", path: "/api/opml"},
		{name: "reader.subscription_list", method: "GET", reader: true, path: greader + "subscription/list?output=json"},
		{name: "reader.tag_list", method: "GET", reader: true, path: greader + "tag/list?output=json"},
		{name: "reader.unread_count", method: "GET", reader: true, path: greader + "unread-count?output=json"},
		{name: "reader.stream_contents_n50", method: "GET", reader: true,
			path: greader + "stream/contents/user/-/state/com.google/reading-list?n=50&xt=user/-/state/com.google/read&output=json"},
		{name: "reader.stream_contents_n250", method: "GET", reader: true,
			path: greader + "stream/contents/user/-/state/com.google/reading-list?n=250&xt=user/-/state/com.google/read&output=json"},
		{name: "reader.stream_ids_n10000_unread", method: "GET", reader: true,
			path: greader + "stream/items/ids?s=user/-/state/com.google/reading-list&n=10000&xt=user/-/state/com.google/read&output=json"},
		{name: "reader.stream_ids_n10000_all", method: "GET", reader: true,
			path: greader + "stream/items/ids?s=user/-/state/com.google/reading-list&n=10000&output=json"},
		{name: "reader.stream_starred_n50", method: "GET", reader: true,
			path: greader + "stream/contents/user/-/state/com.google/starred?n=50&output=json"},
		{name: "reader.items_contents_50ids", method: "POST", reader: true, path: greader + "stream/items/contents?output=json", body: form.Encode()},
	}
	for _, e := range web {
		if err := s.timeEndpoint(m, e); err != nil {
			return err
		}
	}

	// A deep page: follow the cursor 100 pages (5000 items) into the unread list.
	cur := first.Next
	var deep time.Duration
	for i := 0; i < 100 && cur != nil; i++ {
		code, b, d, err := s.req("GET", "/api/items?limit=50&cursor="+url.QueryEscape(*cur), nil, "", nil)
		if err != nil || code != 200 {
			return fmt.Errorf("deep page %d: %d %v", i, code, err)
		}
		var pg struct {
			Next *string `json:"next_cursor"`
		}
		_ = json.Unmarshal(b, &pg)
		cur, deep = pg.Next, d
	}
	m.add("api.web.list_unread_page100.ms", "ms", ms(deep))
	peak, _ := sp.done()
	m.add("rss.peak_during_api_calls_mb", "MB", mb(peak))

	// Full-text search, each term in the same 6-call loop; the peak resident size is taken across all of them.
	sp = s.sample()
	for _, q := range []struct{ name, q, extra string }{
		{"common_term", "market", ""}, {"common_term_rank", "market", "&order=rank"},
		{"mid_term", "software", ""}, {"rare_term", rareTerm, ""}, {"planted_mid_term", midTerm, ""},
		{"two_terms", "market software", ""}, {"prefix_typing", "marke", "&typing=1"}, {"absent_term", "qqqqzzzz", ""},
	} {
		if err := s.timeEndpoint(m, endpoint{name: "search." + q.name, method: "GET", path: "/api/items?limit=50&q=" + url.QueryEscape(q.q) + q.extra}); err != nil {
			return err
		}
	}
	peak, _ = sp.done()
	m.add("rss.peak_during_search_mb", "MB", mb(peak))
	for _, q := range []struct{ name, q string }{{"common_term", "market"}, {"mid_term", "software"}, {"two_terms", "market software"}} {
		if err := s.searchSpread(m, q.name, q.q); err != nil {
			return err
		}
	}
	m.add("rss.after_queries_mb", "MB", mb(s.rssNow()))
	if err := s.soak(m, 5); err != nil {
		return err
	}

	// Backup through the in-app path, then restore into a fresh data directory.
	if err := benchBackup(o, m, s, run); err != nil {
		return err
	}

	// Refresh: every feed returns 5 new items; feeds at their retention cap are trimmed after the fetch.
	if err := allowPrivateFeeds(dir); err != nil {
		return err
	}
	itemsBefore := countRows(dir, "SELECT count(*) FROM items")
	trimBefore := countRows(dir, "SELECT count(*) FROM trimmed_items")
	sp = s.sample()
	t := time.Now()
	pr := s.probe(ids[0])
	code, b, _, err := s.req("POST", "/api/refresh", nil, "application/json", nil)
	if err != nil || code/100 != 2 {
		return fmt.Errorf("refresh: %d %s %v", code, b, err)
	}
	if err := s.waitIdle(500 * time.Millisecond); err != nil {
		return err
	}
	refresh := time.Since(t)
	pr.report(m, "refresh.concurrent")
	peak, wal := sp.done()
	itemsAfter := countRows(dir, "SELECT count(*) FROM items")
	trimAfter := countRows(dir, "SELECT count(*) FROM trimmed_items")
	okFetches := countRows(dir, "SELECT count(*) FROM fetch_log WHERE trigger = 'manual' AND outcome = 'ok'")
	errFetches := countRows(dir, "SELECT count(*) FROM fetch_log WHERE trigger = 'manual' AND outcome = 'error'")
	m.add("refresh.all_feeds_s", "s", refresh.Seconds())
	m.add("refresh.feeds_ok", "count", float64(okFetches))
	m.add("refresh.feeds_error", "count", float64(errFetches))
	m.add("refresh.new_items", "count", float64(itemsAfter-itemsBefore+(trimAfter-trimBefore)))
	m.add("refresh.trimmed_items", "count", float64(trimAfter-trimBefore))
	m.add("rss.peak_during_refresh_mb", "MB", mb(peak))
	m.add("db.wal.peak_during_refresh_mb", "MB", mb(uint64(wal)))
	time.Sleep(5 * time.Second)
	m.add("rss.after_refresh_mb", "MB", mb(s.rssNow()))
	if err := s.timeEndpoint(m, endpoint{name: "web.list_unread_page1_after_refresh", method: "GET", path: "/api/items?limit=50"}); err != nil {
		return err
	}

	// Bulk trim: lower the default retention to 100 and wait for the trim to finish.
	trimBefore = countRows(dir, "SELECT count(*) FROM trimmed_items")
	itemsBefore = countRows(dir, "SELECT count(*) FROM items")
	sp = s.sample()
	t = time.Now()
	pr = s.probe(ids[0])
	code, b, _, err = s.req("PATCH", "/api/settings", []byte(`{"retention.default":100}`), "application/json", nil)
	if err != nil || code/100 != 2 {
		return fmt.Errorf("retention setting: %d %s %v", code, b, err)
	}
	if err := s.waitIdle(500 * time.Millisecond); err != nil {
		return err
	}
	if code, b, _, err = s.req("POST", "/api/retention/apply", nil, "application/json", nil); err != nil || code/100 != 2 {
		return fmt.Errorf("retention apply: %d %s %v", code, b, err)
	}
	if err := s.waitIdle(500 * time.Millisecond); err != nil {
		return err
	}
	trim := time.Since(t)
	pr.report(m, "trim.concurrent")
	peak, wal = sp.done()
	m.add("trim.bulk_s", "s", trim.Seconds())
	m.add("trim.bulk_items_removed", "count", float64(itemsBefore-countRows(dir, "SELECT count(*) FROM items")))
	m.add("trim.bulk_ledger_added", "count", float64(countRows(dir, "SELECT count(*) FROM trimmed_items")-trimBefore))
	m.add("rss.peak_during_bulk_trim_mb", "MB", mb(peak))
	m.add("db.wal.peak_during_bulk_trim_mb", "MB", mb(uint64(wal)))
	if err := benchImport(m, s); err != nil {
		return err
	}
	m.add("db.file.end_mb", "MB", mb(uint64(fsize(filepath.Join(dir, "kipple.db")))))
	m.add("db.wal.end_mb", "MB", mb(uint64(fsize(filepath.Join(dir, "kipple.db-wal")))))
	_, hwm, _ := rss(s.cmd.Process.Pid)
	m.add("rss.process_peak_mb", "MB", mb(hwm))
	if err := s.stop(); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// benchBackup creates a backup with POST /api/backup, downloads it and restores it with `kipple restore`
// into an empty data directory, then starts a second server on the result and compares the unread count.
func benchBackup(o benchOpts, m *metrics, s *server, run int) error {
	want, err := jsonGet[struct {
		Unread int `json:"unread_total"`
	}](s, "/api/status")
	if err != nil {
		return err
	}
	sp := s.sample()
	t := time.Now()
	code, b, _, err := s.req("POST", "/api/backup", nil, "application/json", nil)
	if err != nil {
		return err
	}
	type ready struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
		URL    string `json:"url"`
	}
	var r ready
	_ = json.Unmarshal(b, &r)
	jobID := r.JobID
	for r.Status != "ready" {
		if code != 200 && code != 202 {
			return fmt.Errorf("backup: HTTP %d %s", code, trunc(b))
		}
		time.Sleep(100 * time.Millisecond)
		code, b, _, err = s.req("GET", "/api/backup/jobs/"+jobID, nil, "", nil)
		if err != nil {
			return err
		}
		r = ready{}
		_ = json.Unmarshal(b, &r)
		if r.Status == "failed" {
			return fmt.Errorf("backup failed: %s", trunc(b))
		}
	}
	build := time.Since(t)
	code, zip, dl, err := s.req("GET", r.URL, nil, "", nil)
	if err != nil || code != 200 {
		return fmt.Errorf("backup download: %d %v", code, err)
	}
	peak, _ := sp.done()
	m.add("backup.create_s", "s", build.Seconds())
	m.add("backup.download_s", "s", dl.Seconds())
	m.add("backup.zip_mb", "MB", float64(len(zip))/(1<<20))
	m.add("rss.peak_during_backup_mb", "MB", mb(peak))
	zipPath := filepath.Join(o.work, fmt.Sprintf("backup-%d.zip", run))
	if err := os.WriteFile(zipPath, zip, 0o600); err != nil {
		return err
	}

	rdir := filepath.Join(o.work, fmt.Sprintf("restored-%d", run))
	if err := os.MkdirAll(rdir, 0o750); err != nil {
		return err
	}
	cmd := exec.Command(o.bin, "restore", zipPath, "--yes") // #nosec G204 -- the binary under test
	cmd.Env = append(os.Environ(), "KIPPLE_DATA="+rdir)
	t = time.Now()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restore: %v: %s", err, out)
	}
	m.add("restore.cli_s", "s", time.Since(t).Seconds())
	rs, startup, err := startServer(o, rdir, o.port+2)
	if err != nil {
		return err
	}
	m.add("restore.first_start_ms", "ms", ms(startup))
	if err := rs.login(); err != nil {
		_ = rs.stop()
		return err
	}
	got, err := jsonGet[struct {
		Unread int `json:"unread_total"`
	}](rs, "/api/status")
	_ = rs.stop()
	if err != nil {
		return err
	}
	ok := 0.0
	if got.Unread == want.Unread {
		ok = 1
	}
	m.add("restore.unread_matches", "bool", ok)
	_ = os.Remove(zipPath)
	return os.RemoveAll(rdir)
}

// probe measures what a reader and a writer see while a background job runs: every 250 ms it
// fetches the first page of the unread list (a read) and toggles a star (a write).
type probe struct {
	stop          chan struct{}
	wg            sync.WaitGroup
	reads, writes []float64
	failed        int
	starred       bool
	itemID        string
}

func (s *server) probe(itemID string) *probe {
	p := &probe{stop: make(chan struct{}), itemID: itemID}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			if code, _, d, err := s.req("GET", "/api/items?limit=50", nil, "", nil); err == nil && code == 200 {
				p.reads = append(p.reads, ms(d))
			} else {
				p.failed++
			}
			p.starred = !p.starred
			body := `{"starred":` + strconv.FormatBool(p.starred) + `}`
			if code, _, d, err := s.req("PUT", "/api/items/"+p.itemID+"/star", []byte(body), "application/json", nil); err == nil && code/100 == 2 {
				p.writes = append(p.writes, ms(d))
			} else {
				p.failed++
			}
			select {
			case <-p.stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	return p
}

func maxOf(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		m = max(m, x)
	}
	return m
}

// report records the median and the worst latency of each kind of call and the number that failed.
func (p *probe) report(m *metrics, prefix string) {
	close(p.stop)
	p.wg.Wait()
	m.add(prefix+".read_median_ms", "ms", median(p.reads))
	m.add(prefix+".read_max_ms", "ms", maxOf(p.reads))
	m.add(prefix+".write_median_ms", "ms", median(p.writes))
	m.add(prefix+".write_max_ms", "ms", maxOf(p.writes))
	m.add(prefix+".failed_calls", "count", float64(p.failed))
}

// soak sends rounds of mixed list and search calls and records the resident size after each
// round: a library that leaks would climb round after round. A search that answers 422
// (search_too_broad: it outran its 500 ms budget) is counted, not an error.
func (s *server) soak(m *metrics, rounds int) error {
	paths := []string{"/api/items?limit=50", "/api/items?view=all&limit=50", "/api/items?limit=50&q=market", "/api/items?limit=50&q=software",
		"/api/bootstrap", "/api/items?view=starred&limit=50"}
	searches, tooBroad := 0, 0
	for r := 1; r <= rounds; r++ {
		for i := 0; i < 60; i++ {
			p := paths[i%len(paths)]
			code, b, _, err := s.req("GET", p, nil, "", nil)
			if strings.Contains(p, "q=") {
				searches++
				if code == 422 {
					tooBroad++
					continue
				}
			}
			if err != nil || code != 200 {
				return fmt.Errorf("soak: %d %v %s", code, err, trunc(b))
			}
		}
		time.Sleep(2 * time.Second)
		m.add(fmt.Sprintf("rss.soak_round_%d_mb", r), "MB", mb(s.rssNow()))
	}
	m.add("soak.common_searches", "count", float64(searches))
	m.add("soak.common_searches_too_broad", "count", float64(tooBroad))
	return nil
}

// searchSpread sends 40 searches for one common term, one after another, and records the median, the
// slowest and the number the server refused as too broad.
func (s *server) searchSpread(m *metrics, name, q string) error {
	var times []float64
	tooBroad := 0
	for i := 0; i < 40; i++ {
		code, b, d, err := s.req("GET", "/api/items?limit=50&q="+url.QueryEscape(q), nil, "", nil)
		switch {
		case err != nil:
			return err
		case code == 422:
			tooBroad++
		case code != 200:
			return fmt.Errorf("search %s: %d %s", q, code, trunc(b))
		}
		times = append(times, ms(d))
	}
	m.add("search.spread."+name+".median_ms", "ms", median(times))
	m.add("search.spread."+name+".max_ms", "ms", maxOf(times))
	m.add("search.spread."+name+".too_broad_of_40", "count", float64(tooBroad))
	return nil
}

// allowPrivateFeeds lets every feed be fetched from the loopback address the synthetic feed server uses.
// The generated library leaves the flag off, as most real feeds are public, so the list and search
// numbers do not carry the extra work a LAN feed's images cost; the refresh needs it on.
func allowPrivateFeeds(dir string) error {
	h, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "kipple.db"))+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer h.Close()
	_, err = h.Exec("UPDATE feeds SET allow_private_net = 1")
	return err
}

// opmlWithFolders builds an OPML document of n folders nested up to 5 levels (30 at the top, 1 to 5
// subfolders each, breadth first), each holding one feed that the synthetic feed server does not know.
func opmlWithFolders(n int, prefix string) []byte {
	r := rand.New(rand.NewSource(99)) // #nosec G404 -- fixture data
	children := make([][]int, n)
	depth := make([]int, n)
	next := 0
	for ; next < 30 && next < n; next++ {
		depth[next] = 1
	}
	for q := 0; q < n && next < n; q++ {
		if depth[q] >= 5 || depth[q] == 0 {
			continue
		}
		for k := 1 + r.Intn(5); k > 0 && next < n; k-- {
			depth[next] = depth[q] + 1
			children[q] = append(children[q], next)
			next++
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><opml version="2.0"><head><title>folders</title></head><body>`)
	var emit func(i int)
	emit = func(i int) {
		fmt.Fprintf(&b, `<outline text="%s %d">`, prefix, i)
		fmt.Fprintf(&b, `<outline type="rss" text="%s feed %d" xmlUrl="http://127.0.0.1:1/%s/%d.xml"/>`, prefix, i, strings.ReplaceAll(prefix, " ", "-"), i)
		for _, c := range children[i] {
			emit(c)
		}
		b.WriteString(`</outline>`)
	}
	for i := 0; i < 30 && i < n; i++ {
		emit(i)
	}
	b.WriteString(`</body></opml>`)
	return []byte(b.String())
}

// benchImport imports OPML files that create 250 to 3000 nested folders, each size from a new prefix, and
// records the request time and status of each. A failed import is a result, not an error: folder import
// work grows faster than linearly with the number of folders and ends at the writer's 10 s deadline.
func benchImport(m *metrics, s *server) error {
	for _, n := range []int{250, 500, 1000, 2000, 3000} {
		body := opmlWithFolders(n, fmt.Sprintf("Import %d folder", n))
		t := time.Now()
		code, _, _, err := s.req("POST", "/api/opml", body, "text/x-opml", nil)
		if err != nil {
			return err
		}
		took := time.Since(t).Seconds()
		m.add(fmt.Sprintf("import.opml_%d_folders.http_status", n), "code", float64(code))
		m.add(fmt.Sprintf("import.opml_%d_folders.request_s", n), "s", took)
		if err := s.waitIdle(500 * time.Millisecond); err != nil {
			return err
		}
	}
	return s.timeEndpoint(m, endpoint{name: "web.bootstrap_after_import", method: "GET", path: "/api/bootstrap"})
}
