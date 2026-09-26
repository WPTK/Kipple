package imgcache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTinyFilesChargeWholeBlocksAndURL: the cap counts what a file really
// takes, at least one 4 KiB block plus its URL (the index row), so a cache of
// tiny files cannot grow to many times its cap on disk. The recount at open
// agrees with the running counter.
func TestTinyFilesChargeWholeBlocksAndURL(t *testing.T) {
	per := int64(blockSize + len("http://img.example/t0"))
	c, clk := newCache(t, func(o *Options) { o.ByteAccounting = false; o.MaxBytes = 3 * per })
	put(t, c, "t0", 100)
	st := c.Stats()
	require.Equal(t, int64(100), st.UsedBytes, "the file bytes")
	require.Equal(t, per, st.ChargedBytes, "one block plus the URL")
	require.GreaterOrEqual(t, c.DiskBytes(), per, "the footprint counts the charge")

	c = reopen(t, c, clk)
	require.Equal(t, per, c.Stats().ChargedBytes, "the recount charges the same")

	for i := 1; i < 4; i++ {
		clk.Advance(time.Minute)
		put(t, c, fmt.Sprint("t", i), 100)
	}
	st = c.Stats()
	require.LessOrEqual(t, st.ChargedBytes, 3*per, "four tiny files are over a three-block cap")
	require.EqualValues(t, 2, st.Files, "evicted to 90% of the cap")
	require.EqualValues(t, 200, st.UsedBytes)

	// A file over one block is charged its whole blocks.
	clk.Advance(time.Minute)
	key := put(t, c, "big", blockSize+1)
	e, ok := lookupOK(t, c, key)
	require.True(t, ok)
	require.Equal(t, int64(2*blockSize+len(e.URL)), c.cost(e.Size, len(e.URL)))
}

// TestDownloadsInProgressCountAgainstTheCap: bytes written to tmp/ and not
// yet committed count in the load, make an eviction leave room for them, and
// are given back at Commit or Abort.
func TestDownloadsInProgressCountAgainstTheCap(t *testing.T) {
	c, clk := newCache(t, func(o *Options) { o.MaxBytes = 20000 })
	for i := 0; i < 5; i++ {
		put(t, c, fmt.Sprint("f", i), 3000)
		clk.Advance(time.Minute)
	}
	url := "http://img.example/inflight"
	w, err := c.Begin(KeyOrig(0, url), url, 0, -1)
	require.NoError(t, err)
	_, err = w.Write(make([]byte, 10000))
	require.NoError(t, err)
	st := c.Stats()
	require.EqualValues(t, 15000, st.UsedBytes, "the files alone")
	require.EqualValues(t, 25000, st.ChargedBytes, "plus the download in progress")

	_, err = c.Sweep(context.Background(), false)
	require.NoError(t, err)
	st = c.Stats()
	// Room is made for the download, but the files are never evicted below half the target
	// (18000/2), however large the downloads in flight (TestDownloadsInProgressCannotWipeTheCache).
	require.LessOrEqual(t, st.UsedBytes, int64(18000/2), "room is made for the download")
	require.GreaterOrEqual(t, st.UsedBytes, int64(18000/2-3000), "down to half the target, not further")
	require.LessOrEqual(t, st.ChargedBytes, int64(18000/2+10000))

	w.Abort()
	require.Equal(t, c.Stats().UsedBytes, c.Stats().ChargedBytes, "given back at Abort")

	w, err = c.Begin(KeyOrig(0, url), url, 0, -1)
	require.NoError(t, err)
	_, err = w.Write(make([]byte, 500))
	require.NoError(t, err)
	require.NoError(t, w.Commit(Meta{ContentType: "image/png"}))
	require.Equal(t, c.Stats().UsedBytes, c.Stats().ChargedBytes, "moved to the entry at Commit")
}

// TestHostHintsAreCapped: the hosts table is bounded; past the cap the least
// recently updated hints go, and the one just written always stays.
func TestHostHintsAreCapped(t *testing.T) {
	c, clk := newCache(t)
	c.maxHosts = 3
	for i := 0; i < 5; i++ {
		require.NoError(t, c.SetHostHint(fmt.Sprintf("h%d.example", i), HostHint{UA: "browser", Referer: "self"}))
		clk.Advance(time.Minute)
	}
	var n int
	require.NoError(t, c.rd.QueryRow("SELECT count(*) FROM hosts").Scan(&n))
	require.Equal(t, 3, n)
	for i, want := range []bool{false, false, true, true, true} {
		_, ok := c.HostHint(fmt.Sprintf("h%d.example", i))
		require.Equal(t, want, ok, "h%d", i)
	}
	// Same second as the newest: the new one still stays.
	require.NoError(t, c.SetHostHint("new.example", HostHint{UA: "browser", Referer: "none"}))
	_, ok := c.HostHint("new.example")
	require.True(t, ok)
}
