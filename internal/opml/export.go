package opml

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/WPTK/kipple/internal/store"
)

// Export writes the subscription list as OPML 2.0. The document is built fully
// in memory before the first write to w (design §8).
func Export(ctx context.Context, db *store.DB, w io.Writer) error {
	return ExportFrom(ctx, db.Reader(), w)
}

// Queryer is what ExportFrom reads through: a *sql.DB or *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ExportFrom is Export against any database with Kipple's schema, such as a
// backup snapshot file, so the OPML in a backup matches its database exactly.
// A feed marked for deletion (its URL replaced by a placeholder) is left out.
// The folder tree is written as nested outlines, one per folder named by its own
// name, siblings in folder order; inside a folder its subfolders come first, then its
// own feeds, the order the web app shows. One query reads it all, so the document is one snapshot.
func ExportFrom(ctx context.Context, q Queryer, w io.Writer) error {
	rows, err := q.QueryContext(ctx, `
		SELECT fo.id, COALESCE(fo.parent_id, 0), fo.name, f.url, f.site_url, COALESCE(f.custom_title, NULLIF(f.title,''), ''),
		       f.interval_minutes, f.retention, f.fulltext, f.dedup_mode, f.user_agent,
		       f.ignore_http_cache, f.disable_http2, f.allow_insecure_tls, f.allow_private_net, f.enabled
		FROM folders fo JOIN folder_paths fp ON fp.id = fo.id LEFT JOIN feeds f ON f.folder_id = fo.id AND `+store.ListedFeedSQL("f")+`
		WHERE NOT (fo.is_default = 1 AND f.id IS NULL)
		ORDER BY fp.sort_key, f.position, f.id`)
	if err != nil {
		return fmt.Errorf("opml: export: %w", err)
	}
	defer rows.Close()

	// The query lists folders in pre-order, each folder's own feeds beside it. The tree is built first
	// because a folder's subfolders are written before its own feeds (the order the web app shows).
	type feed struct {
		url, site, title, dedup, ua            sql.NullString
		interval, retention                    sql.NullInt64
		ft, nocache, h2, insecure, private, on sql.NullInt64
	}
	type folderNode struct {
		name  string
		feeds []feed
		kids  []*folderNode
	}
	root := &folderNode{}
	nodes := map[int64]*folderNode{0: root}
	var cur *folderNode
	var curID int64 = -1
	for rows.Next() {
		var fid, parent int64
		var folder string
		var f feed
		if err := rows.Scan(&fid, &parent, &folder, &f.url, &f.site, &f.title, &f.interval, &f.retention, &f.ft, &f.dedup, &f.ua,
			&f.nocache, &f.h2, &f.insecure, &f.private, &f.on); err != nil {
			return err
		}
		if fid != curID {
			if nodes[fid] != nil {
				return fmt.Errorf("opml: export: folder %d listed twice", fid)
			}
			up := nodes[parent]
			if up == nil {
				return fmt.Errorf("opml: export: folder %d listed outside its parent %d", fid, parent)
			}
			cur, curID = &folderNode{name: folder}, fid
			nodes[fid] = cur
			up.kids = append(up.kids, cur)
		}
		if f.url.Valid {
			cur.feeds = append(cur.feeds, f)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<opml version="2.0" xmlns:kipple="` + NS + `">` + "\n  <head><title>Kipple subscriptions</title></head>\n  <body>\n")
	var write func(n *folderNode, depth int)
	write = func(n *folderNode, depth int) {
		b.WriteString(strings.Repeat("  ", depth+1))
		b.WriteString("<outline")
		attr(&b, "text", n.name)
		attr(&b, "title", n.name)
		b.WriteString(">\n")
		for _, k := range n.kids {
			write(k, depth+1)
		}
		for _, f := range n.feeds {
			b.WriteString(strings.Repeat("  ", depth+2))
			b.WriteString(`<outline type="rss"`)
			attr(&b, "text", f.title.String)
			attr(&b, "title", f.title.String)
			attr(&b, "xmlUrl", f.url.String)
			if f.site.String != "" {
				attr(&b, "htmlUrl", f.site.String)
			}
			if f.interval.Valid {
				attr(&b, "kipple:interval", strconv.FormatInt(f.interval.Int64, 10))
			}
			if f.retention.Valid {
				attr(&b, "kipple:retention", strconv.FormatInt(f.retention.Int64, 10))
			}
			if f.ft.Int64 == 1 {
				attr(&b, "kipple:fulltext", "1")
			}
			if f.dedup.String != "auto" {
				attr(&b, "kipple:dedup", f.dedup.String)
			}
			if f.ua.Valid && f.ua.String != "" {
				attr(&b, "kipple:user_agent", f.ua.String)
			}
			for _, o := range []struct {
				n string
				v sql.NullInt64
			}{{"ignore_http_cache", f.nocache}, {"disable_http2", f.h2}, {"allow_insecure_tls", f.insecure}, {"allow_private_net", f.private}} {
				if o.v.Int64 == 1 {
					attr(&b, "kipple:"+o.n, "1")
				}
			}
			if f.on.Int64 == 0 {
				attr(&b, "kipple:enabled", "0")
			}
			b.WriteString("/>\n")
		}
		b.WriteString(strings.Repeat("  ", depth+1))
		b.WriteString("</outline>\n")
	}
	for _, n := range root.kids {
		write(n, 1)
	}
	b.WriteString("  </body>\n</opml>\n")
	_, err = w.Write(b.Bytes())
	return err
}

func attr(b *bytes.Buffer, name, val string) {
	b.WriteByte(' ')
	b.WriteString(name)
	b.WriteString(`="`)
	_ = xml.EscapeText(b, []byte(val))
	b.WriteByte('"')
}
