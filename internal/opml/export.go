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
// name, siblings in folder order; inside a folder its feeds come first, then its
// subfolders. One query reads it all, so the document is one snapshot.
func ExportFrom(ctx context.Context, q Queryer, w io.Writer) error {
	rows, err := q.QueryContext(ctx, `
		SELECT fo.id, COALESCE(fo.parent_id, 0), fo.name, f.url, f.site_url, COALESCE(`+store.FeedOwnTitleSQL("f")+`, ''),
		       f.interval_minutes, f.retention, f.fulltext, f.dedup_mode, f.user_agent,
		       f.ignore_http_cache, f.disable_http2, f.allow_insecure_tls, f.allow_private_net, f.enabled
		FROM folders fo JOIN folder_paths fp ON fp.id = fo.id LEFT JOIN feeds f ON f.folder_id = fo.id AND `+store.ListedFeedSQL("f")+`
		WHERE NOT (fo.is_default = 1 AND f.id IS NULL)
		ORDER BY fp.sort_key, f.position, f.id`)
	if err != nil {
		return fmt.Errorf("opml: export: %w", err)
	}
	defer rows.Close()

	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<opml version="2.0" xmlns:kipple="` + NS + `">` + "\n  <head><title>Kipple subscriptions</title></head>\n  <body>\n")
	indent := func(depth int) { b.WriteString(strings.Repeat("  ", depth+1)) }
	// open holds the ids of the open folder outlines, outermost first. Each folder closes the
	// outlines down to its parent, which must be open: rows in any other order are an error, never
	// a document with misnested or unclosed outlines.
	var open []int64
	opened := map[int64]bool{}
	closeTo := func(n int) {
		for len(open) > n {
			indent(len(open))
			b.WriteString("</outline>\n")
			open = open[:len(open)-1]
		}
	}
	var cur int64 = -1
	for rows.Next() {
		var fid, parent int64
		var folder string
		var url, site, title, dedup sql.NullString
		var interval, retention sql.NullInt64
		var ua sql.NullString
		var ft, nocache, h2, insecure, private, enabled sql.NullInt64
		if err := rows.Scan(&fid, &parent, &folder, &url, &site, &title, &interval, &retention, &ft, &dedup, &ua,
			&nocache, &h2, &insecure, &private, &enabled); err != nil {
			return err
		}
		if fid != cur {
			if opened[fid] {
				return fmt.Errorf("opml: export: folder %d listed twice", fid)
			}
			for len(open) > 0 && open[len(open)-1] != parent {
				closeTo(len(open) - 1)
			}
			if parent != 0 && len(open) == 0 {
				return fmt.Errorf("opml: export: folder %d listed outside its parent %d", fid, parent)
			}
			cur, opened[fid] = fid, true
			indent(len(open) + 1)
			b.WriteString("<outline")
			attr(&b, "text", folder)
			attr(&b, "title", folder)
			b.WriteString(">\n")
			open = append(open, fid)
		}
		depth := len(open)
		if !url.Valid {
			continue
		}
		indent(depth + 1)
		b.WriteString(`<outline type="rss"`)
		attr(&b, "text", title.String)
		attr(&b, "title", title.String)
		attr(&b, "xmlUrl", url.String)
		if site.String != "" {
			attr(&b, "htmlUrl", site.String)
		}
		if interval.Valid {
			attr(&b, "kipple:interval", strconv.FormatInt(interval.Int64, 10))
		}
		if retention.Valid {
			attr(&b, "kipple:retention", strconv.FormatInt(retention.Int64, 10))
		}
		if ft.Int64 == 1 {
			attr(&b, "kipple:fulltext", "1")
		}
		if dedup.String != "auto" {
			attr(&b, "kipple:dedup", dedup.String)
		}
		if ua.Valid && ua.String != "" {
			attr(&b, "kipple:user_agent", ua.String)
		}
		for _, o := range []struct {
			n string
			v sql.NullInt64
		}{{"ignore_http_cache", nocache}, {"disable_http2", h2}, {"allow_insecure_tls", insecure}, {"allow_private_net", private}} {
			if o.v.Int64 == 1 {
				attr(&b, "kipple:"+o.n, "1")
			}
		}
		if enabled.Int64 == 0 {
			attr(&b, "kipple:enabled", "0")
		}
		b.WriteString("/>\n")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	closeTo(0)
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
