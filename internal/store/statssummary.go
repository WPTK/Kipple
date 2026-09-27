package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StatsReadSeconds and StatsReadScroll are the thresholds that make an open a read (design §8).
const (
	StatsReadSeconds = 10
	StatsReadScroll  = 25
	statsSourcesMax  = 300
	statsNeverMax    = 500
)

// StatsSummaryParams selects the range of a summary. From and To are inclusive local dates
// (YYYY-MM-DD); the caller resolves the range key. Now is the current time.
type StatsSummaryParams struct {
	Key             string // week, month, year, all or custom
	From, To        string // "" for all: from the first event through today
	IncludeInferred bool
	Now             time.Time
}

// The types below are the GET /api/stats/summary response.
type (
	StatsRange struct {
		Key  string `json:"key"`
		From string `json:"from"`
		To   string `json:"to"`
		Days int    `json:"days"`
	}
	StatsTotals struct {
		ItemsRead     int   `json:"items_read"`
		Opens         int   `json:"opens"`
		ActiveSeconds int64 `json:"active_seconds"`
		DaysActive    int   `json:"days_active"`
	}
	StatsDaily struct {
		Date          string `json:"date"`
		ItemsRead     int    `json:"items_read"`
		ActiveSeconds int64  `json:"active_seconds"`
	}
	StatsStreaks struct {
		Current    int     `json:"current"`
		Longest    int     `json:"longest"`
		LongestEnd *string `json:"longest_end"`
	}
	StatsHeat struct {
		Weekday       int   `json:"weekday"`
		Hour          int   `json:"hour"`
		ActiveSeconds int64 `json:"active_seconds"`
		Opens         int   `json:"opens"`
	}
	StatsBusyDay struct {
		Weekday       int   `json:"weekday"`
		ActiveSeconds int64 `json:"active_seconds"`
		Opens         int   `json:"opens"`
	}
	StatsBusyHour struct {
		Hour          int   `json:"hour"`
		ActiveSeconds int64 `json:"active_seconds"`
		Opens         int   `json:"opens"`
	}
	StatsLongest struct {
		ItemID    string `json:"item_id"`
		Title     string `json:"title"`
		FeedTitle string `json:"feed_title"`
		Seconds   int64  `json:"seconds"`
		Date      string `json:"date"`
	}
	StatsBehavior struct {
		BusiestWeekday *StatsBusyDay  `json:"busiest_weekday"`
		BusiestHour    *StatsBusyHour `json:"busiest_hour"`
		AvgReadSeconds *float64       `json:"avg_read_seconds"`
		LongestRead    *StatsLongest  `json:"longest_read"`
	}
	StatsSource struct {
		FeedID           string   `json:"feed_id"`
		FeedTitle        string   `json:"feed_title"`
		FolderID         *string  `json:"folder_id"`
		FolderName       *string  `json:"folder_name"`
		ItemsRead        int      `json:"items_read"`
		Opens            int      `json:"opens"`
		ActiveSeconds    int64    `json:"active_seconds"`
		AvgReadSeconds   *float64 `json:"avg_read_seconds"`
		TimedSeconds     int64    `json:"timed_seconds"` // read time of the read opens that have any (AvgReadSeconds numerator)
		TimedItems       int      `json:"timed_items"`   // those opens (AvgReadSeconds denominator)
		BounceRate       *float64 `json:"bounce_rate"`
		OpenOriginalRate *float64 `json:"open_original_rate"`
		TrackedOpens     int      `json:"tracked_opens"`
		Bounces          int      `json:"bounces"`
		ItemsOpened      int      `json:"items_opened"`
		ItemsOriginal    int      `json:"items_original"`
		Stars            int      `json:"stars"`
		Subscribed       bool     `json:"subscribed"`
	}
	StatsNeverOpened struct {
		FeedID       string  `json:"feed_id"`
		Title        string  `json:"title"`
		FolderName   *string `json:"folder_name"`
		SubscribedOn string  `json:"subscribed_on"`
	}
	// StatsSummary is the whole response.
	StatsSummary struct {
		Enabled        bool          `json:"enabled"`
		TZ             string        `json:"tz"`
		WeekStart      string        `json:"week_start"`
		Range          *StatsRange   `json:"range,omitempty"`
		FirstEventDate *string       `json:"first_event_date"`
		Totals         StatsTotals   `json:"totals"`
		Daily          []StatsDaily  `json:"daily"`
		Streaks        StatsStreaks  `json:"streaks"`
		Heatmap        []StatsHeat   `json:"heatmap"`
		Behavior       StatsBehavior `json:"behavior"`
		Sources        []StatsSource `json:"sources"`
		// SourcesTruncated is true when more than statsSourcesMax feeds had activity and the list was cut.
		SourcesTruncated bool               `json:"sources_truncated"`
		NeverOpened      []StatsNeverOpened `json:"never_opened"`
	}
)

// StatsSettings reads the stats switch, the time zone location and its name, and the week start.
func StatsSettings(ctx context.Context, q Querier) (enabled bool, loc *time.Location, tz, weekStart string, err error) {
	if enabled, err = StatsEnabled(ctx, q); err != nil {
		return
	}
	tz = settingString(ctx, q, "tz", "America/New_York")
	loc = LoadLocation(ctx, q)
	if loc == time.UTC && tz != "UTC" {
		tz = "UTC" // an unknown zone falls back to UTC, as at write time
	}
	weekStart = settingString(ctx, q, "stats.week_start", "sunday")
	if weekStart != "monday" {
		weekStart = "sunday"
	}
	return
}

// StatsFirstEventDate is the local date of the oldest stats row (the smallest local_date of any
// row, not the date of the lowest id: a time zone change can leave older ids with later dates),
// or "" when there are none.
func StatsFirstEventDate(ctx context.Context, q Querier) (string, error) {
	return statsFirstDate(ctx, q, math.MaxInt64)
}

const dateLayout = "2006-01-02"

func parseLocalDate(s string) time.Time {
	t, _ := time.ParseInLocation(dateLayout, s, time.UTC)
	return t
}

// The summary queries. The three big ones name their covering index with INDEXED BY (migration
// 0009), so the planner cannot fall back to a wide scan when it has no statistics, and a missing
// index is a hard error rather than a slow answer. Any future rebuild of stats_events (a table
// recreate for a new column or CHECK) must recreate the three 0009 indexes. TestStatsSummaryPlans
// runs EXPLAIN QUERY PLAN over every hinted query below and fails if one stops using its index.
// Every query is bounded by the newest row id captured when the summary started (rowid <= maxid),
// so ingest that lands mid-summary cannot split a session (an open seen without its read time).
const (
	sqlFirstDate = `SELECT MIN(d) FROM (
		SELECT MIN(local_date) AS d FROM stats_events INDEXED BY idx_stats_open_cov WHERE kind = 'open' AND rowid <= ?1
		UNION ALL SELECT MIN(local_date) FROM stats_events INDEXED BY idx_stats_rt_cov WHERE kind = 'read_time' AND rowid <= ?1
		UNION ALL SELECT MIN(local_date) FROM stats_events INDEXED BY idx_stats_scroll_cov WHERE kind = 'scroll' AND rowid <= ?1
		UNION ALL SELECT MIN(local_date) FROM stats_events INDEXED BY idx_stats_kind_ts
			WHERE kind IN ('star', 'unstar', 'open_original', 'share') AND rowid <= ?1)`
	sqlLegacyCut = `SELECT MIN(ts) FROM stats_events INDEXED BY idx_stats_kind_ts WHERE kind = ?1 AND rowid <= ?2`
	sqlOpens     = `SELECT rowid, local_date, local_hour, feed_id, item_id, COALESCE(session_key, ''), ts
		FROM stats_events INDEXED BY idx_stats_open_cov
		WHERE kind = 'open' AND inferred <= ?1 AND local_date BETWEEN ?2 AND ?3 AND rowid <= ?4`
	sqlReadTime = `SELECT local_date, local_hour, feed_id, session_key, SUM(value)
		FROM stats_events INDEXED BY idx_stats_rt_cov
		WHERE kind = 'read_time' AND local_date BETWEEN ?1 AND ?2 AND rowid <= ?3
		GROUP BY local_date, local_hour, feed_id, session_key`
	sqlScroll = `SELECT session_key, value FROM stats_events INDEXED BY idx_stats_scroll_cov
		WHERE kind = 'scroll' AND local_date BETWEEN ?1 AND ?2 AND rowid <= ?3`
	sqlStars = `SELECT feed_id, COUNT(*), MAX(id) FROM stats_events INDEXED BY idx_stats_kind_ts
		WHERE kind = 'star' AND inferred <= ?1 AND ts BETWEEN ?2 AND ?3 AND local_date BETWEEN ?4 AND ?5 AND id <= ?6 GROUP BY feed_id`
	sqlOrig = `SELECT feed_id, COUNT(DISTINCT item_id) FROM stats_events INDEXED BY idx_stats_kind_ts
		WHERE kind = 'open_original' AND inferred <= ?1 AND ts BETWEEN ?2 AND ?3 AND local_date BETWEEN ?4 AND ?5 AND id <= ?6 GROUP BY feed_id`
	// sqlStreaks probes each distinct open date once and stops at its first qualifying open. Both
	// the date list and the probe read idx_stats_open_cov; the session lookups read idx_stats_session.
	sqlStreaks = `SELECT d.local_date FROM (
		SELECT local_date FROM stats_events INDEXED BY idx_stats_open_cov WHERE kind = 'open' AND inferred <= ?2 AND rowid <= ?5 GROUP BY local_date) d
		WHERE EXISTS (SELECT 1 FROM stats_events e INDEXED BY idx_stats_open_cov
		 WHERE e.kind = 'open' AND e.inferred <= ?2 AND e.local_date = d.local_date AND e.rowid <= ?5 AND (e.ts < ?1
		  OR EXISTS (SELECT 1 FROM stats_events r WHERE r.session_key = e.session_key AND r.kind = 'scroll' AND r.value >= ?3 AND r.id <= ?5)
		  OR (SELECT SUM(r.value) FROM stats_events r WHERE r.session_key = e.session_key AND r.kind = 'read_time' AND r.id <= ?5) >= ?4))
		ORDER BY d.local_date`
	// sqlNameByReadTime names a feed that only has read time in the range from its newest read_time
	// snapshot (feed_title is NOT NULL on every row).
	sqlNameByReadTime = `SELECT feed_title, folder_id, folder_name FROM stats_events INDEXED BY idx_stats_feed
		WHERE feed_id = ?1 AND kind = 'read_time' AND ts BETWEEN ?2 AND ?3 AND local_date BETWEEN ?4 AND ?5 AND id <= ?6
		ORDER BY ts DESC, id DESC LIMIT 1`
)

// statsHinted is every INDEXED BY query of the summary with representative arguments (for the plan
// test), keyed by name, with the index each must read.
type statsHint struct {
	name, sql, index string
	hits             int // how many times the plan must read that index
	args             []any
}

func statsHinted() []statsHint {
	const lo, hi, max = "2026-01-01", "2026-12-31", int64(1) << 40
	return []statsHint{
		{"first_date", sqlFirstDate, "idx_stats_open_cov", 1, []any{max}},
		{"first_date", sqlFirstDate, "idx_stats_rt_cov", 1, []any{max}},
		{"first_date", sqlFirstDate, "idx_stats_scroll_cov", 1, []any{max}},
		{"first_date", sqlFirstDate, "idx_stats_kind_ts", 1, []any{max}},
		{"legacy_cut", sqlLegacyCut, "idx_stats_kind_ts", 1, []any{"scroll", max}},
		{"opens", sqlOpens, "idx_stats_open_cov", 1, []any{0, lo, hi, max}},
		{"read_time", sqlReadTime, "idx_stats_rt_cov", 1, []any{lo, hi, max}},
		{"scroll", sqlScroll, "idx_stats_scroll_cov", 1, []any{lo, hi, max}},
		{"stars", sqlStars, "idx_stats_kind_ts", 1, []any{0, 0, 1 << 40, lo, hi, max}},
		{"open_original", sqlOrig, "idx_stats_kind_ts", 1, []any{0, 0, 1 << 40, lo, hi, max}},
		{"streaks", sqlStreaks, "idx_stats_open_cov", 2, []any{0, 0, StatsReadScroll, StatsReadSeconds, max}},
		{"name by read time", sqlNameByReadTime, "idx_stats_feed", 1, []any{1, 0, 1 << 40, lo, hi, max}},
	}
}

// eachRow runs scan for every row and fails on a mid-iteration error, so a broken read never
// yields partial numbers. It always closes rows.
func eachRow(rows *sql.Rows, scan func() error) error {
	for rows.Next() {
		if err := scan(); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

func statsFirstDate(ctx context.Context, q Querier, maxID int64) (string, error) {
	var d sql.NullString
	if err := q.QueryRowContext(ctx, sqlFirstDate, maxID).Scan(&d); err != nil {
		return "", err
	}
	return d.String, nil
}

// statsLegacyCutoff is the ts of the earliest read_time or scroll event; opens before it predate
// the sender and count as reads. MaxInt64 when no such event exists (every open is legacy).
func statsLegacyCutoff(ctx context.Context, q Querier, maxID int64) (int64, error) {
	cut := int64(math.MaxInt64)
	for _, k := range []string{"read_time", "scroll"} {
		var ts sql.NullInt64
		if err := q.QueryRowContext(ctx, sqlLegacyCut, k, maxID).Scan(&ts); err != nil {
			return 0, err
		}
		if ts.Valid && ts.Int64 < cut {
			cut = ts.Int64
		}
	}
	return cut, nil
}

type starAgg struct {
	n     int
	maxID int64
}

// statsStarsOrig reads the per-feed star counts (with the newest star row) and the distinct items
// with an open_original event for a range.
func statsStarsOrig(ctx context.Context, q Querier, inc int, loTS, hiTS int64, from, to string, maxID int64) (stars map[int64]starAgg, origs map[int64]int, err error) {
	stars = map[int64]starAgg{}
	stRows, err := q.QueryContext(ctx, sqlStars, inc, loTS, hiTS, from, to, maxID)
	if err != nil {
		return nil, nil, err
	}
	if err := eachRow(stRows, func() error {
		var f int64
		var a starAgg
		if err := stRows.Scan(&f, &a.n, &a.maxID); err != nil {
			return err
		}
		stars[f] = a
		return nil
	}); err != nil {
		return nil, nil, err
	}
	origs, err = statsCountByFeed(ctx, q, sqlOrig, inc, loTS, hiTS, from, to, maxID)
	return stars, origs, err
}

// statsRTKey is a read-time group: one local hour of one feed.
type statsRTKey struct {
	date string
	hour int
	feed int64
}

// statsScroll is the deepest scroll per session for a local date range.
func statsScroll(ctx context.Context, q Querier, from, to string, maxID int64) (map[string]int64, error) {
	sessScroll := map[string]int64{}
	scRows, err := q.QueryContext(ctx, sqlScroll, from, to, maxID)
	if err != nil {
		return nil, err
	}
	if err := eachRow(scRows, func() error {
		var sk string
		var v int64
		if err := scRows.Scan(&sk, &v); err != nil {
			return err
		}
		if v > sessScroll[sk] {
			sessScroll[sk] = v
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return sessScroll, nil
}

// statsReadTime reads the read time (by date, hour and feed, and by session) for a local date
// range, streamed from a covering index (migration 0009).
func statsReadTime(ctx context.Context, q Querier, from, to string, maxID int64) (rt map[statsRTKey]int64, sessRT map[string]int64, err error) {
	rtRows, err := q.QueryContext(ctx, sqlReadTime, from, to, maxID)
	if err != nil {
		return nil, nil, err
	}
	rt, sessRT = map[statsRTKey]int64{}, map[string]int64{}
	if err := eachRow(rtRows, func() error {
		var k statsRTKey
		var sk string
		var v int64
		if err := rtRows.Scan(&k.date, &k.hour, &k.feed, &sk, &v); err != nil {
			return err
		}
		rt[k] += v
		sessRT[sk] += v
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return rt, sessRT, nil
}

// statsGroup runs the heavy queries of one summary with at most `limit` in flight, so one request
// never takes more reader connections than that (the pool has four, and the UI's other reads must
// not starve). The first error cancels the siblings' context; Wait returns that error, or the
// caller's context error when its cancellation skipped a task, never partial numbers.
type statsGroup struct {
	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
	err    error
}

func newStatsGroup(ctx context.Context, limit int) *statsGroup {
	c, cancel := context.WithCancel(ctx)
	return &statsGroup{ctx: c, cancel: cancel, sem: make(chan struct{}, limit)}
}

func (g *statsGroup) fail(err error) { g.once.Do(func() { g.err = err; g.cancel() }) }

// Go starts fn once a slot is free (tasks start in call order).
func (g *statsGroup) Go(fn func(ctx context.Context) error) {
	if err := g.ctx.Err(); err != nil { // a ready slot must not win over a cancellation
		g.fail(err)
		return
	}
	select {
	case g.sem <- struct{}{}:
	case <-g.ctx.Done():
		g.fail(g.ctx.Err())
		return
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer func() { <-g.sem }()
		if err := fn(g.ctx); err != nil {
			g.fail(err)
		}
	}()
}

func (g *statsGroup) Wait() error {
	g.wg.Wait()
	g.cancel()
	return g.err
}

// maxConcurrentStatsQueries is the most reader connections one summary holds at once.
const maxConcurrentStatsQueries = 3

// StatsSummaryFor computes the summary (design §8). It uses only short read queries and holds
// nothing across the caller's response write. p.From/p.To must already be resolved except for
// Key "all" (From is then the first event date) and are inclusive local dates.
func StatsSummaryFor(ctx context.Context, q Querier, p StatsSummaryParams) (*StatsSummary, error) {
	enabled, loc, tz, ws, err := StatsSettings(ctx, q)
	if err != nil {
		return nil, err
	}
	out := &StatsSummary{Enabled: enabled, TZ: tz, WeekStart: ws,
		Daily: []StatsDaily{}, Heatmap: []StatsHeat{}, Sources: []StatsSource{}, NeverOpened: []StatsNeverOpened{}}
	if !enabled {
		return out, nil
	}
	inc := 0
	if p.IncludeInferred {
		inc = 1
	}
	now := p.Now.In(loc)
	today := now.Format(dateLayout)

	// Snapshot boundary: every query below reads only rows up to this id.
	var maxID int64
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM stats_events").Scan(&maxID); err != nil {
		return nil, err
	}

	first, err := statsFirstDate(ctx, q, maxID)
	if err != nil {
		return nil, err
	}
	if first != "" {
		out.FirstEventDate = &first
	}
	from, to := p.From, p.To
	if p.Key == "all" {
		from, to = first, today
		if from == "" || from > to {
			from = today
		}
	}
	fromT, toT := parseLocalDate(from), parseLocalDate(to)
	days := int(toT.Sub(fromT).Hours()/24) + 1
	out.Range = &StatsRange{Key: p.Key, From: from, To: to, Days: days}

	// ts bounds narrow the (kind, ts) scans of the small kinds; local_date is the exact filter (the
	// slack covers any zone offset and a tz change since the row was written).
	loTS := fromT.Unix() - 2*86400
	hiTS := toT.Unix() + 4*86400

	cut, err := statsLegacyCutoff(ctx, q, maxID)
	if err != nil {
		return nil, err
	}

	// Opens in the range, from the covering index (migration 0009); no table rows are read.
	type openRow struct {
		id            int64
		date, session string
		hour          int
		item, feed    int64
		rt            int64
		legacy, read  bool
	}
	var (
		opens      []openRow
		rt         map[statsRTKey]int64
		sessRT     map[string]int64
		sessScroll map[string]int64
		streaks    StatsStreaks
		stars      map[int64]starAgg
		origs      map[int64]int
	)
	// The heaviest scans start first; at most three run at once (see statsGroup).
	g := newStatsGroup(ctx, maxConcurrentStatsQueries)
	g.Go(func(c context.Context) (e error) {
		rt, sessRT, e = statsReadTime(c, q, from, to, maxID)
		return
	})
	g.Go(func(c context.Context) (e error) {
		streaks, e = statsStreaks(c, q, cut, inc, today, maxID)
		return
	})
	g.Go(func(c context.Context) error {
		rows, err := q.QueryContext(c, sqlOpens, inc, from, to, maxID)
		if err != nil {
			return err
		}
		return eachRow(rows, func() error {
			var o openRow
			var ts int64
			if err := rows.Scan(&o.id, &o.date, &o.hour, &o.feed, &o.item, &o.session, &ts); err != nil {
				return err
			}
			o.legacy = ts < cut
			opens = append(opens, o)
			return nil
		})
	})
	g.Go(func(c context.Context) (e error) {
		sessScroll, e = statsScroll(c, q, from, to, maxID)
		return
	})
	g.Go(func(c context.Context) (e error) {
		stars, origs, e = statsStarsOrig(c, q, inc, loTS, hiTS, from, to, maxID)
		return
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for i := range opens {
		o := &opens[i]
		o.rt = sessRT[o.session]
		o.read = o.legacy || o.rt >= StatsReadSeconds || sessScroll[o.session] >= StatsReadScroll
	}

	// ---- aggregate ----
	type feedAgg struct {
		readItems, openItems map[int64]struct{}
		opens, bounces       int
		nonLegacy            int
		secs                 int64
		readSecs             int64
		readWithTime         int
	}
	feeds := map[int64]*feedAgg{}
	fa := func(id int64) *feedAgg {
		a := feeds[id]
		if a == nil {
			a = &feedAgg{readItems: map[int64]struct{}{}, openItems: map[int64]struct{}{}}
			feeds[id] = a
		}
		return a
	}
	dayItems := map[string]map[int64]struct{}{}
	daySecs := map[string]int64{}
	allRead := map[int64]struct{}{}
	readDays := map[string]struct{}{}
	type cell struct{ wd, h int }
	heat := map[cell]*StatsHeat{}
	heatCell := func(wd, h int) *StatsHeat {
		c := heat[cell{wd, h}]
		if c == nil {
			c = &StatsHeat{Weekday: wd, Hour: h}
			heat[cell{wd, h}] = c
		}
		return c
	}
	var readTimeSum int64
	var readWithTime int
	var longest *openRow
	for i := range opens {
		o := &opens[i]
		a := fa(o.feed)
		a.opens++
		a.openItems[o.item] = struct{}{}
		heatCell(int(parseLocalDate(o.date).Weekday()), o.hour).Opens++
		if !o.legacy {
			a.nonLegacy++
			if !o.read {
				a.bounces++
			}
		}
		if o.read {
			a.readItems[o.item] = struct{}{}
			allRead[o.item] = struct{}{}
			readDays[o.date] = struct{}{}
			m := dayItems[o.date]
			if m == nil {
				m = map[int64]struct{}{}
				dayItems[o.date] = m
			}
			m[o.item] = struct{}{}
			if o.rt > 0 {
				a.readSecs += o.rt
				a.readWithTime++
				readTimeSum += o.rt
				readWithTime++
				if longest == nil || o.rt > longest.rt {
					longest = o
				}
			}
		}
	}
	var active int64
	for k, v := range rt {
		active += v
		daySecs[k.date] += v
		fa(k.feed).secs += v
		heatCell(int(parseLocalDate(k.date).Weekday()), k.hour).ActiveSeconds += v
	}

	out.Totals = StatsTotals{ItemsRead: len(allRead), Opens: len(opens), ActiveSeconds: active, DaysActive: len(readDays)}

	// daily, zero-filled
	out.Daily = make([]StatsDaily, 0, days)
	for d, i := fromT, 0; i < days; d, i = d.AddDate(0, 0, 1), i+1 {
		ds := d.Format(dateLayout)
		out.Daily = append(out.Daily, StatsDaily{Date: ds, ItemsRead: len(dayItems[ds]), ActiveSeconds: daySecs[ds]})
	}

	// heatmap and busiest
	for _, c := range heat {
		if c.ActiveSeconds > 0 || c.Opens > 0 {
			out.Heatmap = append(out.Heatmap, *c)
		}
	}
	sort.Slice(out.Heatmap, func(i, j int) bool {
		a, b := out.Heatmap[i], out.Heatmap[j]
		if a.Weekday != b.Weekday {
			return a.Weekday < b.Weekday
		}
		return a.Hour < b.Hour
	})
	var byDay [7]StatsBusyDay
	var byHour [24]StatsBusyHour
	for i := range byDay {
		byDay[i].Weekday = i
	}
	for i := range byHour {
		byHour[i].Hour = i
	}
	for _, c := range out.Heatmap {
		byDay[c.Weekday].ActiveSeconds += c.ActiveSeconds
		byDay[c.Weekday].Opens += c.Opens
		byHour[c.Hour].ActiveSeconds += c.ActiveSeconds
		byHour[c.Hour].Opens += c.Opens
	}
	for i := range byDay {
		b := byDay[i]
		if b.ActiveSeconds == 0 && b.Opens == 0 {
			continue
		}
		if out.Behavior.BusiestWeekday == nil || b.ActiveSeconds > out.Behavior.BusiestWeekday.ActiveSeconds ||
			(b.ActiveSeconds == out.Behavior.BusiestWeekday.ActiveSeconds && b.Opens > out.Behavior.BusiestWeekday.Opens) {
			bb := b
			out.Behavior.BusiestWeekday = &bb
		}
	}
	for i := range byHour {
		b := byHour[i]
		if b.ActiveSeconds == 0 && b.Opens == 0 {
			continue
		}
		if out.Behavior.BusiestHour == nil || b.ActiveSeconds > out.Behavior.BusiestHour.ActiveSeconds ||
			(b.ActiveSeconds == out.Behavior.BusiestHour.ActiveSeconds && b.Opens > out.Behavior.BusiestHour.Opens) {
			bb := b
			out.Behavior.BusiestHour = &bb
		}
	}
	if readWithTime > 0 {
		v := float64(readTimeSum) / float64(readWithTime)
		out.Behavior.AvgReadSeconds = &v
	}
	if longest != nil {
		l := &StatsLongest{ItemID: strconv.FormatInt(longest.item, 10), Seconds: longest.rt, Date: longest.date}
		var it, ft sql.NullString
		if err := q.QueryRowContext(ctx, "SELECT item_title, feed_title FROM stats_events WHERE id = ?", longest.id).Scan(&it, &ft); err != nil {
			return nil, err
		}
		l.Title, l.FeedTitle = it.String, ft.String
		out.Behavior.LongestRead = l
	}

	// streaks (all time)
	out.Streaks = streaks

	// sources: one per feed with an open, read time or star in the range
	live := map[int64]struct{}{}
	fRows, err := q.QueryContext(ctx, "SELECT id FROM feeds WHERE disabled_reason IS NOT 'archive'")
	if err != nil {
		return nil, err
	}
	if err := eachRow(fRows, func() error {
		var f int64
		if err := fRows.Scan(&f); err != nil {
			return err
		}
		live[f] = struct{}{}
		return nil
	}); err != nil {
		return nil, err
	}
	latest := map[int64]int64{} // feed -> id of its latest open or star in range (the name snapshot)
	for i := range opens {
		if o := &opens[i]; o.id > latest[o.feed] {
			latest[o.feed] = o.id
		}
	}
	for f, a := range stars {
		if a.maxID > latest[f] {
			latest[f] = a.maxID
		}
		fa(f) // a star-only feed still has activity
	}
	for id, a := range feeds {
		s := StatsSource{FeedID: strconv.FormatInt(id, 10), ItemsRead: len(a.readItems), Opens: a.opens, ActiveSeconds: a.secs, Stars: stars[id].n,
			TrackedOpens: a.nonLegacy, Bounces: a.bounces, ItemsOpened: len(a.openItems), ItemsOriginal: origs[id],
			TimedSeconds: a.readSecs, TimedItems: a.readWithTime}
		if a.readWithTime > 0 {
			v := float64(a.readSecs) / float64(a.readWithTime)
			s.AvgReadSeconds = &v
		}
		if a.nonLegacy > 0 {
			v := float64(a.bounces) / float64(a.nonLegacy)
			s.BounceRate = &v
		}
		if len(a.openItems) > 0 {
			v := math.Min(1, float64(origs[id])/float64(len(a.openItems)))
			s.OpenOriginalRate = &v
		}
		_, s.Subscribed = live[id]
		out.Sources = append(out.Sources, s)
	}
	sort.Slice(out.Sources, func(i, j int) bool {
		a, b := out.Sources[i], out.Sources[j]
		if a.ItemsRead != b.ItemsRead {
			return a.ItemsRead > b.ItemsRead
		}
		if a.ActiveSeconds != b.ActiveSeconds {
			return a.ActiveSeconds > b.ActiveSeconds
		}
		return a.FeedID < b.FeedID
	})
	if len(out.Sources) > statsSourcesMax {
		out.Sources = out.Sources[:statsSourcesMax]
		out.SourcesTruncated = true
	}
	// Names come from the newest snapshot row of each listed feed (one query for at most 300 ids).
	setName := func(src *StatsSource, title string, folder sql.NullInt64, fname sql.NullString) {
		src.FeedTitle = title
		if folder.Valid {
			f := strconv.FormatInt(folder.Int64, 10)
			src.FolderID = &f
		}
		if fname.Valid {
			n := fname.String
			src.FolderName = &n
		}
	}
	byRow := map[int64]int{}
	named := make([]bool, len(out.Sources))
	var args []any
	for i := range out.Sources {
		fid, _ := strconv.ParseInt(out.Sources[i].FeedID, 10, 64)
		if id := latest[fid]; id != 0 {
			byRow[id] = i
			args = append(args, id)
		}
	}
	if len(args) > 0 {
		idRows, err := q.QueryContext(ctx, "SELECT id, feed_title, folder_id, folder_name FROM stats_events WHERE id IN (?"+strings.Repeat(",?", len(args)-1)+")", args...)
		if err != nil {
			return nil, err
		}
		if err := eachRow(idRows, func() error {
			var id int64
			var title string
			var folder sql.NullInt64
			var fname sql.NullString
			if err := idRows.Scan(&id, &title, &folder, &fname); err != nil {
				return err
			}
			i := byRow[id]
			setName(&out.Sources[i], title, folder, fname)
			named[i] = true
			return nil
		}); err != nil {
			return nil, err
		}
	}
	for i := range out.Sources {
		if named[i] {
			continue
		}
		// Only read time fell in the range (a session that spanned its start): the snapshot of its
		// newest read_time row in range, so the source never shows a blank name.
		fid, _ := strconv.ParseInt(out.Sources[i].FeedID, 10, 64)
		var title string
		var folder sql.NullInt64
		var fname sql.NullString
		err := q.QueryRowContext(ctx, sqlNameByReadTime, fid, loTS, hiTS, from, to, maxID).Scan(&title, &folder, &fname)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			setName(&out.Sources[i], title, folder, fname)
		}
	}
	for i := range out.Sources {
		if out.Sources[i].FeedTitle != "" {
			continue
		}
		// A snapshot written after the feed row was gone has an empty name: use the current one.
		var t sql.NullString
		err := q.QueryRowContext(ctx, "SELECT "+feedTitleSQL("f")+" FROM feeds f WHERE f.id = ?", out.Sources[i].FeedID).Scan(&t)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		out.Sources[i].FeedTitle = t.String
	}

	// never opened: current, non-archived feeds subscribed by the end of the range with no open in it
	toEnd := time.Date(toT.Year(), toT.Month(), toT.Day()+1, 0, 0, 0, 0, loc).Unix()
	nRows, err := q.QueryContext(ctx, `SELECT f.id, `+feedTitleSQL("f")+`, fo.name, f.created_at
		FROM feeds f LEFT JOIN folders fo ON fo.id = f.folder_id
		WHERE f.disabled_reason IS NOT 'archive' AND f.created_at < ? ORDER BY f.created_at, f.id`, toEnd)
	if err != nil {
		return nil, err
	}
	if err := eachRow(nRows, func() error {
		var id, created int64
		var title string
		var fname sql.NullString
		if err := nRows.Scan(&id, &title, &fname, &created); err != nil {
			return err
		}
		if a := feeds[id]; a != nil && a.opens > 0 {
			return nil
		}
		if len(out.NeverOpened) >= statsNeverMax {
			return nil
		}
		n := StatsNeverOpened{FeedID: strconv.FormatInt(id, 10), Title: title, SubscribedOn: time.Unix(created, 0).In(loc).Format(dateLayout)}
		if fname.Valid {
			f := fname.String
			n.FolderName = &f
		}
		out.NeverOpened = append(out.NeverOpened, n)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func statsCountByFeed(ctx context.Context, q Querier, query string, args ...any) (map[int64]int, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	m := map[int64]int{}
	if err := eachRow(rows, func() error {
		var f int64
		var n int
		if err := rows.Scan(&f, &n); err != nil {
			return err
		}
		m[f] = n
		return nil
	}); err != nil {
		return nil, err
	}
	return m, nil
}

// statsStreaks computes the all-time streaks over local dates with at least one read open (see
// sqlStreaks). A date later than today (rows written under a different time zone, or a clock that
// moved back) counts as today, so the current streak cannot drop to 0 because of it.
func statsStreaks(ctx context.Context, q Querier, cut int64, inc int, today string, maxID int64) (StatsStreaks, error) {
	var st StatsStreaks
	rows, err := q.QueryContext(ctx, sqlStreaks, cut, inc, StatsReadScroll, StatsReadSeconds, maxID)
	if err != nil {
		return st, err
	}
	var dates []string
	if err := eachRow(rows, func() error {
		var d string
		if err := rows.Scan(&d); err != nil {
			return err
		}
		if d > today {
			d = today
		}
		if n := len(dates); n == 0 || dates[n-1] != d { // ascending, so clamped dates are adjacent
			dates = append(dates, d)
		}
		return nil
	}); err != nil {
		return st, err
	}
	run := 0
	var prev time.Time
	for i, d := range dates {
		t := parseLocalDate(d)
		if i > 0 && t.Sub(prev) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		prev = t
		if run >= st.Longest {
			st.Longest = run
			e := d
			st.LongestEnd = &e
		}
	}
	if n := len(dates); n > 0 {
		last := parseLocalDate(dates[n-1])
		t := parseLocalDate(today)
		if gap := t.Sub(last); gap == 0 || gap == 24*time.Hour {
			st.Current = run
		}
	}
	return st, nil
}
