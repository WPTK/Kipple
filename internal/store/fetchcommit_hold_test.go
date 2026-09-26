package store

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// uidsByGUID maps each parsed item's guid to its uid (the key HoldUIDs uses).
func uidsByGUID(res *fetch.Result) map[string]string {
	m := map[string]string{}
	for _, it := range res.Feed.Items {
		m[it.GUID] = it.UID
	}
	return m
}

func pendingIDs(t *testing.T, d *DB) []int64 {
	t.Helper()
	var ids []int64
	require.NoError(t, json.Unmarshal([]byte(d.HoldPending()), &ids))
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// The commit marks the items the scheduler will extract pending as it inserts
// them, before the transaction commits: a Reader client can never list one of
// them before its hold starts. Muted items are never held.
func TestCommitMarksHeldItemsPendingInTransaction(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("https://a.example/feed")
	_, err := e.db.CreateFilter(e.ctx, Filter{Enabled: true, Scope: "global", Kind: "text",
		Terms: []string{"title g2"}, Fields: []string{"title"}, WholeWord: true, FoldDiacritics: true, Action: "mute"})
	require.NoError(t, err)

	res := e.okResult(e.snap(id), rss(numbered(4)...))
	uid := uidsByGUID(res)
	res.HoldUIDs = map[string]bool{uid["g1"]: true, uid["g2"]: true, uid["g3"]: true}
	info := e.commit(res)
	require.Len(t, info.NewIDs, 4)
	require.Len(t, info.MutedIDs, 1)
	require.Len(t, info.Held, 2, "g2 is muted: never held")
	require.NotContains(t, info.Held, uid["g0"], "not asked for")
	var want []int64
	for _, g := range []string{"g1", "g3"} {
		iid := info.Held[uid[g]]
		require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE id = ? AND uid = ?", iid, uid[g]))
		want = append(want, iid)
	}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	require.Equal(t, want, pendingIDs(t, e.db), "marked by the commit itself")
}

// A chunk that rolls back takes its marks with it: its items never became visible.
func TestCommitRollbackClearsHeldMarks(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("https://a.example/feed")
	e.exec(`CREATE TRIGGER fail_log BEFORE INSERT ON fetch_log BEGIN SELECT RAISE(ABORT, 'injected'); END`)

	res := e.okResult(e.snap(id), rss(numbered(3)...))
	res.HoldUIDs = map[string]bool{}
	for _, u := range uidsByGUID(res) {
		res.HoldUIDs[u] = true
	}
	info, err := e.db.CommitFetch(e.ctx, res)
	require.Error(t, err)
	require.Empty(t, info.Held)
	require.Equal(t, 0, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Equal(t, "[]", e.db.HoldPending())
}
