package sched

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// An import run loads its feeds in one query: every enabled new feed is in the
// run, a disabled or unknown one is not.
func TestImportRunLoadsFeedsTogether(t *testing.T) {
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
