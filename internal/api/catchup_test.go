package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatchupCountsAndMark(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	news := h.addFolder("News")
	sub := h.addFolder("Sub")
	h.exec("UPDATE folders SET parent_id = ? WHERE id = ?", news, sub)
	a := h.addFeed("A", news)
	b := h.addFeed("B", sub)
	z := h.addFeed("Z", 0)
	a1 := h.addItem(a, seedItem{})
	b1 := h.addItem(b, seedItem{})
	h.addItem(a, seedItem{Read: true})
	z1 := h.addItem(z, seedItem{})
	groups := "?groups=d" + sid(news) + ",f" + sid(z) + ",d99999"

	code, body, _ := h.api(c, "GET", "/api/catchup"+groups, "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "0", body["mark"])
	require.Equal(t, sid(z1), body["as_of"], "as_of is the newest committed id")
	secs := body["sections"].([]any)
	require.Len(t, secs, 3)
	count := func(i int) float64 { return secs[i].(map[string]any)["count"].(float64) }
	require.Equal(t, float64(2), count(0), "a folder counts its subfolders and skips read articles")
	require.Equal(t, float64(1), count(1))
	require.Equal(t, float64(0), count(2), "a folder that is gone counts nothing")
	require.Equal(t, "d"+sid(news), secs[0].(map[string]any)["group"])

	// Leaving the page moves the mark to its as_of; the next visit counts only what arrived after it.
	code, body, _ = h.api(c, "POST", "/api/catchup/seen", `{"as_of":"`+sid(b1)+`"}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, sid(b1), body["mark"])
	_, body, _ = h.api(c, "GET", "/api/catchup"+groups, "")
	require.Equal(t, sid(b1), body["mark"])
	secs = body["sections"].([]any)
	require.Equal(t, float64(0), secs[0].(map[string]any)["count"], "a1 and b1 are at or below the mark")
	require.Equal(t, float64(1), secs[1].(map[string]any)["count"])
	_ = a1

	// mark= counts against that mark instead of the server's, and is the mark answered.
	_, body, _ = h.api(c, "GET", "/api/catchup"+groups+"&mark=0", "")
	require.Equal(t, "0", body["mark"])
	secs = body["sections"].([]any)
	require.Equal(t, float64(2), secs[0].(map[string]any)["count"], "against mark 0, a1 and b1 count again")
	require.Equal(t, float64(1), secs[1].(map[string]any)["count"])
	_, body, _ = h.api(c, "GET", "/api/catchup"+groups+"&mark="+sid(z1), "")
	require.Equal(t, sid(z1), body["mark"])
	require.Equal(t, float64(0), body["sections"].([]any)[1].(map[string]any)["count"])
	_, body, _ = h.api(c, "GET", "/api/catchup"+groups+"&mark="+sid(z1+100), "")
	require.Equal(t, sid(z1), body["mark"], "a mark above as_of is capped to it, as POST /api/catchup/seen does")
	_, body, _ = h.api(c, "GET", "/api/catchup"+groups, "")
	require.Equal(t, sid(b1), body["mark"], "asking about another mark leaves the server's alone")

	// It never moves back (a second device leaving later with an older page), and never past what exists.
	_, body, _ = h.api(c, "POST", "/api/catchup/seen", `{"as_of":"`+sid(a1)+`"}`)
	require.Equal(t, sid(b1), body["mark"])
	// A page that knew of a newer article than exists now (a feed was deleted elsewhere) still moves the mark as far as it can.
	code, body, _ = h.api(c, "POST", "/api/catchup/seen", `{"as_of":"`+sid(z1+1)+`"}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, sid(z1), body["mark"])
	for _, bad := range []string{`{}`, `{"as_of":"0"}`, `{"as_of":"x"}`, `{"as_of":5}`} {
		code, _, _ = h.api(c, "POST", "/api/catchup/seen", bad)
		require.Equal(t, http.StatusBadRequest, code, bad)
	}
	for _, bad := range []string{"?groups=x1", "?groups=d", "?groups=d0", "?groups=d1,,f2", "?groups=g3", "?mark=x", "?mark=-1", "?mark=5x", "?mark="} {
		code, _, _ = h.api(c, "GET", "/api/catchup"+bad, "")
		require.Equal(t, http.StatusBadRequest, code, bad)
	}
}

// The mark is one value for the account, kept as the hidden setting catchup.seen.
func TestCatchupMarkIsAHiddenSetting(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	h.api(c, "POST", "/api/catchup/seen", `{"as_of":"`+sid(id)+`"}`)
	_, body, _ := h.api(c, "GET", "/api/settings", "")
	require.Equal(t, sid(id), body["values"].(map[string]any)["catchup.seen"])
	for _, s := range body["settings"].([]any) {
		if m := s.(map[string]any); m["key"] == "catchup.seen" {
			require.Equal(t, "hidden", m["surface"])
		}
	}
	// Only leaving the page writes it: a settings write could move it back or past every article.
	for _, v := range []string{`"1"`, `"9000000000000000000"`, `"0"`, `null`} {
		code, _, _ := h.api(c, "PATCH", "/api/settings", `{"catchup.seen":`+v+`}`)
		require.Equal(t, http.StatusBadRequest, code, v)
	}
	_, body, _ = h.api(c, "GET", "/api/settings", "")
	require.Equal(t, sid(id), body["values"].(map[string]any)["catchup.seen"])
}

func TestCatchupRoutesNeedASessionAndOrigin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, tc := range []struct{ method, path string }{{"GET", "/api/catchup"}, {"POST", "/api/catchup/seen"}} {
		rec := h.do(tc.method, tc.path, `{"as_of":"1"}`)
		require.Equal(t, http.StatusUnauthorized, rec.Code, tc.path)
	}
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	body := `{"as_of":"` + sid(id) + `"}`
	noClient := func(r *http.Request) { r.Header.Del("X-Kipple-Client") }
	crossSite := func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }
	badOrigin := func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
		r.Header.Set("Origin", "https://evil.example")
	}
	goodOrigin := func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
		r.Header.Set("Origin", "http://"+r.Host)
	}
	for label, mod := range map[string]func(*http.Request){"no client header": noClient, "cross-site": crossSite, "bad origin": badOrigin} {
		code, resp, _ := h.api(c, "POST", "/api/catchup/seen", body, mod)
		require.Equal(t, http.StatusForbidden, code, label)
		require.Equal(t, "origin", resp["error"])
	}
	code, _, _ := h.api(c, "POST", "/api/catchup/seen", body, goodOrigin)
	require.Equal(t, http.StatusOK, code, "what a browser sends: Origin, with the client header")
	code, _, _ = h.api(c, "GET", "/api/catchup", "", func(r *http.Request) {
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Sec-Fetch-Mode", "cors")
	})
	require.Equal(t, http.StatusOK, code)
}

func TestItemsAfterAndMarkReadAfter(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	fo := h.addFolder("News")
	a := h.addFeed("A", fo)
	i1 := h.addItem(a, seedItem{SortAt: 100})
	i2 := h.addItem(a, seedItem{SortAt: 200})
	i3 := h.addItem(a, seedItem{SortAt: 300})

	code, body, _ := h.api(c, "GET", "/api/items?view=unread&folder="+sid(fo)+"&after="+sid(i1), "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, strs(i3, i2), itemIDs(t, body), "only what arrived after the mark")
	// The mark before the page was ever left is 0: everything is after it.
	code, body, _ = h.api(c, "GET", "/api/items?view=unread&folder="+sid(fo)+"&after=0", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, strs(i3, i2, i1), itemIDs(t, body))
	for _, q := range []string{"after=-1", "after=x", "view=all&after=" + sid(i1), "q=body&after=" + sid(i1)} {
		code, _, _ = h.api(c, "GET", "/api/items?"+q, "")
		require.Equal(t, http.StatusBadRequest, code, q)
	}

	// Mark all on the New page marks what arrived after the mark and leaves the older unread alone.
	code, _, _ = h.api(c, "POST", "/api/items/mark-read",
		`{"read":true,"scope":{"folder_id":"`+sid(fo)+`","view":"unread","after_id":"`+sid(i1)+`"}}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE read = 0"))
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", i1))
	code, _, _ = h.api(c, "POST", "/api/items/mark-read", `{"read":true,"scope":{"all":true,"view":"all","after_id":"`+sid(i1)+`"}}`)
	require.Equal(t, http.StatusBadRequest, code, "after_id belongs to the unread list")
}
