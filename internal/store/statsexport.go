package store

import (
	"context"
	"database/sql"
)

// StatsExportPageSize is the rows one export page reads; StatsDeleteBatch is the id window one
// delete transaction covers (so at most that many rows).
const (
	StatsExportPageSize = 5000
	StatsDeleteBatch    = 10000
)

// StatsExportRow is one stats_events row as exported (design section 8). event_id is internal and
// not part of it.
type StatsExportRow struct {
	ID           int64
	TS           int64
	LocalDate    string
	LocalHour    int
	LocalWeekday int
	Kind         string
	Client       string
	Inferred     int
	ItemID       int64
	FeedID       int64
	FeedTitle    string
	FolderID     sql.NullInt64
	FolderName   sql.NullString
	ItemTitle    sql.NullString
	ItemURL      sql.NullString
	Value        sql.NullInt64
	SessionKey   sql.NullString
}

const (
	dateMin = "0000-01-01"
	dateMax = "9999-12-31"
)

func dateBounds(from, to string) (string, string) {
	if from == "" {
		from = dateMin
	}
	if to == "" {
		to = dateMax
	}
	return from, to
}

// StatsMaxID is the newest stats row id (0 when there are none): an export stops there, so rows
// recorded while it runs do not move its end.
func StatsMaxID(ctx context.Context, q Querier) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM stats_events").Scan(&n)
	return n, err
}

// StatsExportPage reads up to limit rows with after < id <= maxID whose local_date is in from..to
// (empty = unbounded), in id order. Rows are closed before it returns: the caller writes the page
// to the network with no read open.
func StatsExportPage(ctx context.Context, q Querier, from, to string, includeInferred bool, after, maxID int64, limit int) ([]StatsExportRow, error) {
	from, to = dateBounds(from, to)
	inc := 0
	if includeInferred {
		inc = 1
	}
	rows, err := q.QueryContext(ctx, `SELECT id, ts, local_date, local_hour, local_weekday, kind, client, inferred, item_id, feed_id,
		feed_title, folder_id, folder_name, item_title, item_url, value, session_key
		FROM stats_events WHERE id > ?1 AND id <= ?2 AND local_date BETWEEN ?3 AND ?4 AND inferred <= ?5
		ORDER BY id LIMIT ?6`, after, maxID, from, to, inc, limit)
	if err != nil {
		return nil, err
	}
	out := make([]StatsExportRow, 0, limit)
	err = eachRow(rows, func() error {
		var r StatsExportRow
		if err := rows.Scan(&r.ID, &r.TS, &r.LocalDate, &r.LocalHour, &r.LocalWeekday, &r.Kind, &r.Client, &r.Inferred,
			&r.ItemID, &r.FeedID, &r.FeedTitle, &r.FolderID, &r.FolderName, &r.ItemTitle, &r.ItemURL, &r.Value, &r.SessionKey); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

// sqlExportCount counts the rows an export writes, one index-only count per big kind from the
// covering indexes of migration 0009 (no table row is read for them) and the few star, unstar,
// open_original and share rows through idx_stats_kind_ts. Read time and scroll rows are never
// inferred, so their indexes carry no inferred column. TestStatsSummaryPlans checks each plan.
const sqlExportCount = `SELECT SUM(n) FROM (
	SELECT COUNT(*) AS n FROM stats_events INDEXED BY idx_stats_open_cov
		WHERE kind = 'open' AND rowid <= ?1 AND local_date BETWEEN ?2 AND ?3 AND inferred <= ?4
	UNION ALL SELECT COUNT(*) FROM stats_events INDEXED BY idx_stats_rt_cov
		WHERE kind = 'read_time' AND rowid <= ?1 AND local_date BETWEEN ?2 AND ?3
	UNION ALL SELECT COUNT(*) FROM stats_events INDEXED BY idx_stats_scroll_cov
		WHERE kind = 'scroll' AND rowid <= ?1 AND local_date BETWEEN ?2 AND ?3
	UNION ALL SELECT COUNT(*) FROM stats_events INDEXED BY idx_stats_kind_ts
		WHERE kind IN ('star', 'unstar', 'open_original', 'share') AND rowid <= ?1 AND local_date BETWEEN ?2 AND ?3 AND inferred <= ?4)`

// StatsCountUpTo counts the rows an export of this range would write: local_date in from..to
// (empty = unbounded), id <= maxID, and inferred rows only when includeInferred. It is a handful
// of index-only counts, not a walk of the wide rows.
func StatsCountUpTo(ctx context.Context, q Querier, from, to string, includeInferred bool, maxID int64) (int, error) {
	from, to = dateBounds(from, to)
	inc := 0
	if includeInferred {
		inc = 1
	}
	var n sql.NullInt64
	err := q.QueryRowContext(ctx, sqlExportCount, maxID, from, to, inc).Scan(&n)
	return int(n.Int64), err
}

// StatsCount counts the stats rows whose local_date is in from..to (empty = every row).
func StatsCount(ctx context.Context, q Querier, from, to string) (int, error) {
	from, to = dateBounds(from, to)
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM stats_events WHERE local_date BETWEEN ? AND ?`, from, to).Scan(&n)
	return n, err
}

// StatsDelete deletes the stats_events rows whose local_date is in from..to (empty = every row)
// and returns how many went. It touches no other table, but it records the gap it leaves (a
// window that removed rows moves SettingStatsGapEnd to the newest local_date it removed). It works in id windows of
// StatsDeleteBatch, one WithWrite transaction each, so the single writer is never held long and a
// window with no matches costs an index-bounded probe rather than a table scan. Rows recorded
// after it starts are not considered. On an error part of the range may already be gone; running
// it again finishes the job. A cancelled ctx stops it between windows.
//
// progress, when not nil, is called before each window (the API extends its write deadline with
// it). Before the first window's rows go, the same transaction stores the earliest timed event's
// ts (EnsureStatsTimedSince), so the summary's legacy cutoff survives the delete.
func StatsDelete(ctx context.Context, d *DB, from, to string, progress func()) (int, error) {
	from, to = dateBounds(from, to)
	maxID, err := StatsMaxID(ctx, d.Reader())
	if err != nil {
		return 0, err
	}
	total := 0
	var after int64
	for after < maxID {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var next sql.NullInt64
		if err := d.Reader().QueryRowContext(ctx, `SELECT MIN(id) FROM stats_events WHERE id > ?`, after).Scan(&next); err != nil {
			return total, err
		}
		if !next.Valid || next.Int64 > maxID {
			break
		}
		upper := next.Int64 + StatsDeleteBatch - 1
		if upper > maxID {
			upper = maxID
		}
		var n int64
		if progress != nil {
			progress()
		}
		err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if err := EnsureStatsTimedSince(ctx, tx, d.Clock().Now().Unix()); err != nil {
				return err
			}
			// The gap is exactly the days of the rows that go: through the newest of them.
			var newest sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT MAX(local_date) FROM stats_events WHERE id >= ?1 AND id <= ?2 AND local_date BETWEEN ?3 AND ?4`,
				next.Int64, upper, from, to).Scan(&newest); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `DELETE FROM stats_events WHERE id >= ?1 AND id <= ?2 AND local_date BETWEEN ?3 AND ?4`,
				next.Int64, upper, from, to)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			if err != nil || n == 0 || !newest.Valid {
				return err
			}
			return RecordStatsGap(ctx, tx, newest.String, d.Clock().Now())
		})
		if err != nil {
			return total, err
		}
		total += int(n)
		after = upper
	}
	return total, nil
}
