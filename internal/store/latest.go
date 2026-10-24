package store

import "context"

// LatestMaxPerFeed is the most cards LatestCards returns for one feed.
const LatestMaxPerFeed = 10

// FeedLatest is one feed's newest cards, newest first.
type FeedLatest struct {
	FeedID int64  `json:"feed_id,string"`
	Items  []Card `json:"items"`
}

// LatestCards returns, for every subscribed feed that has any, its newest n cards (sort_at DESC, id DESC) in one query,
// feeds in id order. Newest means newest of all the feed's articles, read or not; muted ones are left out (they live in
// the Muted view, as they do in All), and so are the archive feed and a feed being deleted.
//
// The picks are a correlated LIMIT per feed, not a ROW_NUMBER() window over all articles: a window has to number every
// article before it can keep the first n of each feed (about 40 times slower on 300 000 articles), while this walks
// idx_items_feed_sort from the newest end of each feed and stops after n.
func (d *DB) LatestCards(ctx context.Context, n int) ([]FeedLatest, error) {
	n = min(max(n, 1), LatestMaxPerFeed)
	rows, err := d.reader.QueryContext(ctx, "SELECT "+cardCols+` FROM feeds f
		CROSS JOIN items i ON i.id IN (SELECT x.id FROM items x WHERE x.feed_id = f.id AND x.muted_by IS NULL ORDER BY x.sort_at DESC, x.id DESC LIMIT ?)
		LEFT JOIN item_content c ON c.item_id = i.id
		WHERE f.disabled_reason IS NOT 'archive' AND NOT `+deletingFeedSQL("f")+`
		ORDER BY f.id, i.sort_at DESC, i.id DESC`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeedLatest{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].FeedID != c.FeedID {
			out = append(out, FeedLatest{FeedID: c.FeedID, Items: []Card{}})
		}
		last := &out[len(out)-1]
		last.Items = append(last.Items, c)
	}
	return out, rows.Err()
}
