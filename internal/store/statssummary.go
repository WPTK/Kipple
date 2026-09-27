package store

import (
	"context"
	"database/sql"
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
		Enabled        bool               `json:"enabled"`
		TZ             string             `json:"tz"`
		WeekStart      string             `json:"week_start"`
		Range          *StatsRange        `json:"range,omitempty"`
		FirstEventDate *string            `json:"first_event_date"`
		Totals         StatsTotals        `json:"totals"`
		Daily          []StatsDaily       `json:"daily"`
		Streaks        StatsStreaks       `json:"streaks"`
		Heatmap        []StatsHeat        `json:"heatmap"`
		Behavior       StatsBehavior      `json:"behavior"`
		Sources        []StatsSource      `json:"sources"`
		NeverOpened    []StatsNeverOpened `json:"never_opened"`
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

// StatsFirstEventDate is the local date of the oldest stats row, or "" when there are none.
func StatsFirstEventDate(ctx context.Context, q Querier) (string, error) {
	var d string
	err := q.QueryRowContext(ctx, "SELECT local_date FROM stats_events ORDER BY id LIMIT 1").Scan(&d)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return d, err
}

const dateLayout = "2006-01-02"

func parseLocalDate(s string) time.Time {
	t, _ := time.ParseInLocation(dateLayout, s, time.UTC)
	return t
}

// statsLegacyCutoff is the ts of the earliest read_time or scroll event; opens before it predate
// the sender and count as reads. MaxInt64 when no such event exists (every open is legacy).
func statsLegacyCutoff(ctx context.Context, q Querier) (int64, error) {
	cut := int64(math.MaxInt64)
	for _, k := range []string{"read_time", "scroll"} {
		var ts sql.NullInt64
		if err := q.QueryRowContext(ctx, "SELECT MIN(ts) FROM stats_events WHERE kind = ?", k).Scan(&ts); err != nil {
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
func statsStarsOrig(ctx context.Context, q Querier, inc int, loTS, hiTS int64, from, to string) (stars map[int64]starAgg, origs map[int64]int, err error) {
	stars = map[int64]starAgg{}
	stRows, err := q.QueryContext(ctx, `SELECT feed_id, COUNT(*), MAX(id) FROM stats_events
		WHERE kind = 'star' AND inferred <= ? AND ts BETWEEN ? AND ? AND local_date BETWEEN ? AND ? GROUP BY feed_id`,
		inc, loTS, hiTS, from, to)
	if err != nil {
		return nil, nil, err
	}
	for stRows.Next() {
		var f int64
		var a starAgg
		if err := stRows.Scan(&f, &a.n, &a.maxID); err != nil {
			_ = stRows.Close()
			return nil, nil, err
		}
		stars[f] = a
	}
	if err := stRows.Close(); err != nil {
		return nil, nil, err
	}
	origs, err = statsCountByFeed(ctx, q, `SELECT feed_id, COUNT(DISTINCT item_id) FROM stats_events
		WHERE kind = 'open_original' AND inferred <= ? AND ts BETWEEN ? AND ? AND local_date BETWEEN ? AND ? GROUP BY feed_id`, inc, loTS, hiTS, from, to)
	return stars, origs, err
}

// statsRTKey is a read-time group: one local hour of one feed.
type statsRTKey struct {
	date string
	hour int
	feed int64
}

// statsScroll is the deepest scroll per session for a local date range.
func statsScroll(ctx context.Context, q Querier, from, to string) (map[string]int64, error) {
	sessScroll := map[string]int64{}
	scRows, err := q.QueryContext(ctx, `SELECT session_key, value FROM stats_events INDEXED BY idx_stats_scroll_cov
		WHERE kind = 'scroll' AND local_date BETWEEN ? AND ?`, from, to)
	if err != nil {
		return nil, err
	}
	for scRows.Next() {
		var sk string
		var v int64
		if err := scRows.Scan(&sk, &v); err != nil {
			_ = scRows.Close()
			return nil, err
		}
		if v > sessScroll[sk] {
			sessScroll[sk] = v
		}
	}
	if err := scRows.Close(); err != nil {
		return nil, err
	}
	return sessScroll, nil
}

// statsReadTime reads the read time (by date, hour and feed, and by session) and the scroll depth
// per session for a local date range. Both are streamed from covering indexes (migration 0009).
func statsReadTime(ctx context.Context, q Querier, from, to string) (rt map[statsRTKey]int64, sessRT map[string]int64, err error) {
	rtRows, err := q.QueryContext(ctx, `SELECT local_date, local_hour, feed_id, session_key, SUM(value)
		FROM stats_events INDEXED BY idx_stats_rt_cov
		WHERE kind = 'read_time' AND local_date BETWEEN ? AND ?
		GROUP BY local_date, local_hour, feed_id, session_key`, from, to)
	if err != nil {
		return nil, nil, err
	}
	rt, sessRT = map[statsRTKey]int64{}, map[string]int64{}
	for rtRows.Next() {
		var k statsRTKey
		var sk string
		var v int64
		if err := rtRows.Scan(&k.date, &k.hour, &k.feed, &sk, &v); err != nil {
			_ = rtRows.Close()
			return nil, nil, err
		}
		rt[k] += v
		sessRT[sk] += v
	}
	if err := rtRows.Close(); err != nil {
		return nil, nil, err
	}
	return rt, sessRT, nil
}

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

	first, err := StatsFirstEventDate(ctx, q)
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

	cut, err := statsLegacyCutoff(ctx, q)
	if err != nil {
		return nil, err
	}

	// The read-time scans and the all-time streaks run beside the opens on their own reader
	// connections (the pool has four; two extra is the most this endpoint takes).
	var (
		rt           map[statsRTKey]int64
		sessRT       map[string]int64
		sessScroll   map[string]int64
		streaks      StatsStreaks
		rtErr, stErr error
		wg           sync.WaitGroup
	)
	wg.Add(4)
	defer wg.Wait() // never leave a query running after an early return
	go func() {
		defer wg.Done()
		rt, sessRT, rtErr = statsReadTime(ctx, q, from, to)
	}()
	var scErr error
	go func() {
		defer wg.Done()
		sessScroll, scErr = statsScroll(ctx, q, from, to)
	}()
	var (
		stars map[int64]starAgg
		origs map[int64]int
		soErr error
	)
	go func() {
		defer wg.Done()
		stars, origs, soErr = statsStarsOrig(ctx, q, inc, loTS, hiTS, from, to)
	}()
	go func() {
		defer wg.Done()
		streaks, stErr = statsStreaks(ctx, q, cut, inc, today)
	}()

	// Opens in the range, from the covering index (migration 0009); no table rows are read.
	type openRow struct {
		id            int64
		date, session string
		hour          int
		item, feed    int64
		rt            int64
		legacy, read  bool
	}
	rows, err := q.QueryContext(ctx, `SELECT rowid, local_date, local_hour, feed_id, item_id, COALESCE(session_key, ''), ts
		FROM stats_events INDEXED BY idx_stats_open_cov
		WHERE kind = 'open' AND inferred <= ? AND local_date BETWEEN ? AND ?`, inc, from, to)
	if err != nil {
		return nil, err
	}
	var opens []openRow
	for rows.Next() {
		var o openRow
		var ts int64
		if err := rows.Scan(&o.id, &o.date, &o.hour, &o.feed, &o.item, &o.session, &ts); err != nil {
			_ = rows.Close()
			return nil, err
		}
		o.legacy = ts < cut
		opens = append(opens, o)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	wg.Wait()
	if rtErr != nil {
		return nil, rtErr
	}
	if stErr != nil {
		return nil, stErr
	}
	if scErr != nil {
		return nil, scErr
	}
	if soErr != nil {
		return nil, soErr
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
	for fRows.Next() {
		var f int64
		if err := fRows.Scan(&f); err != nil {
			_ = fRows.Close()
			return nil, err
		}
		live[f] = struct{}{}
	}
	if err := fRows.Close(); err != nil {
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
			TrackedOpens: a.nonLegacy, Bounces: a.bounces, ItemsOpened: len(a.openItems), ItemsOriginal: origs[id]}
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
	}
	// Names come from the newest snapshot row of each listed feed (one query for at most 300 ids).
	byRow := map[int64]int{}
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
		for idRows.Next() {
			var id int64
			var title string
			var folder sql.NullInt64
			var fname sql.NullString
			if err := idRows.Scan(&id, &title, &folder, &fname); err != nil {
				_ = idRows.Close()
				return nil, err
			}
			src := &out.Sources[byRow[id]]
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
		if err := idRows.Close(); err != nil {
			return nil, err
		}
	}
	for i := range out.Sources {
		if out.Sources[i].FeedTitle != "" {
			continue
		}
		// Only read time fell in the range (a session that spanned its start): the current name.
		var t string
		_ = q.QueryRowContext(ctx, "SELECT COALESCE("+feedTitleSQL("f")+", '') FROM feeds f WHERE f.id = ?", out.Sources[i].FeedID).Scan(&t)
		out.Sources[i].FeedTitle = t
	}

	// never opened: current, non-archived feeds with no open in the range
	nRows, err := q.QueryContext(ctx, `SELECT f.id, `+feedTitleSQL("f")+`, fo.name, f.created_at
		FROM feeds f LEFT JOIN folders fo ON fo.id = f.folder_id
		WHERE f.disabled_reason IS NOT 'archive' ORDER BY f.created_at, f.id`)
	if err != nil {
		return nil, err
	}
	for nRows.Next() {
		var id, created int64
		var title string
		var fname sql.NullString
		if err := nRows.Scan(&id, &title, &fname, &created); err != nil {
			_ = nRows.Close()
			return nil, err
		}
		if a := feeds[id]; a != nil && a.opens > 0 {
			continue
		}
		if len(out.NeverOpened) >= statsNeverMax {
			continue
		}
		n := StatsNeverOpened{FeedID: strconv.FormatInt(id, 10), Title: title, SubscribedOn: time.Unix(created, 0).In(loc).Format(dateLayout)}
		if fname.Valid {
			f := fname.String
			n.FolderName = &f
		}
		out.NeverOpened = append(out.NeverOpened, n)
	}
	if err := nRows.Close(); err != nil {
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
	for rows.Next() {
		var f int64
		var n int
		if err := rows.Scan(&f, &n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		m[f] = n
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return m, nil
}

// statsStreaks computes the all-time streaks over local dates with at least one read open. Each
// distinct open date is one probe that stops at its first qualifying open.
func statsStreaks(ctx context.Context, q Querier, cut int64, inc int, today string) (StatsStreaks, error) {
	var st StatsStreaks
	rows, err := q.QueryContext(ctx, `SELECT d.local_date FROM (
		SELECT local_date FROM stats_events INDEXED BY idx_stats_open_cov WHERE kind = 'open' AND inferred <= ?2 GROUP BY local_date) d
		WHERE EXISTS (SELECT 1 FROM stats_events e INDEXED BY idx_stats_open_cov
		 WHERE e.kind = 'open' AND e.inferred <= ?2 AND e.local_date = d.local_date AND (e.ts < ?1
		  OR EXISTS (SELECT 1 FROM stats_events r WHERE r.session_key = e.session_key AND r.kind = 'scroll' AND r.value >= ?3)
		  OR (SELECT SUM(r.value) FROM stats_events r WHERE r.session_key = e.session_key AND r.kind = 'read_time') >= ?4))
		ORDER BY d.local_date`, cut, inc, StatsReadScroll, StatsReadSeconds)
	if err != nil {
		return st, err
	}
	var dates []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			_ = rows.Close()
			return st, err
		}
		dates = append(dates, d)
	}
	if err := rows.Close(); err != nil {
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
