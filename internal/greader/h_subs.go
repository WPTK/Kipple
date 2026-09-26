package greader

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/WPTK/kipple/internal/opml"
	"github.com/WPTK/kipple/internal/store"
)

const (
	stateReadingList = "user/-/state/com.google/reading-list"
	stateStarred     = "user/-/state/com.google/starred"
	stateRead        = "user/-/state/com.google/read"
	stateUnread      = "user/-/state/com.google/unread"
	stateKeptUnread  = "user/-/state/com.google/kept-unread"
	labelPrefix      = "user/-/label/"
)

func (a *API) registerSubs() {
	a.routes["subscription/list"] = route{h: (*call).subscriptionList}
	a.routes["tag/list"] = route{h: (*call).tagList}
	a.routes["subscription/quickadd"] = route{h: (*call).quickAdd, post: true}
	a.routes["subscription/edit"] = route{h: (*call).subscriptionEdit, post: true}
	a.routes["subscription/import"] = route{h: (*call).subscriptionImport, post: true, raw: true}
	a.routes["subscription/export"] = route{h: (*call).subscriptionExport}
	a.routes["rename-tag"] = route{h: (*call).renameTag, post: true}
	a.routes["disable-tag"] = route{h: (*call).disableTag, post: true, repair: true}
	a.routes["unread-count"] = route{h: (*call).unreadCount}
}

type categoryJSON struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type subscriptionJSON struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Categories []categoryJSON `json:"categories"`
	URL        string         `json:"url"`
	HTMLURL    string         `json:"htmlUrl"`
	IconURL    string         `json:"iconUrl"`
}

func feedID(id int64) string { return "feed/" + strconv.FormatInt(id, 10) }

// subscriptionList is GET subscription/list (design §6.9). ETag = hash of the body.
func (c *call) subscriptionList() {
	ctx := c.r.Context()
	subs, err := c.a.db.Subscriptions(ctx)
	if err != nil {
		c.serverError("subscription list", err)
		return
	}
	icons := c.a.db.BoolSetting(ctx, "greader.icon_urls", true) && c.a.opt.PublicURL != ""
	pub := strings.TrimRight(c.a.opt.PublicURL, "/")
	out := make([]subscriptionJSON, 0, len(subs))
	for _, s := range subs {
		j := subscriptionJSON{
			ID: feedID(s.ID), Title: s.Title, URL: s.URL, HTMLURL: s.SiteURL,
			Categories: []categoryJSON{{ID: labelPrefix + s.Folder, Label: s.Folder}},
		}
		if icons && s.IconHash != "" {
			j.IconURL = pub + apiPrefix + "/icon/" + strconv.FormatInt(s.ID, 10) + "-" + s.IconHash
		}
		out = append(out, j)
	}
	c.jsonETag(map[string]any{"subscriptions": out})
}

// tagList is GET tag/list.
func (c *call) tagList() {
	names, err := c.a.db.FolderNames(c.r.Context())
	if err != nil {
		c.serverError("tag list", err)
		return
	}
	tags := []map[string]string{{"id": stateStarred}, {"id": stateReadingList}}
	for _, n := range names {
		tags = append(tags, map[string]string{"id": labelPrefix + n, "type": "folder"})
	}
	c.jsonETag(map[string]any{"tags": tags})
}

// icon serves feed icons, unauthenticated, only while greader.icon_urls is on.
func (c *call) icon(rest string) {
	if !c.a.db.BoolSetting(c.r.Context(), "greader.icon_urls", true) {
		c.text(http.StatusNotFound, "Not Found")
		return
	}
	idStr, hash, ok := strings.Cut(rest, "-")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if !ok || err != nil || hash == "" {
		c.text(http.StatusNotFound, "Not Found")
		return
	}
	data, ctype, found, err := c.a.db.FeedIcon(c.r.Context(), id, hash)
	if err != nil {
		c.serverError("icon", err)
		return
	}
	if !found {
		c.text(http.StatusNotFound, "Not Found")
		return
	}
	if !safeIconType(ctype) {
		c.text(http.StatusNotFound, "Not Found")
		return
	}
	c.w.Header().Set("Content-Type", ctype)
	c.w.Header().Set("X-Content-Type-Options", "nosniff")
	c.w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	c.w.Header().Set("Cache-Control", "public, max-age=86400")
	c.w.WriteHeader(http.StatusOK)
	_, _ = c.w.Write(data)
}

// safeIconType allows only raster and icon image types (never svg or html).
func safeIconType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch strings.ToLower(mt) {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/avif", "image/x-icon", "image/vnd.microsoft.icon":
		return true
	}
	return false
}

// ---- labels ----

// parseUserPath returns the name after user/<x>/<suffix> (suffix such as
// "/label/" or "/state/com.google/"). An empty or all-space name is not ok.
func parseUserPath(id, suffix string) (string, bool) {
	rest, ok := strings.CutPrefix(id, "user/")
	if !ok {
		return "", false
	}
	_, name, ok := strings.Cut(rest, suffix)
	if !ok || strings.TrimSpace(name) == "" {
		return "", false
	}
	return name, true
}

// labelName returns the folder name after user/<x>/label/.
func labelName(id string) (string, bool) { return parseUserPath(id, "/label/") }

// labelCandidates are the lookup forms of §6.2: decoded, raw, raw through
// PathUnescape (which keeps '+'). Duplicates are dropped.
func labelCandidates(decoded, raw string) []string {
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		for _, o := range out {
			if o == s {
				return
			}
		}
		out = append(out, s)
	}
	if n, ok := labelName(decoded); ok {
		add(n)
	}
	if n, ok := labelName(raw); ok {
		add(n)
		if u, err := url.PathUnescape(n); err == nil {
			add(u)
		}
	}
	return out
}

// folderName resolves the a=/r= label values to the folder name to use: an
// existing folder matching any lookup form of §6.2 wins (so a raw '+' still finds
// "Politics+"), otherwise the decoded name.
func (c *call) folderName(ctx context.Context, values, raws []string) (string, bool, error) {
	for i, v := range values {
		n, ok := labelName(v)
		if !ok {
			continue
		}
		raw := ""
		if i < len(raws) {
			raw = raws[i]
		}
		for _, cand := range labelCandidates(v, raw) {
			if _, found, err := c.a.db.FindLabel(ctx, []string{cand}); err != nil {
				return "", false, err
			} else if found {
				return cand, true, nil
			}
		}
		return n, true, nil
	}
	return "", false, nil
}

// feedRefs turns s= values into FeedRefs: feed/<digits> or feed/<url>.
func feedRefs(values []string) []store.FeedRef {
	var out []store.FeedRef
	for _, v := range values {
		v = strings.TrimSpace(v)
		rest, ok := strings.CutPrefix(v, "feed/")
		if !ok || rest == "" {
			continue
		}
		if id, err := strconv.ParseInt(rest, 10, 64); err == nil && id > 0 {
			out = append(out, store.FeedRef{ID: id})
			continue
		}
		out = append(out, store.FeedRef{URL: rest})
	}
	return out
}

// ---- writes ----

type quickAddJSON struct {
	NumResults int    `json:"numResults"`
	Query      string `json:"query"`
	StreamID   string `json:"streamId"`
	StreamName string `json:"streamName"`
}

// quickAdd is POST subscription/quickadd.
func (c *call) quickAdd() {
	q := strings.TrimSpace(c.p.Get("quickadd"))
	res, err := c.a.db.Subscribe(c.r.Context(), store.SubscribeOpts{URL: q})
	var bad *store.InvalidURLError
	switch {
	case errors.As(err, &bad):
		c.json(http.StatusOK, map[string]any{"numResults": 0, "query": q, "error": bad.Reason})
		return
	case err != nil:
		c.serverError("quickadd", err)
		return
	}
	c.afterSubscribe(res)
	c.json(http.StatusOK, quickAddJSON{NumResults: 1, Query: q, StreamID: feedID(res.FeedID), StreamName: res.Title})
}

func (c *call) publishFolders() { c.a.publish("folder.changed", map[string]any{}) }

func (c *call) afterSubscribe(res store.SubscribeResult) {
	if !res.Existed {
		c.a.wake()
	}
	c.a.publish("feed.changed", map[string]any{"feed_id": strconv.FormatInt(res.FeedID, 10)})
}

// subscriptionEdit is POST subscription/edit (ac=subscribe|edit|unsubscribe).
func (c *call) subscriptionEdit() {
	ctx := c.r.Context()
	p := c.p
	ss, ts := p.All("s"), p.All("t")
	switch p.Get("ac") {
	case "subscribe":
		folder, _, err := c.folderName(ctx, p.All("a"), p.AllRaw("a"))
		if err != nil {
			c.serverError("subscribe", err)
			return
		}
		for i, s := range ss {
			rest, ok := strings.CutPrefix(strings.TrimSpace(s), "feed/")
			if !ok || rest == "" {
				continue
			}
			var title string
			if i < len(ts) {
				title = ts[i]
			}
			res, err := c.a.db.Subscribe(ctx, store.SubscribeOpts{URL: rest, Folder: folder, Title: title})
			var bad *store.InvalidURLError
			if errors.As(err, &bad) {
				continue
			}
			if err != nil {
				c.serverError("subscribe", err)
				return
			}
			c.afterSubscribe(res)
			if folder != "" {
				c.publishFolders() // may have created the folder
			}
		}
	case "edit":
		refs := feedRefs(ss)
		opts := store.EditOpts{}
		if len(ts) > 0 {
			opts.Title = ts[0]
		}
		if name, ok, err := c.folderName(ctx, p.All("a"), p.AllRaw("a")); err != nil {
			c.serverError("edit subscription", err)
			return
		} else if ok {
			opts.Folder, opts.SetFolder = name, true
		} else if _, ok := labelName(firstOrEmpty(p.All("r"))); ok {
			opts.MoveToDefault = true
		}
		// One title per feed when several are edited at once.
		if len(refs) > 1 && len(ts) == len(refs) {
			for i, ref := range refs {
				o := opts
				o.Title = ts[i]
				ids, err := c.a.db.EditSubscription(ctx, []store.FeedRef{ref}, o)
				if err != nil {
					c.serverError("edit subscription", err)
					return
				}
				c.publishFeeds(ids)
				if len(ids) > 0 && (o.SetFolder || o.MoveToDefault) {
					c.publishFolders()
				}
			}
			break
		}
		ids, err := c.a.db.EditSubscription(ctx, refs, opts)
		if err != nil {
			c.serverError("edit subscription", err)
			return
		}
		c.publishFeeds(ids)
		if len(ids) > 0 && (opts.SetFolder || opts.MoveToDefault) {
			c.publishFolders()
		}
	case "unsubscribe":
		ids, skipped, err := c.a.db.UnsubscribeSkipped(ctx, feedRefs(ss))
		if err != nil {
			c.serverError("unsubscribe", err)
			return
		}
		if len(skipped) > 0 {
			c.a.log.Info("greader: unsubscribe kept the archive feed: it still holds starred items", "feed_ids", skipped)
		}
		c.publishFeeds(ids)
	}
	c.ok()
}

func (c *call) publishFeeds(ids []int64) {
	for _, id := range ids {
		c.a.publish("feed.changed", map[string]any{"feed_id": strconv.FormatInt(id, 10)})
	}
}

// subscriptionImport is POST subscription/import: raw OPML, no T, exactly 200.
// Feeds are inserted due now and the scheduler is woken; nothing is fetched here.
func (c *call) subscriptionImport() {
	doc, err := opml.Parse(strings.NewReader(c.p.RawBody()))
	if err != nil {
		c.a.log.Warn("greader: subscription/import: bad OPML", "err", err, "ua", c.r.UserAgent())
		c.text(http.StatusBadRequest, "Bad OPML")
		return
	}
	res, err := opml.Import(c.r.Context(), c.a.db, doc, opml.ImportOptions{})
	if err != nil {
		c.serverError("import", err)
		return
	}
	if res.FeedsAdded > 0 {
		c.a.wake()
	}
	for _, id := range res.NewFeedIDs {
		c.a.publish("feed.changed", map[string]any{"feed_id": strconv.FormatInt(id, 10)})
	}
	if res.FoldersCreated > 0 {
		c.publishFolders()
	}
	c.ok()
}

// subscriptionExport is GET subscription/export: the same OPML as the UI.
func (c *call) subscriptionExport() {
	var buf bytes.Buffer
	if err := opml.Export(c.r.Context(), c.a.db, &buf); err != nil {
		c.serverError("export", err)
		return
	}
	h := c.w.Header()
	h.Set("Content-Type", "text/x-opml; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="kipple.opml"`)
	c.w.WriteHeader(http.StatusOK)
	_, _ = c.w.Write(buf.Bytes())
}

// renameTag is POST rename-tag (s=old label, dest=new label).
func (c *call) renameTag() {
	ctx := c.r.Context()
	s := c.p.Get("s")
	rawS := firstOrEmpty(c.p.AllRaw("s"))
	dest, ok, err := c.folderName(ctx, c.p.All("dest"), c.p.AllRaw("dest"))
	if err != nil {
		c.serverError("rename-tag", err)
		return
	}
	if !ok {
		c.ok()
		return
	}
	id, found, err := c.a.db.FindLabel(ctx, labelCandidates(s, rawS))
	if err != nil {
		c.serverError("rename-tag", err)
		return
	}
	if found {
		if err := c.a.db.RenameLabel(ctx, id, dest); err != nil {
			c.serverError("rename-tag", err)
			return
		}
		c.a.publish("feed.changed", map[string]any{})
		c.publishFolders()
	}
	c.ok()
}

func firstOrEmpty(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return l[0]
}

// disableTag is POST disable-tag: delete each folder, moving its feeds to Uncategorized.
func (c *call) disableTag() {
	ctx := c.r.Context()
	svals, raws := c.p.All("s"), c.p.AllRaw("s")
	for i, s := range svals {
		raw := ""
		if i < len(raws) {
			raw = raws[i]
		}
		cands := labelCandidates(s, raw)
		// A stray "&" after the id ("News&") is glued into the name; when no folder
		// carries that name, fall back to the name without the stray tail. Only
		// empty tails: "AT&T" never deletes "AT".
		if trimmed := strings.TrimRight(raw, "&"); trimmed != raw {
			cands = append(cands, labelCandidates(lenientUnescape(trimmed), trimmed)...)
		}
		id, found, err := c.a.db.FindLabel(ctx, cands)
		if err != nil {
			c.serverError("disable-tag", err)
			return
		}
		if found {
			if err := c.a.db.DisableLabel(ctx, id); err != nil {
				c.serverError("disable-tag", err)
				return
			}
			c.a.publish("feed.changed", map[string]any{})
			c.publishFolders()
		}
	}
	c.ok()
}

type unreadCountJSON struct {
	ID                      string `json:"id"`
	Count                   int64  `json:"count"`
	NewestItemTimestampUsec string `json:"newestItemTimestampUsec"`
}

// unreadCount is GET unread-count (FeedMe-class clients).
func (c *call) unreadCount() {
	rows, err := c.a.db.UnreadCounts(c.r.Context(), c.a.holdCut())
	if err != nil {
		c.serverError("unread-count", err)
		return
	}
	var total, totalMax int64
	perFolder := map[string]*unreadCountJSON{}
	var folderOrder []string
	feeds := make([]unreadCountJSON, 0, len(rows))
	folderMax := map[string]int64{}
	for _, r := range rows {
		total += r.Count
		if r.MaxID > totalMax {
			totalMax = r.MaxID
		}
		feeds = append(feeds, unreadCountJSON{ID: feedID(r.FeedID), Count: r.Count, NewestItemTimestampUsec: strconv.FormatInt(r.MaxID, 10)})
		f := perFolder[r.Folder]
		if f == nil {
			f = &unreadCountJSON{ID: labelPrefix + r.Folder}
			perFolder[r.Folder] = f
			folderOrder = append(folderOrder, r.Folder)
		}
		f.Count += r.Count
		if r.MaxID > folderMax[r.Folder] {
			folderMax[r.Folder] = r.MaxID
		}
	}
	out := make([]unreadCountJSON, 0, len(feeds)+len(folderOrder)+1)
	out = append(out, unreadCountJSON{ID: stateReadingList, Count: total, NewestItemTimestampUsec: strconv.FormatInt(totalMax, 10)})
	for _, name := range folderOrder {
		f := perFolder[name]
		f.NewestItemTimestampUsec = strconv.FormatInt(folderMax[name], 10)
		out = append(out, *f)
	}
	out = append(out, feeds...)
	c.json(http.StatusOK, map[string]any{"max": total, "unreadcounts": out})
}
