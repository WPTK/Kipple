package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
)

// SettingCatchupSeen is the New page's mark: the item id up to which the account has seen what arrived. One value for
// the whole account (a string, like every item id the web app handles), moved only by AdvanceCatchupMark.
const SettingCatchupSeen = "catchup.seen"

// CatchupGroup is one section of the New page: a folder with its subfolders, or a single feed.
type CatchupGroup struct {
	FolderTreeID int64
	FeedID       int64
}

// CatchupMark is the stored mark, 0 before the page was ever left.
func (d *DB) CatchupMark(ctx context.Context) (int64, error) {
	return readCatchupMark(ctx, d.reader)
}

func readCatchupMark(ctx context.Context, q Querier) (int64, error) {
	var raw string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", SettingCatchupSeen).Scan(&raw)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var s string
	if json.Unmarshal([]byte(raw), &s) != nil {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, nil
	}
	return n, nil
}

// AdvanceCatchupMark moves the mark to `to` unless it is already at or past it, and returns the mark in force. It never
// moves back: two devices leaving the page in either order leave the later mark.
func (d *DB) AdvanceCatchupMark(ctx context.Context, to int64) (int64, error) {
	var mark int64
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		cur, err := readCatchupMark(ctx, tx)
		if err != nil {
			return err
		}
		mark = cur
		if to <= cur {
			return nil
		}
		mark = to
		return setSettingsTx(ctx, tx, map[string]any{SettingCatchupSeen: strconv.FormatInt(to, 10)})
	})
	return mark, err
}

// CatchupCounts is, for each group, how many unread articles arrived after `after` and up to `asOf` (item ids in
// (after, asOf]). A feed being deleted is not counted, as in every list; a group that no longer exists counts 0.
func (d *DB) CatchupCounts(ctx context.Context, after, asOf int64, groups []CatchupGroup) ([]int64, error) {
	out := make([]int64, len(groups))
	for i, g := range groups {
		where := []string{"read = 0", "id > ?", "id <= ?", notDeletingItemSQL}
		args := []any{after, asOf}
		switch {
		case g.FeedID != 0:
			where = append(where, "feed_id = ?")
			args = append(args, g.FeedID)
		case g.FolderTreeID != 0:
			where = append(where, inFolderTreeSQL("feed_id", "?"))
			args = append(args, g.FolderTreeID)
		}
		if err := d.reader.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE "+strings.Join(where, " AND "), args...).Scan(&out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
