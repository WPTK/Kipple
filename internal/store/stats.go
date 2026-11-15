package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// StatRow is one stats_events row (design §2.2, §8). The stats package builds
// it; this package owns the SQL.
type StatRow struct {
	TS           int64
	LocalDate    string
	LocalHour    int
	LocalWeekday int
	Kind         string
	Client       string
	Inferred     bool
	ItemID       int64
	FeedID       int64
	FeedTitle    string
	FolderID     sql.NullInt64
	FolderName   sql.NullString
	ItemTitle    string
	ItemURL      string
	Value        sql.NullInt64
	SessionKey   string // "" = NULL
	EventID      string // client event id; "" = NULL
}

// StatSnapshot is the identity snapshotted into a stats row.
type StatSnapshot struct {
	FeedID     int64
	FeedTitle  string
	FolderID   sql.NullInt64
	FolderName sql.NullString
	ItemTitle  string
	ItemURL    string
}

// statFeedTitle is the feed name a stats row snapshots: feedTitleSQL (custom title, else title,
// else URL), and an empty string when the feed row is gone (the LEFT JOINs below).
var statFeedTitle = "COALESCE(" + feedTitleSQL("f") + ", '')"

// statFolderPath is the folder path a stats row snapshots: the folder's names from the top level joined
// with "/", read by primary key per level (folderNames), not through the folder_paths view, which reads every
// folder. No folder, or one that is gone, is NULL.
func statFolderPath(ctx context.Context, q Querier, folder sql.NullInt64) (sql.NullString, error) {
	if !folder.Valid {
		return sql.NullString{}, nil
	}
	names, err := folderNames(ctx, q, folder.Int64, nil)
	if errors.Is(err, ErrFolderNotFound) {
		return sql.NullString{}, nil
	}
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: strings.Join(names, "/"), Valid: true}, nil
}

// StatItemSnapshot reads the identity of an item, or of a ledger id (title and
// URL come from its restore stub when one exists). ok is false when the id is
// in neither table.
func StatItemSnapshot(ctx context.Context, q Querier, itemID int64) (s StatSnapshot, ok bool, err error) {
	row := q.QueryRowContext(ctx, `SELECT x.feed_id, `+statFeedTitle+`, f.folder_id, x.title, x.url
		FROM items x LEFT JOIN feeds f ON f.id = x.feed_id
		WHERE x.id = ?`, itemID)
	err = row.Scan(&s.FeedID, &s.FeedTitle, &s.FolderID, &s.ItemTitle, &s.ItemURL)
	if errors.Is(err, sql.ErrNoRows) {
		row = q.QueryRowContext(ctx, `SELECT x.feed_id, `+statFeedTitle+`, f.folder_id,
			COALESCE(c.title, ''), COALESCE(c.url, '')
			FROM trimmed_items x LEFT JOIN trimmed_content c ON c.id = x.id
			LEFT JOIN feeds f ON f.id = x.feed_id
			WHERE x.id = ?`, itemID)
		err = row.Scan(&s.FeedID, &s.FeedTitle, &s.FolderID, &s.ItemTitle, &s.ItemURL)
		if errors.Is(err, sql.ErrNoRows) {
			return s, false, nil
		}
	}
	if err != nil {
		return s, false, err
	}
	s.FolderName, err = statFolderPath(ctx, q, s.FolderID)
	return s, err == nil, err
}

// InsertStat appends a stats row. A row whose event_id already exists is skipped (the unique
// index is the backstop for a race the caller's StatEventSeen check cannot see).
func InsertStat(ctx context.Context, q Querier, r StatRow) error {
	_, err := q.ExecContext(ctx, `INSERT INTO stats_events
		(ts, local_date, local_hour, local_weekday, kind, client, inferred, item_id, feed_id, feed_title,
		 folder_id, folder_name, item_title, item_url, value, session_key, event_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT DO NOTHING`,
		r.TS, r.LocalDate, r.LocalHour, r.LocalWeekday, r.Kind, r.Client, boolInt(r.Inferred), r.ItemID, r.FeedID, r.FeedTitle,
		r.FolderID, r.FolderName, nullStr(r.ItemTitle), nullStr(r.ItemURL), r.Value, nullStr(r.SessionKey), nullStr(r.EventID))
	if err != nil {
		return err
	}
	if r.Kind == "read_time" || r.Kind == "scroll" {
		// The first timed row ever recorded fixes the summary's legacy cutoff for good, in this same
		// transaction (a no-op once stored).
		return EnsureStatsTimedSince(ctx, q, r.TS)
	}
	return nil
}

// StatOpenTS returns the ts of the open event that issued sessionKey for
// itemID, when it is no older than since.
func StatOpenTS(ctx context.Context, q Querier, sessionKey string, itemID, since int64) (ts int64, ok bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT ts FROM stats_events WHERE session_key = ? AND kind = 'open' AND item_id = ? AND ts >= ?
		ORDER BY id LIMIT 1`, sessionKey, itemID, since).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return ts, err == nil, err
}

// StatReadTimeSum is the seconds of read_time already recorded for a session.
func StatReadTimeSum(ctx context.Context, q Querier, sessionKey string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(value), 0) FROM stats_events WHERE session_key = ? AND kind = 'read_time'`,
		sessionKey).Scan(&n)
	return n, err
}

// StatSetScroll keeps one scroll row per session: it raises the existing row
// when value is larger and reports whether a row already existed.
func StatSetScroll(ctx context.Context, q Querier, sessionKey string, value int64) (existed bool, err error) {
	var id int64
	err = q.QueryRowContext(ctx, `SELECT id FROM stats_events WHERE session_key = ? AND kind = 'scroll' ORDER BY id LIMIT 1`, sessionKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = q.ExecContext(ctx, `UPDATE stats_events SET value = ? WHERE id = ? AND value < ?`, value, id, value)
	return true, err
}

// StatItemFeeds groups the ids that are live items by feed, in one query. An id in no group is a ledger
// id or gone.
func StatItemFeeds(ctx context.Context, q Querier, ids []int64) (map[int64][]int64, error) {
	js, err := idsJSON(ids)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id, feed_id FROM items WHERE id IN (SELECT value FROM json_each(?))`, js)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byFeed := map[int64][]int64{}
	for rows.Next() {
		var id, feed int64
		if err := rows.Scan(&id, &feed); err != nil {
			return nil, err
		}
		byFeed[feed] = append(byFeed[feed], id)
	}
	return byFeed, rows.Err()
}

// InsertItemStats appends one stats row per live item in ids, in one statement. r carries what the rows
// share (time, kind, client and the feed's snapshot); each row takes its id, title and URL from items, an
// empty title or URL stored as NULL (as InsertStat's nullStr). The rows have no value, session key or
// event id, so the event id index cannot conflict.
func InsertItemStats(ctx context.Context, q Querier, r StatRow, ids []int64) error {
	js, err := idsJSON(ids)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO stats_events
		(ts, local_date, local_hour, local_weekday, kind, client, inferred, item_id, feed_id, feed_title,
		 folder_id, folder_name, item_title, item_url)
		SELECT ?, ?, ?, ?, ?, ?, ?, id, ?, ?, ?, ?, NULLIF(title, ''), NULLIF(url, '')
		FROM items WHERE id IN (SELECT value FROM json_each(?))`,
		r.TS, r.LocalDate, r.LocalHour, r.LocalWeekday, r.Kind, r.Client, boolInt(r.Inferred), r.FeedID, r.FeedTitle,
		r.FolderID, r.FolderName, js)
	return err
}

// StatFeedSnapshot reads the feed and folder identity for a stats row.
func StatFeedSnapshot(ctx context.Context, q Querier, feedID int64) (s StatSnapshot, err error) {
	s.FeedID = feedID
	if err = q.QueryRowContext(ctx, `SELECT `+statFeedTitle+`, f.folder_id FROM feeds f WHERE f.id = ?`, feedID).Scan(&s.FeedTitle, &s.FolderID); err != nil {
		return s, err
	}
	s.FolderName, err = statFolderPath(ctx, q, s.FolderID)
	return s, err
}

// StatsEnabled reads the stats.enabled setting (default true) through q. A failed read is an
// error, never a silent "on" or "off": the caller's transaction fails and nothing is recorded.
func StatsEnabled(ctx context.Context, q Querier) (bool, error) {
	return settingBoolErr(ctx, q, "stats.enabled", true)
}

// StatEventSeen reports whether a row with this client event_id already exists.
func StatEventSeen(ctx context.Context, q Querier, eventID string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM stats_events WHERE event_id = ?`, eventID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
