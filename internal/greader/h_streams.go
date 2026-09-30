package greader

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/WPTK/kipple/internal/store"
)

func (a *API) registerStreams() {
	a.routes["stream/items/ids"] = route{h: (*call).streamItemIDs}
	a.routes["stream/items/contents"] = route{h: (*call).streamItemContents}
	a.routes["stream/contents"] = route{h: (*call).streamContents, prefix: true}
}

const (
	// contentCap is the per-item content limit (design §6.6), cut UTF-8-safely.
	contentCap = 500_000
	// maxContentIDs is the number of i= ids honoured per contents request.
	maxContentIDs = 1000
)

func (c *call) jsonStart() *bufio.Writer {
	c.w.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.w.WriteHeader(http.StatusOK)
	return bufio.NewWriterSize(c.w, 32<<10)
}

// streamItemIDs is GET stream/items/ids: itemRefs as decimal id strings and a
// string continuation, omitted on the last page. Rows are written as they are read.
func (c *call) streamItemIDs() {
	f, err := c.resolveStream(c.p.Get("s"), firstOrEmpty(c.p.AllRaw("s")))
	if err != nil {
		c.serverError("resolve stream", err)
		return
	}
	f = applyStates(f, c.p.All("it"), c.p.All("xt"))
	page := c.pageParams(maxIDsN)
	f.HoldCut = c.a.holdCut()

	var bw *bufio.Writer
	first := true
	last, more, err := c.a.db.StreamIDs(c.r.Context(), f, page, func(id int64) error {
		if bw == nil {
			bw = c.jsonStart()
			_, _ = bw.WriteString(`{"itemRefs":[`)
		}
		if !first {
			_ = bw.WriteByte(',')
		}
		first = false
		_, _ = bw.WriteString(`{"id":"`)
		_, _ = bw.WriteString(strconv.FormatInt(id, 10))
		_, err := bw.WriteString(`"}`)
		return err
	})
	if err != nil {
		if bw == nil {
			c.serverError("stream ids", err)
			return
		}
		c.a.log.Error("greader: stream ids aborted", "err", err) // truncated response; the client retries
		return
	}
	if bw == nil {
		bw = c.jsonStart()
		_, _ = bw.WriteString(`{"itemRefs":[`)
	}
	_, _ = bw.WriteString(`]`)
	if more {
		_, _ = bw.WriteString(`,"continuation":"` + strconv.FormatInt(last, 10) + `"`)
	}
	_, _ = bw.WriteString(`}`)
	_ = bw.Flush()
}

// streamItemContents is POST stream/items/contents (GET tolerated).
func (c *call) streamItemContents() {
	raw := c.p.All("i")
	ids := make([]int64, 0, len(raw))
	for _, v := range raw {
		if id, ok := ParseItemID(v); ok {
			ids = append(ids, id)
		} else if strings.TrimSpace(v) != "" {
			c.a.log.Debug("greader: skipping unparseable item id", "value", v)
		}
	}
	if len(ids) > maxContentIDs {
		c.a.log.Warn("greader: contents id list truncated", "ids", len(ids), "kept", maxContentIDs, "ua", c.r.UserAgent())
		ids = ids[:maxContentIDs]
	}
	c.warnNoIDs(len(ids))
	c.writeContents(stateReadingList, "", ids, c.p.Get("r") == "o")
}

// streamContents is GET stream/contents[/<stream>] for FeedMe/Readrops-style
// clients: the ids filter with n capped at 1000, then the contents.
func (c *call) streamContents() {
	sid := c.p.Get("s")
	rawSID := firstOrEmpty(c.p.AllRaw("s"))
	if rest := strings.TrimPrefix(c.name, "stream/contents"); rest != "" {
		if path := strings.TrimPrefix(rest, "/"); path != "" && sid == "" {
			sid, rawSID = path, path
		}
	}
	f, err := c.resolveStream(sid, rawSID)
	if err != nil {
		c.serverError("resolve stream", err)
		return
	}
	f = applyStates(f, c.p.All("it"), c.p.All("xt"))
	page := c.pageParams(maxContentsN)
	f.HoldCut = c.a.holdCut()
	var ids []int64
	last, more, err := c.a.db.StreamIDs(c.r.Context(), f, page, func(id int64) error {
		ids = append(ids, id)
		return nil
	})
	if err != nil {
		c.serverError("stream contents", err)
		return
	}
	cont := ""
	if more {
		cont = strconv.FormatInt(last, 10)
	}
	if strings.TrimSpace(sid) == "" {
		sid = stateReadingList
	}
	c.writeContents(sid, cont, ids, page.Asc)
}

// writeContents streams the envelope and one item at a time (the full item
// slice is never built).
func (c *call) writeContents(streamID, continuation string, ids []int64, asc bool) {
	var bw *bufio.Writer
	begin := func() {
		bw = c.jsonStart()
		_, _ = bw.WriteString(`{"id":`)
		_, _ = bw.Write(marshal(streamID))
		_, _ = bw.WriteString(`,"updated":` + strconv.FormatInt(c.a.now().Unix(), 10))
		if continuation != "" {
			_, _ = bw.WriteString(`,"continuation":"` + continuation + `"`)
		}
		_, _ = bw.WriteString(`,"items":[`)
	}
	first := true
	err := c.a.db.StreamItems(c.r.Context(), ids, asc, c.a.holdCut(), func(r *store.ContentRow) error {
		if bw == nil {
			begin()
		}
		if !first {
			_ = bw.WriteByte(',')
		}
		first = false
		_, err := bw.Write(marshal(newItemJSON(r)))
		return err
	})
	if err != nil {
		if bw == nil {
			c.serverError("stream contents", err)
			return
		}
		c.a.log.LogAttrs(c.r.Context(), slog.LevelError, "greader: contents aborted", slog.Any("err", err))
		return
	}
	if bw == nil {
		begin()
	}
	_, _ = bw.WriteString(`]}`)
	_ = bw.Flush()
}

type hrefJSON struct {
	Href string `json:"href"`
}

type altJSON struct {
	Href string `json:"href"`
	Type string `json:"type"`
}

type summaryJSON struct {
	Direction string `json:"direction"`
	Content   string `json:"content"`
}

type originJSON struct {
	StreamID string `json:"streamId"`
	Title    string `json:"title"`
	HTMLURL  string `json:"htmlUrl"`
}

type enclosureJSON struct {
	Href   string `json:"href"`
	URL    string `json:"url"`
	Type   string `json:"type"`
	Length int64  `json:"length"`
}

// itemJSON is one stream item (design §6.6). Strings are never null; summary,
// categories and origin are always present (NetNewsWire's decoder needs them).
type itemJSON struct {
	ID            string          `json:"id"`
	CrawlTimeMsec string          `json:"crawlTimeMsec"`
	TimestampUsec string          `json:"timestampUsec"`
	Published     int64           `json:"published"`
	Updated       int64           `json:"updated"`
	Title         string          `json:"title"`
	Author        string          `json:"author"`
	Canonical     []hrefJSON      `json:"canonical"`
	Alternate     []altJSON       `json:"alternate"`
	Summary       summaryJSON     `json:"summary"`
	Categories    []string        `json:"categories"`
	Origin        originJSON      `json:"origin"`
	Enclosure     []enclosureJSON `json:"enclosure,omitempty"`
}

func newItemJSON(r *store.ContentRow) itemJSON {
	content := r.HTML
	if r.UseFulltext && r.FulltextHTML.Valid {
		content = r.FulltextHTML.String
	}
	updated := r.Published
	if r.Updated.Valid {
		updated = r.Updated.Int64
	}
	cats := []string{stateReadingList}
	if r.Folder != "" { // an archived item is in no folder (its feed is not a subscription)
		cats = append(cats, labelPrefix+r.Folder)
	}
	if r.Read {
		cats = append(cats, stateRead)
	}
	if r.Starred {
		cats = append(cats, stateStarred)
	}
	title := r.OriginTitle
	if title == "" {
		title = r.FeedTitle
	}
	return itemJSON{
		ID:            FormatLongID(r.ID),
		CrawlTimeMsec: strconv.FormatInt(r.ID/1000, 10),
		TimestampUsec: strconv.FormatInt(r.ID, 10),
		Published:     r.Published,
		Updated:       updated,
		Title:         r.Title,
		Author:        r.Author,
		Canonical:     []hrefJSON{{Href: r.URL}},
		Alternate:     []altJSON{{Href: r.URL, Type: "text/html"}},
		Summary:       summaryJSON{Direction: "ltr", Content: truncateUTF8(content, contentCap)},
		Categories:    cats,
		Origin:        originJSON{StreamID: feedID(r.FeedID), Title: title, HTMLURL: r.SiteURL},
		Enclosure:     parseEnclosures(r.Enclosures),
	}
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// parseEnclosures converts item_content.enclosures_json ([{url,type,length}],
// length a string) to the Reader shape; nil when there are none.
func parseEnclosures(raw string) []enclosureJSON {
	if raw == "" {
		return nil
	}
	var in []struct {
		URL    string `json:"url"`
		Type   string `json:"type"`
		Length string `json:"length"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil || len(in) == 0 {
		return nil
	}
	out := make([]enclosureJSON, 0, len(in))
	for _, e := range in {
		n, _ := strconv.ParseInt(e.Length, 10, 64)
		out = append(out, enclosureJSON{Href: e.URL, URL: e.URL, Type: e.Type, Length: n})
	}
	return out
}
