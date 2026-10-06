package greader

// Protocol conformance suite (issue #256).
//
// These tests drive the real handler over HTTP the way any Google Reader API
// client does: sign in with ClientLogin, keep the Auth value, send it as
// "Authorization: GoogleLogin auth=<token>", and use only what the responses
// return. No test depends on a particular client application; each assertion
// names the reference it follows:
//
//   - [GR]   the historical Google Reader API as clients implement it
//            (ClientLogin, the GoogleLogin header, T tokens, stream ids,
//            itemRefs and item shapes).
//   - [RS]   the two reference server implementations of the API, the ones
//            clients are most commonly used against. Where they disagree,
//            the assertion says which one Kipple follows and
//            docs/compatibility.md lists the divergence.
//   - [K §x] docs/design.md, where Kipple decides between them or goes
//            further. Deliberate divergences are listed endpoint by endpoint in
//            docs/compatibility.md.
//
// The library, items and clock come from the package test harness (harness_test.go);
// the HTTP client is the one the contract tests use (contract_test.go).
//
// Run only this suite with:  go test ./internal/greader -run Conformance

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// confUA is a generic User-Agent; every Reader API client is treated the same.
const confUA = "conformance-suite/1.0"

// confClient is a generic Reader API client over a real HTTP server.
type confClient struct {
	*client
	h     *harness
	token string // the Auth value from ClientLogin
}

// newConf starts a server for h and signs in through ClientLogin at the mount prefix.
func newConf(t *testing.T, h *harness) *confClient {
	t.Helper()
	c := &confClient{client: newClient(t, h, base, confUA), h: h}
	c.token = c.login()
	c.auth = "GoogleLogin auth=" + c.token
	return c
}

// login posts ClientLogin and parses the key=value lines on the first '=' only [GR].
func (c *confClient) login() string {
	c.t.Helper()
	r := c.doAny(http.MethodPost, "/accounts/ClientLogin", loginBody(), nil)
	require.Equal(c.t, 200, r.code, r.body)
	kv := parseLoginLines(c.t, r.body)
	require.NotEmpty(c.t, kv["Auth"])
	return kv["Auth"]
}

func parseLoginLines(t *testing.T, body string) map[string]string {
	t.Helper()
	kv := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		k, v, ok := strings.Cut(line, "=")
		require.True(t, ok, "every ClientLogin line is key=value: %q", line)
		kv[k] = v
	}
	return kv
}

// call sends method path with optional body and extra headers, never asserting the status.
func (c *confClient) call(method, path, body string, hdr map[string]string) resp {
	c.t.Helper()
	return c.doAny(method, path, body, hdr)
}

// getJSON GETs a read endpoint and decodes its JSON object.
func (c *confClient) getJSON(path string) map[string]any {
	c.t.Helper()
	r := c.call(http.MethodGet, path, "", nil)
	require.Equal(c.t, 200, r.code, "%s: %s", path, r.body)
	require.Contains(c.t, r.header.Get("Content-Type"), "application/json", path)
	var m map[string]any
	require.NoError(c.t, json.Unmarshal([]byte(r.body), &m), r.body)
	return m
}

// write POSTs a form to a write endpoint with the edit token in T [GR] and requires "OK".
func (c *confClient) write(name, form string) {
	c.t.Helper()
	r := c.call(http.MethodPost, rd+name, form+"&T="+url.QueryEscape(c.token), nil)
	require.Equal(c.t, 200, r.code, "%s: %s", name, r.body)
	require.Equal(c.t, "OK", r.body, name)
}

// ---- library ----

// confLib is the library most conformance tests start from: two folders, one
// feed outside any folder, and items with known states and ids.
type confLib struct {
	tech, news, loose int64            // feed ids
	ids               map[string]int64 // item name -> id
}

// Item ids are crawl times in microseconds; these are fixed so ot, nt and ts
// windows can be asserted exactly. The harness clock stands at baseID.
const (
	confHour = int64(3600) * 1_000_000
	confMin  = int64(60) * 1_000_000
)

func seedConf(h *harness) confLib {
	h.t.Helper()
	l := confLib{ids: map[string]int64{}}
	l.tech = h.addFeed("https://tech.example/feed.xml", "Tech Daily", "Tech")
	l.news = h.addFeed("https://news.example/rss", "World News", "News & Politics")
	l.loose = h.addFeed("https://loose.example/atom", "Loose Feed", "")
	add := func(name string, feed int64, id int64, s itemSeed) {
		s.ID, s.Title = id, name
		s.URL = "https://example.org/" + name
		s.Author = "Author " + name
		l.ids[name] = h.addItem(feed, s)
	}
	// Oldest first.
	add("tech-old-read", l.tech, baseID-3*confHour, itemSeed{Read: true})
	add("news-old-starred", l.news, baseID-2*confHour, itemSeed{Read: true, Starred: true})
	add("loose-old", l.loose, baseID-90*confMin, itemSeed{})
	add("tech-unread", l.tech, baseID-30*confMin, itemSeed{})
	add("news-unread-starred", l.news, baseID-20*confMin, itemSeed{Starred: true})
	add("news-unread", l.news, baseID-10*confMin, itemSeed{})
	add("tech-new", l.tech, baseID-1*confMin, itemSeed{})
	return l
}

// dec is an item id in decimal string form.
func (l confLib) dec(name string) string { return strconv.FormatInt(l.ids[name], 10) }

// decs maps names to decimal ids.
func (l confLib) decs(names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = l.dec(n)
	}
	return out
}

// itemRefs decodes stream/items/ids: the decimal string ids and the continuation, if any.
func itemRefs(t *testing.T, r resp) (ids []string, cont string) {
	t.Helper()
	require.Equal(t, 200, r.code, r.body)
	got, c := decodeIDs(t, r.body)
	if c != nil {
		require.NotEmpty(t, *c, "a continuation, when present, is a non-empty string")
		cont = *c
	}
	return got, cont
}

// confItem is the item shape a generic client decodes [GR][RS].
type confItem struct {
	ID            string `json:"id"`
	CrawlTimeMsec string `json:"crawlTimeMsec"`
	TimestampUsec string `json:"timestampUsec"`
	Published     int64  `json:"published"`
	Updated       int64  `json:"updated"`
	Title         string `json:"title"`
	Author        string `json:"author"`
	Canonical     []struct {
		Href string `json:"href"`
	} `json:"canonical"`
	Alternate []struct {
		Href string `json:"href"`
		Type string `json:"type"`
	} `json:"alternate"`
	Summary struct {
		Content string `json:"content"`
	} `json:"summary"`
	Content struct {
		Content string `json:"content"`
	} `json:"content"`
	Categories []string `json:"categories"`
	Origin     struct {
		StreamID string `json:"streamId"`
		Title    string `json:"title"`
		HTMLURL  string `json:"htmlUrl"`
	} `json:"origin"`
}

type confStream struct {
	ID           string     `json:"id"`
	Updated      int64      `json:"updated"`
	Continuation string     `json:"continuation"`
	Items        []confItem `json:"items"`
}

func decodeStream(t *testing.T, r resp) confStream {
	t.Helper()
	require.Equal(t, 200, r.code, r.body)
	require.Contains(t, r.header.Get("Content-Type"), "application/json")
	var s confStream
	require.NoError(t, json.Unmarshal([]byte(r.body), &s), r.body)
	require.NotNil(t, s.Items, "items is always an array, never null or absent")
	return s
}

// itemDecimal turns an items[].id long form back into the decimal id.
func itemDecimal(t *testing.T, long string) string {
	t.Helper()
	id, ok := ParseItemID(long)
	require.True(t, ok, long)
	return strconv.FormatInt(id, 10)
}

func streamDecimals(t *testing.T, s confStream) []string {
	t.Helper()
	out := make([]string, len(s.Items))
	for i, it := range s.Items {
		out[i] = itemDecimal(t, it.ID)
	}
	return out
}

// longID is the items[].id form of a decimal id: tag:google.com,2005:reader/item/ + 16 hex digits [GR].
func longID(dec string) string {
	n, _ := strconv.ParseInt(dec, 10, 64)
	return FormatLongID(n)
}

func q1(k, v string) string { return k + "=" + url.QueryEscape(v) }
