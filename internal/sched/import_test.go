package sched

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/opml"
	"github.com/WPTK/kipple/internal/store"
)

// An import run loads its feeds in one query: every enabled new feed is in the
// run, a disabled or unknown one is not.
func TestImportRunLoadsFeedsTogether(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{})
	srv := newSrv(t, serveOK)
	later := func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() }
	ids := make([]int64, 0, 30)
	for i := 0; i < 30; i++ {
		ids = append(ids, r.add(srv.URL+"/f"+string(rune('a'+i%26))+string(rune('a'+i/26)), later))
	}
	r.sql("UPDATE feeds SET enabled = 0 WHERE id = ?", ids[3])
	info, err := r.s.StartImport(append(ids, 987654321))
	require.NoError(t, err)
	require.Equal(t, 29, info.Total)
	r.waitEvents("run.done", 1)
	require.EqualValues(t, 29, r.num("SELECT count(*) FROM fetch_log WHERE trigger = 'import'"))
}

// An import run's events say it is one (issue #93): each fetch.done reports the import trigger and run.done its kind,
// so a client that missed run.start still does not take the run's new items for a refresh the person asked for.
func TestImportRunEventsSayImport(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{})
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/imported", func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() })
	_, err := r.s.StartImport([]int64{id})
	require.NoError(t, err)
	r.waitEvents("run.done", 1)
	require.Equal(t, "import", r.events("run.done")[0]["kind"])
	r.waitEvents("fetch.done", 1)
	ev := r.events("fetch.done")[0]
	require.Equal(t, "import", ev["trigger"])
	require.NotZero(t, ev["new_items"])
}

// A big import does not start every fetch at once (issue #464): the run queues its feeds and starts one only when a
// worker is free, so no more than Workers fetches are ever in flight.
func TestImportRunIsHeldToTheWorkerCap(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{Workers: 3, PerHost: 100})
	var cur, peak atomic.Int32
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		n := cur.Add(1)
		for {
			m := peak.Load()
			if n <= m || peak.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		serveOK(p, w, req)
	})
	later := func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() }
	ids := make([]int64, 0, 40)
	for i := 0; i < 40; i++ {
		ids = append(ids, r.add(fmt.Sprintf("%s/f%d", srv.URL, i), later))
	}
	_, err := r.s.StartImport(ids)
	require.NoError(t, err)
	r.waitEvents("run.done", 1)
	require.Equal(t, 40, srv.total(), "every new feed was fetched")
	require.LessOrEqual(t, int(peak.Load()), 3, "never more fetches at once than workers")
	require.Equal(t, 3, int(peak.Load()), "and the workers are used")
}

// What a person importing 500 feeds gets (issue #464), from the file to the last fetch: the run counts all 500 from its
// start, so a client can show "0 of 500" at once; while it goes, no more than Workers fetches are in flight and the
// status answer (what a reloaded page reads) reports the run and its progress; at the end every feed was fetched once.
func TestImportOf500FeedsIsQueuedAndReported(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{Workers: 8, PerHost: 100})
	// The handler holds every fetch until release closes; closing it at cleanup too means a failed require below
	// ends the test instead of leaving the server and the scheduler blocked until the package times out.
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)
	var cur, peak atomic.Int32
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		n := cur.Add(1)
		for {
			m := peak.Load()
			if n <= m || peak.CompareAndSwap(m, n) {
				break
			}
		}
		<-release
		cur.Add(-1)
		serveOK(p, w, req)
	})
	file, err := os.ReadFile("testdata/import-500.opml")
	require.NoError(t, err)
	doc, err := opml.Parse(bytes.NewReader(bytes.ReplaceAll(file, []byte("http://feeds.test"), []byte(srv.URL))))
	require.NoError(t, err)
	res, err := opml.Import(context.Background(), r.db, doc, opml.ImportOptions{})
	require.NoError(t, err)
	require.Len(t, res.NewFeedIDs, 500)
	r.sql("UPDATE feeds SET allow_private_net = 1") // the test server is on loopback

	info, err := r.s.StartImport(res.NewFeedIDs)
	require.NoError(t, err)
	require.Equal(t, 500, info.Total, "the answer counts every new feed")
	r.waitEvents("run.start", 1)
	require.EqualValues(t, 500, r.events("run.start")[0]["total"])

	waitFor(t, "the workers busy", func() bool { return cur.Load() == 8 })
	runs, inflight, ok := r.s.Status()
	require.True(t, ok)
	require.Len(t, runs, 1)
	require.Equal(t, RunImport, runs[0].Kind)
	require.Equal(t, 500, runs[0].Total)
	require.Equal(t, 0, runs[0].Done)
	require.Equal(t, 8, inflight, "the other 492 wait in the queue")

	releaseAll()
	r.waitEvents("run.done", 1)
	require.LessOrEqual(t, int(peak.Load()), 8, "never more fetches at once than workers")
	require.Equal(t, 500, srv.total(), "every new feed was fetched once")
	require.EqualValues(t, 500, r.num("SELECT count(*) FROM fetch_log WHERE trigger = 'import'"))
}
