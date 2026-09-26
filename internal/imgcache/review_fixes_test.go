package imgcache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// syncBuf is a log sink safe for the background goroutines.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// failDeletes makes every DELETE on entries fail, as a full disk or an I/O
// error would (SQLITE_FULL): selects keep working.
func failDeletes(t *testing.T, c *Cache) {
	t.Helper()
	_, err := c.wr.Exec(`CREATE TRIGGER fail_delete BEFORE DELETE ON entries BEGIN SELECT RAISE(ABORT, 'database or disk is full'); END`)
	require.NoError(t, err)
}

// returnsWithin runs f and fails the test if it does not return in d (the
// old eviction loops spun forever here, holding c.mu).
func returnsWithin(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("did not return: the loop spins on a failing index")
	}
}

func TestEvictionStopsOnAFailingIndexWithoutSpinning(t *testing.T) {
	logs := &syncBuf{}
	c, clk := newCache(t, func(o *Options) {
		o.MaxBytes = 1 << 20
		o.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	})
	for i := 0; i < 3; i++ {
		put(t, c, "e"+string(rune('a'+i)), 300<<10)
	}
	failDeletes(t, c)

	// Over the cap: Sweep's eviction fails at once instead of re-selecting the same keys forever.
	c.maxBytes.Store(500 << 10)
	var err error
	returnsWithin(t, 5*time.Second, func() { _, err = c.Sweep(context.Background(), false) })
	require.ErrorContains(t, err, "full")
	require.True(t, c.mu.TryLock(), "the lock is not held after the failure")
	c.mu.Unlock()

	// A commit that pushes the cache over the cap returns too, and warns once, not per commit.
	returnsWithin(t, 5*time.Second, func() { put(t, c, "ez", 100<<10) })
	returnsWithin(t, 5*time.Second, func() { put(t, c, "ey", 100<<10) })
	require.Equal(t, 1, strings.Count(logs.String(), "imgcache: eviction"), logs.String())
	clk.Advance(2 * time.Minute)
	returnsWithin(t, 5*time.Second, func() { put(t, c, "ex", 100<<10) })
	require.Equal(t, 2, strings.Count(logs.String(), "imgcache: eviction"), "warned again after the back-off")

	// Idle expiry stops the same way.
	c.maxBytes.Store(64 << 20)
	clk.Advance(61 * 24 * time.Hour)
	returnsWithin(t, 5*time.Second, func() { _, err = c.Sweep(context.Background(), false) })
	require.Error(t, err)
	require.True(t, c.mu.TryLock())
	c.mu.Unlock()

	// Once the index works again, eviction catches up and the counters are right.
	_, err = c.wr.Exec("DROP TRIGGER fail_delete")
	require.NoError(t, err)
	c.maxBytes.Store(500 << 10)
	_, err = c.Sweep(context.Background(), false)
	require.NoError(t, err)
	require.LessOrEqual(t, c.Stats().UsedBytes, int64(500<<10))
	var sum int64
	require.NoError(t, c.wr.QueryRow("SELECT COALESCE(SUM(size),0) FROM entries WHERE status='ok'").Scan(&sum))
	require.Equal(t, sum, c.Stats().UsedBytes)
}

func TestEvictionHonorsCancellation(t *testing.T) {
	c, _ := newCache(t)
	put(t, c, "a", 1000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.mu.Lock()
	err := c.evictLocked(ctx, 0)
	c.mu.Unlock()
	require.ErrorIs(t, err, context.Canceled)
}

func TestDeferRevalidationBacksOffAndResets(t *testing.T) {
	c, clk := newCache(t)
	key := put(t, c, "r", 100)
	clk.Advance(8 * 24 * time.Hour)
	for i, want := range []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute} {
		require.NoError(t, c.DeferRevalidation(key))
		e, ok := lookupOK(t, c, key)
		require.True(t, ok && e.OK, "the entry and its file stay")
		require.Equal(t, want, e.FreshUntil.Sub(clk.Now()), "failure %d", i+1)
		require.Equal(t, i+1, e.NegCount)
		f, err := c.OpenFile(key)
		require.NoError(t, err)
		_ = f.Close()
	}
	require.Zero(t, c.Stats().Failures)
	require.NoError(t, c.Revalidated(key, 0, "", ""))
	e, _ := lookupOK(t, c, key)
	require.Zero(t, e.NegCount, "a 304 clears the back-off")
	require.Error(t, c.DeferRevalidation(KeyOrig(0, "http://img.example/none")), "no such entry")
}

func TestInProgressMarker(t *testing.T) {
	c, clk := newCache(t)
	url := "http://img.example/t.jpg"
	key := KeyThumb(0, url)
	require.NoError(t, c.PutInProgress(key, url, 0, VariantThumb))
	e, ok := lookupOK(t, c, key)
	require.True(t, ok)
	require.False(t, e.OK)
	require.Equal(t, InProgress, e.NegReason)
	require.Equal(t, 10*time.Minute, e.FreshUntil.Sub(clk.Now()))
	require.Zero(t, c.Stats().Failures, "a marker is not a failure")

	// The marker expires; the sweep keeps it so the next marker (another crash) doubles.
	clk.Advance(11 * time.Minute)
	_, ok = lookupOK(t, c, key)
	require.False(t, ok, "expired: the work may be tried again")
	_, err := c.Sweep(context.Background(), false)
	require.NoError(t, err)
	require.NoError(t, c.PutInProgress(key, url, 0, VariantThumb))
	e, _ = lookupOK(t, c, key)
	require.Equal(t, 20*time.Minute, e.FreshUntil.Sub(clk.Now()))

	// The work's own transient failure replaces the marker at the same step, not the next.
	require.NoError(t, c.PutNegVariant(key, url, 0, VariantThumb, NegTransient, 0, "could not store"))
	e, _ = lookupOK(t, c, key)
	require.Equal(t, 20*time.Minute, e.FreshUntil.Sub(clk.Now()))
	require.EqualValues(t, 1, c.Stats().Failures)

	// A success replaces it and the counters follow.
	require.NoError(t, c.PutInProgress(key, url, 0, VariantThumb))
	w, err := c.Begin(key, url, 0, 10)
	require.NoError(t, err)
	_, _ = w.Write([]byte("0123456789"))
	require.NoError(t, w.Commit(Meta{ContentType: "image/jpeg", Variant: VariantThumb}))
	st := c.Stats()
	require.EqualValues(t, 0, st.NegEntries)
	require.EqualValues(t, 1, st.Files)

	// Expired markers go two days after their expiry.
	other := KeyThumb(0, url+"?2")
	require.NoError(t, c.PutInProgress(other, url+"?2", 0, VariantThumb))
	clk.Advance(24 * time.Hour)
	_, err = c.Sweep(context.Background(), false)
	require.NoError(t, err)
	var n int
	require.NoError(t, c.rd.QueryRow("SELECT count(*) FROM entries WHERE key = ?", other).Scan(&n))
	require.Equal(t, 1, n, "kept while a repeat could still double")
	clk.Advance(49 * time.Hour)
	_, err = c.Sweep(context.Background(), false)
	require.NoError(t, err)
	require.NoError(t, c.rd.QueryRow("SELECT count(*) FROM entries WHERE key = ?", other).Scan(&n))
	require.Zero(t, n)
}

func TestOpenReaderOutlivesCommitAndAbort(t *testing.T) {
	c, _ := newCache(t)
	data := bytes.Repeat([]byte("kipple"), 10000)
	for _, commit := range []bool{true, false} {
		url := "http://img.example/reader-" + map[bool]string{true: "c", false: "a"}[commit]
		key := KeyOrig(0, url)
		w, err := c.Begin(key, url, 0, int64(len(data)))
		require.NoError(t, err)
		_, err = w.Write(data[:len(data)/2])
		require.NoError(t, err)
		rd, err := w.OpenReader()
		require.NoError(t, err)
		head := make([]byte, 100)
		_, err = rd.ReadAt(head, 0)
		require.NoError(t, err, "bytes already written are readable")
		_, err = w.Write(data[len(data)/2:])
		require.NoError(t, err)
		if commit {
			require.NoError(t, w.Commit(Meta{ContentType: "image/png"}), "the rename works with a reader open (Windows too)")
		} else {
			w.Abort()
		}
		got, err := io.ReadAll(io.NewSectionReader(rd, 0, int64(len(data))))
		require.NoError(t, err)
		require.Equal(t, data, got, "the whole body is still readable after commit=%v", commit)
		require.NoError(t, rd.Close())
		ents, _ := os.ReadDir(filepath.Join(c.Dir(), "tmp"))
		require.Empty(t, ents)
		_, err = w.OpenReader()
		require.Error(t, err, "no reader after the end")
	}
	f, err := c.OpenFile(KeyOrig(0, "http://img.example/reader-c"))
	require.NoError(t, err)
	_ = f.Close()
}

func TestDiskBytesIsTheCounterPlusTheIndex(t *testing.T) {
	c, _ := newCache(t)
	put(t, c, "d1", 1000)
	put(t, c, "d2", 2000)
	var idx int64
	for _, s := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(filepath.Join(c.Dir(), "index.db"+s)); err == nil {
			idx += fi.Size()
		}
	}
	require.Positive(t, idx)
	require.Equal(t, int64(3000)+idx, c.DiskBytes())
	// Not a directory walk: a stray file does not count.
	require.NoError(t, os.WriteFile(filepath.Join(c.Dir(), "stray"), make([]byte, 5000), 0o600))
	require.Equal(t, int64(3000)+idx, c.DiskBytes())
}

// Past the cap on failure rows, in-progress markers are never pushed out by
// ordinary failures (they are the crash-loop protection), expired failures go
// first, then the least recently replayed; markers have their own, smaller cap.
func TestNegPruneKeepsMarkersAndRecentlyHitFailures(t *testing.T) {
	c, clk := newCache(t)
	c.maxNeg, c.maxMarkers = 10, 4
	u := func(s string, i int) string { return fmt.Sprintf("http://img.example/%s%d", s, i) }
	fail := func(i int) {
		t.Helper()
		require.NoError(t, c.PutNeg(KeyOrig(0, u("p", i)), u("p", i), 0, NegPermanent, 404, "gone"))
		clk.Advance(time.Second)
	}
	has := func(key string) bool {
		var n int
		require.NoError(t, c.rd.QueryRow("SELECT count(*) FROM entries WHERE key = ?", key).Scan(&n))
		return n == 1
	}
	marker := func(i int) string { return KeyThumb(0, u("m", i)) }
	for i := 0; i < 4; i++ {
		require.NoError(t, c.PutInProgress(marker(i), u("m", i), 0, VariantThumb))
		clk.Advance(time.Second)
	}
	for i := 0; i < 9; i++ {
		fail(i)
	}
	// A transient failure, newer than all of them, that has expired by the time the cap is hit.
	x := KeyOrig(0, u("x", 0))
	require.NoError(t, c.PutNeg(x, u("x", 0), 0, NegTransient, 503, "busy"))
	clk.Advance(11 * time.Minute)
	for i := 0; i < 3; i++ { // replayed: the most recently used failures
		e, ok := lookupOK(t, c, KeyOrig(0, u("p", i)))
		require.True(t, ok && !e.OK)
	}
	clk.Advance(time.Second)
	for i := 9; i < 12; i++ { // two prunes, of two rows each
		fail(i)
	}
	for i := 0; i < 4; i++ {
		require.True(t, has(marker(i)), "marker %d survives the failures", i)
	}
	require.False(t, has(x), "the expired failure went first")
	for i := 0; i < 3; i++ {
		require.True(t, has(KeyOrig(0, u("p", i))), "recently replayed failure %d is kept", i)
	}
	require.False(t, has(KeyOrig(0, u("p", 3))), "the least recently used went")
	var failures int64
	require.NoError(t, c.rd.QueryRow("SELECT count(*) FROM entries WHERE status = 'neg' AND neg_reason <> ?", InProgress).Scan(&failures))
	require.LessOrEqual(t, failures, int64(10))
	require.Equal(t, failures+4, c.Stats().NegEntries, "the counter follows")

	// Markers have their own cap: the oldest go.
	for i := 4; i < 9; i++ {
		require.NoError(t, c.PutInProgress(marker(i), u("m", i), 0, VariantThumb))
		clk.Advance(time.Second)
	}
	fail(99) // over the total: a prune
	require.False(t, has(marker(0)))
	require.True(t, has(marker(8)))
	var markers int64
	require.NoError(t, c.rd.QueryRow("SELECT count(*) FROM entries WHERE neg_reason = ?", InProgress).Scan(&markers))
	require.LessOrEqual(t, markers, int64(4))
}

// Each count has its own cap: markers past 5,000 are pruned although failures are few, and
// failures past 20,000 are pruned although markers are few (the total alone stayed under the sum).
func TestNegPruneChecksEachCapOnItsOwn(t *testing.T) {
	count := func(c *Cache, marker bool) int64 {
		var n int64
		q := "SELECT count(*) FROM entries WHERE status = 'neg' AND neg_reason <> ?"
		if marker {
			q = "SELECT count(*) FROM entries WHERE status = 'neg' AND neg_reason = ?"
		}
		require.NoError(t, c.rd.QueryRow(q, InProgress).Scan(&n))
		return n
	}
	u := func(s string, i int) string { return fmt.Sprintf("http://img.example/%s%d", s, i) }

	t.Run("markers over their cap, few failures", func(t *testing.T) {
		c, clk := newCache(t)
		c.maxNeg, c.maxMarkers = 10, 4
		for i := 0; i < 2; i++ {
			require.NoError(t, c.PutNeg(KeyOrig(0, u("p", i)), u("p", i), 0, NegPermanent, 404, "gone"))
		}
		for i := 0; i < 8; i++ { // 10 rows in all: under the 14 of the two caps together
			require.NoError(t, c.PutInProgress(KeyThumb(0, u("m", i)), u("m", i), 0, VariantThumb))
			clk.Advance(time.Second)
		}
		require.LessOrEqual(t, count(c, true), c.maxMarkers, "markers are held to their own cap")
		require.EqualValues(t, 2, count(c, false), "the failures were not touched")
	})
	t.Run("failures over their cap, few markers", func(t *testing.T) {
		c, clk := newCache(t)
		c.maxNeg, c.maxMarkers = 10, 4
		require.NoError(t, c.PutInProgress(KeyThumb(0, u("m", 0)), u("m", 0), 0, VariantThumb))
		for i := 0; i < 12; i++ { // 13 rows in all: still under 14
			require.NoError(t, c.PutNeg(KeyOrig(0, u("p", i)), u("p", i), 0, NegPermanent, 404, "gone"))
			clk.Advance(time.Second)
		}
		require.LessOrEqual(t, count(c, false), c.maxNeg, "failures are held to their own cap")
		require.EqualValues(t, 1, count(c, true), "the marker was not touched")
	})
	t.Run("the marker count follows replacement and deletion", func(t *testing.T) {
		c, _ := newCache(t)
		k := KeyThumb(0, u("m", 0))
		require.NoError(t, c.PutInProgress(k, u("m", 0), 0, VariantThumb))
		require.EqualValues(t, 1, c.markN.Load())
		require.NoError(t, c.PutNeg(k, u("m", 0), 0, NegPermanent, 415, "not an image")) // the outcome replaces the marker
		require.Zero(t, c.markN.Load())
		require.NoError(t, c.PutInProgress(k, u("m", 0), 0, VariantThumb))
		c.Delete(k)
		require.Zero(t, c.markN.Load())
		require.Zero(t, c.negN.Load())
	})
}
