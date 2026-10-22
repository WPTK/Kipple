package store

import (
	"context"
	"time"
)

const (
	// paceWindowDays is how many complete local days the pace averages over, at most.
	paceWindowDays = 28
	// paceMinDays is the fewest counted days a feed needs for a pace: with fewer, a busy or quiet
	// stretch would pass for how the feed is.
	paceMinDays = 7
)

// FeedPaces is each listed feed's pace: its average new items per complete local day over the last
// paceWindowDays complete days, from feed_daily_new. A feed has no entry (no pace) when fewer than
// paceMinDays days were counted for it. Counted days run from the later of the day after the feed
// was subscribed (its first fetch brings a backlog that is not counted) and the day counting began
// (the first feed_daily_new row, as statsRateWindow has it) to yesterday in the account's time zone.
// A day without a row counts as zero: rows exist only for days with new items.
func (d *DB) FeedPaces(ctx context.Context, now time.Time) (map[int64]float64, error) {
	_, loc, _, _, err := StatsSettings(ctx, d.reader)
	if err != nil {
		return nil, err
	}
	var began string
	if err := d.reader.QueryRowContext(ctx, sqlDailyNewFirst).Scan(&began); err != nil || began == "" {
		return nil, err
	}
	end := parseLocalDate(now.In(loc).Format(dateLayout)).AddDate(0, 0, -1)
	windowStart := end.AddDate(0, 0, 1-paceWindowDays).Format(dateLayout)
	endS := end.Format(dateLayout)

	sums := map[int64]int64{}
	rows, err := d.reader.QueryContext(ctx,
		`SELECT feed_id, SUM(new_items) FROM feed_daily_new WHERE local_date BETWEEN ?1 AND ?2 GROUP BY feed_id`, windowStart, endS)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		sums[id] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	feeds, err := d.reader.QueryContext(ctx, `SELECT f.id, f.created_at FROM feeds f WHERE `+listedFeedSQL)
	if err != nil {
		return nil, err
	}
	defer feeds.Close()
	out := map[int64]float64{}
	for feeds.Next() {
		var id, created int64
		if err := feeds.Scan(&id, &created); err != nil {
			return nil, err
		}
		firstDay := parseLocalDate(time.Unix(created, 0).In(loc).Format(dateLayout)).AddDate(0, 0, 1).Format(dateLayout)
		start := max(firstDay, began, windowStart)
		if start > endS {
			continue
		}
		days := int(end.Sub(parseLocalDate(start)).Hours()/24) + 1
		if days < paceMinDays {
			continue
		}
		out[id] = float64(sums[id]) / float64(days)
	}
	return out, feeds.Err()
}
