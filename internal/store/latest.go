package store

import "context"

// LatestMaxPerFeed is the most headlines LatestHeadlines returns for one feed.
const LatestMaxPerFeed = 10

// Headline is what the Feed index draws of an article: enough to show the title, say whether it is read and open it,
// and nothing that costs a join or a rewrite (no excerpt, no picture, no link).
type Headline struct {
	ID          int64  `json:"id,string"`
	FeedID      int64  `json:"feed_id,string"`
	Title       string `json:"title"`
	PublishedAt int64  `json:"published_at"`
	Read        bool   `json:"read"`
	Starred     bool   `json:"starred"`
}

// FeedLatest is one feed's newest headlines, newest first.
type FeedLatest struct {
	FeedID int64      `json:"feed_id,string"`
	Items  []Headline `json:"items"`
}

// LatestHeadlines returns, for every subscribed feed that has any, its newest n headlines (sort_at DESC, id DESC) in one
// query, feeds in id order. Newest means newest of all the feed's articles, read or not; muted ones are left out (they
// live in the Muted view, as they do in All), and so are the archive feed and a feed being deleted. A feed with no such
// article does not appear.
//
// The picks are a correlated LIMIT per feed, not a ROW_NUMBER() window over all articles: a window has to number every
// article before it can keep the first n of each feed (about 4 s against a few ms on 300 000 articles in 150 feeds), while this walks
// idx_items_feed_sort from the newest end of each feed and stops after n.
func (d *DB) LatestHeadlines(ctx context.Context, n int) ([]FeedLatest, error) {
	n = min(max(n, 1), LatestMaxPerFeed)
	rows, err := d.reader.QueryContext(ctx, `SELECT i.id, i.feed_id, i.title, i.published_at, i.read, i.starred FROM feeds f
		CROSS JOIN items i ON i.id IN (SELECT x.id FROM items x WHERE x.feed_id = f.id AND x.muted_by IS NULL ORDER BY x.sort_at DESC, x.id DESC LIMIT ?)
		WHERE f.disabled_reason IS NOT 'archive' AND NOT `+deletingFeedSQL("f")+`
		ORDER BY f.id, i.sort_at DESC, i.id DESC`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeedLatest{}
	for rows.Next() {
		var h Headline
		var read, starred int
		if err := rows.Scan(&h.ID, &h.FeedID, &h.Title, &h.PublishedAt, &read, &starred); err != nil {
			return nil, err
		}
		h.Read, h.Starred = read == 1, starred == 1
		if len(out) == 0 || out[len(out)-1].FeedID != h.FeedID {
			out = append(out, FeedLatest{FeedID: h.FeedID, Items: []Headline{}})
		}
		last := &out[len(out)-1]
		last.Items = append(last.Items, h)
	}
	return out, rows.Err()
}
