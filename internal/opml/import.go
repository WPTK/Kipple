package opml

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

// ImportOptions tunes Import.
type ImportOptions struct {
	// MarkReadOlderThanDays (1-365, 0 = off) sets initial_read_before = now - N*86400
	// on every new feed.
	MarkReadOlderThanDays int
	// MoveExisting moves each feed that already exists into the folder the file puts it in
	// (POST /api/opml ?move_existing=true). Off, an existing feed stays where it is.
	MoveExisting bool
}

// Existing is a feed that already existed (left in its folder unless MoveExisting).
type Existing struct {
	URL    string `json:"url"`
	FeedID int64  `json:"feed_id"`
}

// Dropped is a feed listed in several folders: the first is kept. Folders are paths.
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
	FoldersCreated    int          `json:"folders_created"`
	FeedsAdded        int          `json:"feeds_added"`
	FeedsExisting     []Existing   `json:"feeds_existing"`
	FoldersMergedCase []MergedCase `json:"folders_merged_case"`
	// FoldersRefused are folders not created (too deep, a name the folder writer refuses); their
	// feeds went into the deepest ancestor that was kept, or Uncategorized.
	FoldersRefused []RefusedFolder `json:"folders_refused"`
	// FoldersMergedPath are folders of the file filed into an existing folder with the same full
	// path but other levels (full paths are unique), each side as its chain of names.
	FoldersMergedPath []MergedPath `json:"folders_merged_path"`
	// FeedsMoved are the existing feeds MoveExisting moved to another folder.
	FeedsMoved []Existing `json:"feeds_moved"`
	// FoldersEmptied are folders (by path) that MoveExisting left with no feed and no subfolder.
	// They are not deleted: deleting one would also delete its filters.
	FoldersEmptied     []string  `json:"folders_emptied"`
	MembershipsDropped []Dropped `json:"memberships_dropped"`
	Skipped            []Skipped `json:"skipped"`
	InvalidAttrs       []string  `json:"invalid_attrs"`
	// IgnoredAttrs are valid but security-sensitive kipple:* attributes that an
	// import never applies (allow_private_net, allow_insecure_tls), and feeds
	// on a literal private address, imported with allow_private_net off.
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
		FoldersRefused:     append([]RefusedFolder{}, doc.FoldersRefused...),
		FoldersMergedPath:  []MergedPath{},
		FeedsMoved:         []Existing{},
		FoldersEmptied:     []string{},
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
		var nextFeedPos int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position)+1,0) FROM feeds").Scan(&nextFeedPos); err != nil {
			return err
		}

		// Folders: each chain is matched level by level (ignoring ASCII case) or created by the folder
		// writer, which walks down from the nearest level already resolved (never from the top again)
		// on indexed lookups. A chain the writer refuses (a bad name, Uncategorized as a parent)
		// resolves to its deepest kept ancestor and is reported once, at its top. A chain that lands on
		// a folder with the same full path but other levels (Music > AC > DC onto a literal "AC/DC") is
		// reported in folders_merged_path. A chain a hand-built Doc forgot to list is resolved on demand.
		folderID := map[string]int64{} // chainKey -> folder id; the empty chain is the top level (0)
		refused := map[string]bool{}   // chains that fell back to an ancestor
		var resolve func(chain []string) (int64, error)
		resolve = func(chain []string) (int64, error) {
			if len(chain) == 0 {
				return 0, nil
			}
			k := chainKey(chain)
			if id, ok := folderID[k]; ok {
				return id, nil
			}
			base, from := int64(0), 0
			for i := len(chain) - 1; i > 0; i-- {
				ak := chainKey(chain[:i])
				if id, ok := folderID[ak]; ok {
					if refused[ak] {
						folderID[k], refused[k] = id, true
						return id, nil
					}
					base, from = id, i
					break
				}
			}
			id, created, werr := store.EnsureFolderChainFrom(ctx, tx, base, chain[from:])
			res.FoldersCreated += created
			if store.FolderRefused(werr) {
				parent, err := resolve(chain[:len(chain)-1])
				if err != nil {
					return 0, err
				}
				if !refused[chainKey(chain[:len(chain)-1])] {
					res.FoldersRefused = append(res.FoldersRefused, RefusedFolder{Path(chain), refusal(werr), chain})
				}
				folderID[k], refused[k] = parent, true
				return parent, nil
			}
			if werr != nil {
				return 0, fmt.Errorf("opml: create folder %q: %w", Path(chain), werr)
			}
			folderID[k] = id
			names, err := store.FolderNames(ctx, tx, id)
			if err != nil {
				return 0, err
			}
			if foldCase(chainKey(names)) != foldCase(chainKey(chain)) {
				res.FoldersMergedPath = append(res.FoldersMergedPath, MergedPath{names, chain})
			}
			return id, nil
		}
		// A folder with subfolders in the file is resolved through them, top-down, so the writer can
		// match a whole run of levels to an existing folder (and leave no empty "AC" beside a literal
		// "AC/DC"); it is still created, in document order, by the first of them.
		hasKids := map[string]bool{}
		for _, chain := range doc.Folders {
			if len(chain) > 1 {
				hasKids[chainKey(chain[:len(chain)-1])] = true
			}
		}
		for _, chain := range doc.Folders {
			if hasKids[chainKey(chain)] {
				continue
			}
			if _, err := resolve(chain); err != nil {
				return err
			}
		}

		type firstSeen struct {
			url     string
			folder  []string
			dropped []string
		}
		var emptiedFrom []int64 // folders MoveExisting took a feed out of, first time first
		seen := map[string]*firstSeen{}
		var order []*firstSeen
		for _, f := range doc.Feeds {
			// The URL syntax check every other way a feed enters the database makes
			// (userinfo refused). A literal private address is still imported: the
			// feed gets allow_private_net off like every imported feed, so the
			// dial-time guard blocks it until the user turns the exception on
			// (a self-export or migration must not silently drop LAN feeds).
			norm, key, host, err := store.ValidateFeedURL(f.URL, true)
			if err != nil {
				res.Skipped = append(res.Skipped, Skipped{f.URL, err.Error()})
				continue
			}
			privateAddr := false
			if ip, perr := netip.ParseAddr(host); perr == nil && fetch.Blocked(ip.Unmap()) {
				privateAddr = true
			}
			if p, ok := seen[key]; ok {
				if chainKey(f.Folder) != chainKey(p.folder) {
					p.dropped = append(p.dropped, Path(f.Folder))
				}
				continue
			}
			p := &firstSeen{url: f.URL, folder: f.Folder}
			seen[key] = p
			order = append(order, p)

			target, err := resolve(f.Folder)
			if err != nil {
				return err
			}
			if target == 0 {
				target = 1 // the default folder
			}
			if id, found, err := store.FindFeedByURL(ctx, tx, f.URL); err != nil {
				return err
			} else if found {
				res.FeedsExisting = append(res.FeedsExisting, Existing{norm, id})
				// A feed whose folder in the file was not kept stays where it is: moving it to an
				// ancestor (or Uncategorized) is not what the file says.
				if opts.MoveExisting && !f.cut && !refused[chainKey(f.Folder)] {
					var from int64
					err := tx.QueryRowContext(ctx, "SELECT f.folder_id FROM feeds f WHERE f.id = ? AND "+store.ListedFeedSQL("f"), id).Scan(&from)
					if errors.Is(err, sql.ErrNoRows) || err == nil && from == target {
						continue
					}
					if err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, "UPDATE feeds SET folder_id = ?, updated_at = unixepoch() WHERE id = ?", target, id); err != nil {
						return fmt.Errorf("opml: move %s: %w", norm, err)
					}
					res.FeedsMoved = append(res.FeedsMoved, Existing{norm, id})
					if from != 1 && !slices.Contains(emptiedFrom, from) {
						emptiedFrom = append(emptiedFrom, from)
					}
				}
				continue
			}
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
			if privateAddr {
				res.IgnoredAttrs = append(res.IgnoredAttrs, norm+": private address, imported with allow_private_net off; turn it on for this feed to fetch it")
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
				target, norm, key, host, nullStr(f.Title), httpURLOrEmpty(f.SiteURL), nextFeedPos, enabled, reason,
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
		for _, id := range emptiedFrom {
			var empty bool
			if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM feeds f WHERE f.folder_id = ?1 AND `+store.ListedFeedSQL("f")+`)
				AND NOT EXISTS (SELECT 1 FROM folders c WHERE c.parent_id = ?1)`, id).Scan(&empty); err != nil {
				return err
			}
			if !empty {
				continue
			}
			names, err := store.FolderNames(ctx, tx, id)
			if err != nil {
				return err
			}
			res.FoldersEmptied = append(res.FoldersEmptied, Path(names))
		}
		for _, p := range order {
			if len(p.dropped) > 0 {
				res.MembershipsDropped = append(res.MembershipsDropped, Dropped{p.url, Path(p.folder), p.dropped})
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// chainKey is an unambiguous map key for a folder chain (a name may hold any character).
func chainKey(chain []string) string {
	var b strings.Builder
	for _, s := range chain {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	return b.String()
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
