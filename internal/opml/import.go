package opml

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

// ImportOptions tunes Import.
type ImportOptions struct {
	// MarkReadOlderThanDays (1-365, 0 = off) sets initial_read_before = now - N*86400
	// on every new feed.
	MarkReadOlderThanDays int
}

// Existing is a feed that already existed and was left untouched.
type Existing struct {
	URL    string `json:"url"`
	FeedID int64  `json:"feed_id"`
}

// Dropped is a feed listed in several folders: the first is kept.
type Dropped struct {
	URL     string   `json:"url"`
	Kept    string   `json:"kept"`
	Dropped []string `json:"dropped"`
}

// Skipped is an outline that could not be imported.
type Skipped struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

// Result is the import report (POST /api/opml shape, design §7).
type Result struct {
	FoldersCreated     int          `json:"folders_created"`
	FeedsAdded         int          `json:"feeds_added"`
	FeedsExisting      []Existing   `json:"feeds_existing"`
	FoldersMergedCase  []MergedCase `json:"folders_merged_case"`
	MembershipsDropped []Dropped    `json:"memberships_dropped"`
	Skipped            []Skipped    `json:"skipped"`
	InvalidAttrs       []string     `json:"invalid_attrs"`
	// IgnoredAttrs are valid but security-sensitive kipple:* attributes that an
	// import never applies (allow_private_net, allow_insecure_tls).
	IgnoredAttrs []string `json:"ignored_attrs"`
	// NewFeedIDs are the inserted feeds in document order (for sched.StartImport).
	NewFeedIDs []int64 `json:"-"`
}

// Import writes doc in one transaction. New feeds get next_fetch_at = now, so a
// running scheduler picks them up as ordinary due feeds (design §4.9); callers
// that own a scheduler may additionally start an import run over NewFeedIDs.
func Import(ctx context.Context, db *store.DB, doc *Doc, opts ImportOptions) (Result, error) {
	res := Result{
		FeedsExisting:      []Existing{},
		FoldersMergedCase:  append([]MergedCase{}, doc.FoldersMergedCase...),
		MembershipsDropped: []Dropped{},
		Skipped:            []Skipped{},
		InvalidAttrs:       []string{},
		IgnoredAttrs:       []string{},
		NewFeedIDs:         []int64{},
	}
	now := db.Clock().Now().Unix()
	var readBefore any
	if opts.MarkReadOlderThanDays > 0 {
		readBefore = now - int64(opts.MarkReadOlderThanDays)*86400
	}

	err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var nextFolderPos, nextFeedPos int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position)+1,1) FROM folders").Scan(&nextFolderPos); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position)+1,0) FROM feeds").Scan(&nextFeedPos); err != nil {
			return err
		}

		// Folders: reuse a NOCASE match, else create in document order. A folder
		// that holds a feed but is missing from doc.Folders is created on demand.
		folderID := map[string]int64{"": 1}
		ensureFolder := func(name string) error {
			if _, ok := folderID[name]; ok {
				return nil
			}
			var id int64
			err := tx.QueryRowContext(ctx, "SELECT id FROM folders WHERE name = ? COLLATE NOCASE", name).Scan(&id)
			if err == sql.ErrNoRows {
				r, err := tx.ExecContext(ctx, "INSERT INTO folders (name, position) VALUES (?,?)", name, nextFolderPos)
				if err != nil {
					return fmt.Errorf("opml: create folder %q: %w", name, err)
				}
				nextFolderPos++
				res.FoldersCreated++
				if id, err = r.LastInsertId(); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			folderID[name] = id
			return nil
		}
		for _, name := range doc.Folders {
			if err := ensureFolder(name); err != nil {
				return err
			}
		}

		type firstSeen struct {
			url     string
			folder  string
			dropped []string
		}
		seen := map[string]*firstSeen{}
		var order []*firstSeen
		for _, f := range doc.Feeds {
			key, norm, err := feedurl.KeyAndNormalize(f.URL)
			if err != nil {
				res.Skipped = append(res.Skipped, Skipped{f.URL, "not a valid http(s) URL"})
				continue
			}
			if p, ok := seen[key]; ok {
				if f.Folder != p.folder {
					p.dropped = append(p.dropped, f.Folder)
				}
				continue
			}
			p := &firstSeen{url: f.URL, folder: f.Folder}
			seen[key] = p
			order = append(order, p)

			if id, found, err := store.FindFeedByURL(ctx, tx, f.URL); err != nil {
				return err
			} else if found {
				res.FeedsExisting = append(res.FeedsExisting, Existing{norm, id})
				continue
			}
			if err := ensureFolder(f.Folder); err != nil {
				return err
			}
			host, _ := feedurl.Host(norm)
			a := f.Attrs
			res.InvalidAttrs = append(res.InvalidAttrs, prefixAll(norm, f.BadAttrs)...)
			// An imported file must not weaken the SSRF/TLS guards of a feed.
			if a.AllowPrivateNet != nil {
				res.IgnoredAttrs = append(res.IgnoredAttrs, norm+": kipple:allow_private_net")
				a.AllowPrivateNet = nil
			}
			if a.AllowInsecureTLS != nil {
				res.IgnoredAttrs = append(res.IgnoredAttrs, norm+": kipple:allow_insecure_tls")
				a.AllowInsecureTLS = nil
			}
			enabled, reason := 1, any(nil)
			if a.Enabled != nil && !*a.Enabled {
				enabled, reason = 0, "user"
			}
			dedup := fetch.DedupAuto
			if a.Dedup != nil {
				dedup = *a.Dedup
			}
			r, err := tx.ExecContext(ctx, `INSERT INTO feeds
				(folder_id, url, url_key, host, custom_title, site_url, position, enabled, disabled_reason,
				 interval_minutes, retention, fulltext, dedup_mode, user_agent, ignore_http_cache,
				 disable_http2, allow_insecure_tls, allow_private_net, initial_read_before, next_fetch_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				folderID[f.Folder], norm, key, host, nullStr(f.Title), httpURLOrEmpty(f.SiteURL), nextFeedPos, enabled, reason,
				nullInt(a.Interval), nullInt(a.Retention), b2i(a.Fulltext), dedup, nullStrP(a.UserAgent),
				b2i(a.IgnoreHTTPCache), b2i(a.DisableHTTP2), b2i(a.AllowInsecureTLS), b2i(a.AllowPrivateNet),
				readBefore, now)
			if err != nil {
				return fmt.Errorf("opml: insert %s: %w", norm, err)
			}
			nextFeedPos++
			id, err := r.LastInsertId()
			if err != nil {
				return err
			}
			res.FeedsAdded++
			res.NewFeedIDs = append(res.NewFeedIDs, id)
		}
		for _, p := range order {
			if len(p.dropped) > 0 {
				res.MembershipsDropped = append(res.MembershipsDropped, Dropped{p.url, p.folder, p.dropped})
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// httpURLOrEmpty keeps only absolute http(s) URLs (htmlUrl is rendered as a link).
func httpURLOrEmpty(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

func prefixAll(u string, l []string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = u + ": " + s
	}
	return out
}

func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func nullStrP(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func b2i(p *bool) int {
	if p != nil && *p {
		return 1
	}
	return 0
}
