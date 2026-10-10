package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// The filter rules run before a chunk's write transaction (preEvaluate); the
// transaction only applies their results. These tests pin that the verdicts are
// exactly the in-transaction ones, that a rule change in between falls back to
// evaluating inside the transaction, and that the transaction itself does no
// matching when nothing changed.

// countTxMatches counts the rule evaluations that run inside a write transaction.
func (e *env) countTxMatches() *int {
	n := new(int)
	e.db.testTxMatch = func() { *n++ }
	e.t.Cleanup(func() { e.db.testTxMatch = nil })
	return n
}

type ingestOutcome struct {
	Items []string // uid:read:read_at set:starred:starred_at set:muted_by:muted_was_read
	Hits  []string // id:hits:last_hit_at set
	Infos []string // per fetch: new, muted, marked_read, starred, muted ids
	Notes []string
}

func (e *env) ingestOutcome(infos []CommitInfo) ingestOutcome {
	e.t.Helper()
	var o ingestOutcome
	o.Items = column(e.t.(*testing.T), e, `SELECT uid || ':' || read || ':' || (read_at IS NOT NULL) || ':' || starred || ':' ||
		(starred_at IS NOT NULL) || ':' || COALESCE(muted_by, '-') || ':' || COALESCE(muted_was_read, '-') FROM items ORDER BY uid`)
	o.Hits = column(e.t.(*testing.T), e, `SELECT id || ':' || hits || ':' || (last_hit_at IS NOT NULL) FROM filters ORDER BY id`)
	o.Notes = column(e.t.(*testing.T), e, `SELECT COALESCE(note, '') FROM fetch_log ORDER BY id`)
	for _, in := range infos {
		o.Infos = append(o.Infos, fmt.Sprintf("new %d muted %d marked %d starred %d muted_ids %d", in.New, in.Muted, in.MarkedRead, in.Starred, len(in.MutedIDs)))
	}
	return o
}

type ingestCase struct {
	name  string
	rules func(e *env, feed int64) []Filter
	// fetches are the documents committed in turn; initialRead, when set, is the
	// feed's initial-read window for the fetch at that index (items published before
	// it arrive read).
	fetches     [][]byte
	initialRead map[int]time.Time
}

func ingestCases() []ingestCase {
	mixed := func(e *env, _ int64) []Filter {
		return []Filter{
			newFilter("mute", "sponsored"),
			newFilter("mute", "sponsored", "ad"), // second mute rule: muted_by stays the lowest id
			newFilter("mark_read", "weekly"),
			newFilter("star", "urgent"),
			newFilter("highlight", "release"),
		}
	}
	mixedDoc := frss(
		fspec{guid: "p", title: "Plain post", age: 6 * time.Hour},
		fspec{guid: "m", title: "Sponsored: buy now", age: 5 * time.Hour},
		fspec{guid: "w", title: "Weekly roundup", age: 4 * time.Hour},
		fspec{guid: "u", title: "Urgent release notes", age: 3 * time.Hour},
		fspec{guid: "su", title: "Urgent sponsored thing", age: 2 * time.Hour},
		fspec{guid: "mw", title: "Weekly sponsored", age: time.Hour},
	)
	return []ingestCase{
		{name: "text rules", rules: mixed, fetches: [][]byte{mixedDoc}},
		{
			// the older half arrives read: mark_read then takes no effect (no hit, not
			// counted) and a muted item remembers it was read already
			name: "initial read window", rules: mixed, fetches: [][]byte{mixedDoc},
			initialRead: map[int]time.Time{0: base.Add(-3*time.Hour - 30*time.Minute)},
		},
		{
			name: "regex, feed title, folder, author, category, inverted",
			rules: func(e *env, feed int64) []Filter {
				re := newFilter("mute", `spons(or|ored)\b`)
				re.Kind = "regex"
				ft := newFilter("star", "Feed")
				ft.Fields = []string{"feed"}
				ft.Scope, ft.FeedID = "feed", &feed
				one := int64(1)
				fo := newFilter("mark_read", "digest")
				fo.Scope, fo.FolderID = "folder", &one
				au := newFilter("mute", "jane doe")
				au.Fields = []string{"author"}
				ca := newFilter("mark_read", "gossip")
				ca.Fields = []string{"category"}
				inv := newFilter("mute", "keep")
				inv.Invert = true
				inv.Fields = []string{"content"}
				return []Filter{re, ft, fo, au, ca, inv}
			},
			fetches: [][]byte{frss(
				fspec{guid: "1", title: "Sponsored post", body: "<p>keep</p>"},
				fspec{guid: "2", title: "Daily digest", body: "<p>keep</p>"},
				fspec{guid: "3", title: "Plain", author: "Jane Doe", body: "<p>keep</p>"},
				fspec{guid: "4", title: "Plain two", cats: []string{"Gossip"}, body: "<p>keep</p>"},
				fspec{guid: "5", title: "No keyword in body"},
			)},
		},
		{
			// a second fetch: only the new items are decided, the known ones are left alone
			name: "second fetch", rules: mixed,
			fetches: [][]byte{
				frss(fspec{guid: "a", title: "Sponsored a", age: 3 * time.Hour}, fspec{guid: "b", title: "Weekly b", age: 2 * time.Hour}),
				frss(fspec{guid: "a", title: "Sponsored a", age: 3 * time.Hour}, fspec{guid: "b", title: "Weekly b", age: 2 * time.Hour},
					fspec{guid: "c", title: "Urgent sponsored c", age: time.Hour}, fspec{guid: "d", title: "Weekly d", age: 30 * time.Minute}),
			},
		},
		{
			// more than chunkThreshold items: committed in chunks, each pre-evaluated
			name: "chunked", rules: mixed,
			fetches: [][]byte{frss(fnumbered(chunkThreshold+120, func(i int) string {
				switch i % 4 {
				case 0:
					return fmt.Sprintf("Sponsored %d", i)
				case 1:
					return fmt.Sprintf("Weekly %d", i)
				case 2:
					return fmt.Sprintf("Urgent %d", i)
				}
				return fmt.Sprintf("Plain %d", i)
			})...)},
		},
	}
}

func runIngestCase(t *testing.T, c ingestCase, noPreEval bool) (ingestOutcome, int) {
	e := newEnv(t)
	e.db.testNoPreEval = noPreEval
	txMatches := e.countTxMatches()
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET folder_id = 1, retention = 0 WHERE id = ?", id)
	for _, f := range c.rules(e, id) {
		e.mkFilter(f)
	}
	var infos []CommitInfo
	for i, body := range c.fetches {
		snap := e.snap(id)
		if b, ok := c.initialRead[i]; ok {
			snap.InitialReadBefore = b.Unix()
		}
		infos = append(infos, e.commit(e.okResult(snap, body)))
	}
	e.assertMutedInvariant()
	return e.ingestOutcome(infos), *txMatches
}

func TestPreEvaluatedVerdictsMatchInTransaction(t *testing.T) {
	t.Parallel()
	for _, c := range ingestCases() {
		t.Run(c.name, func(t *testing.T) {
			old, oldTx := runIngestCase(t, c, true)
			got, newTx := runIngestCase(t, c, false)
			require.NotEmpty(t, old.Items)
			require.Equal(t, old, got, "the pre-evaluated path decides every item as the in-transaction one")
			require.NotZero(t, oldTx, "the old path matches inside the transaction")
			require.Zero(t, newTx, "the new path does no matching inside the transaction")
		})
	}
}

// A sanity check on the table itself: the cases exercise every verdict.
func TestIngestCasesCoverEveryEffect(t *testing.T) {
	t.Parallel()
	var muted, marked, starred, wasRead bool
	for _, c := range ingestCases() {
		o, _ := runIngestCase(t, c, false)
		for _, in := range o.Infos {
			var n, m, mr, s, ids int
			_, err := fmt.Sscanf(in, "new %d muted %d marked %d starred %d muted_ids %d", &n, &m, &mr, &s, &ids)
			require.NoError(t, err)
			muted, marked, starred = muted || m > 0, marked || mr > 0, starred || s > 0
		}
		for _, it := range o.Items {
			if len(it) > 2 && it[len(it)-2:] == ":1" {
				wasRead = true
			}
		}
	}
	require.True(t, muted && marked && starred && wasRead)
}

// A filter write between the evaluation and the transaction moves the generation:
// the transaction discards the early results and evaluates with the new rules.
func TestPreEvalFallsBackWhenFiltersChange(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	txMatches := e.countTxMatches()
	id := e.addFeed("http://a.example/feed")
	e.mkFilter(newFilter("mute", "spam"))
	e.fetchBody(id, frss(fspec{guid: "warm", title: "warm up"})) // the cache is current now

	var eggs Filter
	e.db.testAfterPreEval = func() {
		e.db.testAfterPreEval = nil
		eggs = e.mkFilter(newFilter("mute", "eggs"))
	}
	*txMatches = 0
	info := e.fetchBody(id, frss(fspec{guid: "warm", title: "warm up"},
		fspec{guid: "1", title: "spam one"}, fspec{guid: "2", title: "eggs two"}, fspec{guid: "3", title: "plain"}))
	require.Equal(t, 3, info.New)
	require.Equal(t, 2, info.Muted, "the rule created after the evaluation applies")
	require.Equal(t, []string{"eggs two"}, e.titles("muted_by = ?", eggs.ID))
	require.Equal(t, 3, *txMatches, "every new item was evaluated again inside the transaction")

	// a feed moved to another folder in between: the folder rules differ, so again
	nine := int64(9)
	e.exec("INSERT INTO folders (id, name, position) VALUES (9, 'Other', 3)")
	fr := newFilter("star", "news")
	fr.Scope, fr.FolderID = "folder", &nine
	e.mkFilter(fr)
	e.db.testAfterPreEval = func() {
		e.db.testAfterPreEval = nil
		e.exec("UPDATE feeds SET folder_id = 9 WHERE id = ?", id)
	}
	*txMatches = 0
	info = e.fetchBody(id, frss(fspec{guid: "4", title: "news four"}))
	require.Equal(t, 1, info.Starred, "the folder the transaction sees decides")
	require.Equal(t, 1, *txMatches)

	// nothing changed: the early results are used
	*txMatches = 0
	info = e.fetchBody(id, frss(fspec{guid: "5", title: "spam five"}, fspec{guid: "6", title: "news six"}))
	require.Equal(t, 1, info.Muted)
	require.Equal(t, 1, info.Starred)
	require.Zero(t, *txMatches)
}

// The reader can compile the rules at a generation a filter write already bumped
// but has not committed (the bump happens inside its transaction): the set then
// holds the old rules under the new generation. The rule-list comparison catches
// it and the transaction evaluates with the committed rules.
func TestPreEvalCatchesRulesReadBeforeTheirCommit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	txMatches := e.countTxMatches()
	id := e.addFeed("http://a.example/feed")
	f := e.mkFilter(newFilter("mute", "spam"))
	e.fetchBody(id, frss(fspec{guid: "warm", title: "warm up"}))

	// the filter write's bump, before its rows are visible
	e.db.filterGen.Add(1)
	e.db.testAfterPreEval = func() {
		e.db.testAfterPreEval = nil
		// ... and its commit, after the reader compiled the old rules
		e.exec(`UPDATE filters SET terms = '["eggs"]' WHERE id = ?`, f.ID)
	}
	*txMatches = 0
	info := e.fetchBody(id, frss(fspec{guid: "1", title: "spam one"}, fspec{guid: "2", title: "eggs two"}))
	require.Equal(t, 1, info.Muted)
	require.Equal(t, []string{"eggs two"}, e.titles("muted_by IS NOT NULL"), "the committed rule decides, not the early read")
	require.Equal(t, 2, *txMatches)
}

// Without any change in between, no rule is evaluated inside the transaction, on a
// cold cache (the first commit compiles it) as on a warm one, regex rules included.
func TestWriteTransactionDoesNoFilterMatching(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	txMatches := e.countTxMatches()
	id := e.addFeed("http://a.example/feed")
	re := newFilter("mute", `(a|b|c|d)+x`)
	re.Kind = "regex"
	re.Fields = []string{"title", "content"}
	e.mkFilter(re)
	e.mkFilter(newFilter("star", "urgent"))

	info := e.fetchBody(id, frss(fspec{guid: "1", title: "abcdx"}, fspec{guid: "2", title: "urgent"}, fspec{guid: "3", title: "plain"}))
	require.Equal(t, 1, info.Muted)
	require.Equal(t, 1, info.Starred)
	info = e.fetchBody(id, frss(fspec{guid: "4", title: "ddddx"}, fspec{guid: "5", title: "urgent too"}))
	require.Equal(t, 1, info.Muted)
	require.Equal(t, 1, info.Starred)
	require.Zero(t, *txMatches, "the transaction only applied the early results")
}

// ---- trim backlog ----

// A fetch commit trims one bounded batch and says when it filled it.
func TestCommitReportsTrimPending(t *testing.T) {
	withTrimBatch(t, 100)
	e := newEnv(t)
	id := e.loadFeed("http://a.example/feed", 450)
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)

	info := e.fetchBody(id, rss(numbered(450)...))
	require.EqualValues(t, 100, info.Trimmed)
	require.True(t, info.TrimPending, "300 more are over the cap")

	_, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	info = e.fetchBody(id, rss(numbered(450)...))
	require.Zero(t, info.Trimmed)
	require.False(t, info.TrimPending)

	// a trim that fits its batch exactly is reported as possibly unfinished; the
	// follow-up job then finds nothing
	e2 := newEnv(t)
	id2 := e2.loadFeed("http://a.example/feed", 150)
	e2.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id2)
	info = e2.fetchBody(id2, rss(numbered(150)...))
	require.EqualValues(t, 100, info.Trimmed)
	require.True(t, info.TrimPending)
	n, more, err := e2.db.TrimOnlyBudget(e2.ctx, id2, fetch.TriggerRetention, TrimBudget{Total: time.Minute})
	require.NoError(t, err)
	require.Zero(t, n)
	require.False(t, more)
}

// A trim job stops once its budget is spent and reports the rest; repeated jobs
// finish the backlog, each with its own fetch_log row, and match one long trim.
func TestTrimOnlyBudgetStopsAndResumes(t *testing.T) {
	withTrimBatch(t, 100)
	e := newEnv(t)
	id := e.loadFeed("http://a.example/feed", 600)
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)

	b := TrimBudget{Total: time.Nanosecond, PerBatch: 10 * time.Second} // one batch per job
	var total int64
	jobs := 0
	for {
		n, more, err := e.db.TrimOnlyBudget(e.ctx, id, fetch.TriggerRetention, b)
		require.NoError(t, err)
		jobs++
		total += n
		if !more {
			break
		}
		require.EqualValues(t, 100, n, "a spent budget stops after the first batch")
		require.Less(t, jobs, 20)
	}
	require.EqualValues(t, 550, total)
	require.Equal(t, 50, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Equal(t, 6, jobs, "five full batches, then the job that finds the last 50")
	require.Equal(t, 6, e.count("SELECT count(*) FROM fetch_log WHERE feed_id = ? AND outcome = 'trim_only'", id))
	require.Equal(t, 550, e.count("SELECT sum(trimmed_items) FROM fetch_log WHERE feed_id = ? AND outcome = 'trim_only'", id))

	// with room in the budget one job does it all
	e2 := newEnv(t)
	id2 := e2.loadFeed("http://a.example/feed", 600)
	e2.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id2)
	n, more, err := e2.db.TrimOnlyBudget(e2.ctx, id2, fetch.TriggerRetention, TrimBudget{Total: time.Minute, PerBatch: 10 * time.Second})
	require.NoError(t, err)
	require.False(t, more)
	require.EqualValues(t, 550, n)
}

// With PerBatch set a batch in progress is not cut short by ctx; ctx is checked
// between batches, and the job reports that work is left.
func TestTrimOnlyBudgetStopsBetweenBatches(t *testing.T) {
	withTrimBatch(t, 100)
	e := newEnv(t)
	id := e.loadFeed("http://a.example/feed", 400)
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	afterTrimBatch = cancel // cancelled as soon as the first batch commits
	t.Cleanup(func() { afterTrimBatch = nil })
	n, more, err := e.db.TrimOnlyBudget(ctx, id, fetch.TriggerRetention, TrimBudget{Total: time.Minute, PerBatch: 10 * time.Second})
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, more)
	require.EqualValues(t, 100, n)
	require.Equal(t, 300, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
}
