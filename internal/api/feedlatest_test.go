package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func latestFeeds(t *testing.T, body map[string]any) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, f := range body["feeds"].([]any) {
		m := f.(map[string]any)
		var ids []string
		for _, it := range m["items"].([]any) {
			ids = append(ids, it.(map[string]any)["id"].(string))
		}
		out[m["feed_id"].(string)] = ids
	}
	return out
}

func TestFeedsLatest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	a := h.addFeed("A", 0)
	b := h.addFeed("B", 0)
	h.addFeed("Empty", 0)
	gone := h.addFeed("Gone", 0)
	a1 := h.addItem(a, seedItem{SortAt: 100})
	a2 := h.addItem(a, seedItem{SortAt: 300, Read: true}) // read articles count: newest of all, not of unread
	a3 := h.addItem(a, seedItem{SortAt: 200})
	a4 := h.addItem(a, seedItem{SortAt: 400})
	h.addItem(a, seedItem{SortAt: 500, Read: true}) // muted below
	b1 := h.addItem(b, seedItem{SortAt: 50})
	h.addItem(gone, seedItem{})
	// The newest article of A is muted, so it is not a headline.
	h.exec("UPDATE items SET muted_by = 7 WHERE sort_at = 500")
	h.exec("UPDATE feeds SET url = 'kipple:deleting:' || id, url_key = 'kipple:deleting:' || id WHERE id = ?", gone)

	code, body, _ := h.api(c, "GET", "/api/feeds/latest", "")
	require.Equal(t, http.StatusOK, code)
	got := latestFeeds(t, body)
	require.Equal(t, map[string][]string{
		sid(a): {sid(a4), sid(a2), sid(a3)},
		sid(b): {sid(b1)},
	}, got, "3 by default, newest first, read ones included; an empty, a deleting and the muted are out")

	code, body, _ = h.api(c, "GET", "/api/feeds/latest?n=1", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{sid(a4)}, latestFeeds(t, body)[sid(a)])
	code, body, _ = h.api(c, "GET", "/api/feeds/latest?n=10", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{sid(a4), sid(a2), sid(a3), sid(a1)}, latestFeeds(t, body)[sid(a)])

	first := body["feeds"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "feed_id", "title", "read", "starred", "published_at"} {
		require.Contains(t, first, k, "what a headline row draws")
	}
	for _, k := range []string{"excerpt", "image", "url", "author", "source"} {
		require.NotContains(t, first, k, "a headline carries no excerpt, picture or link: the page draws none of them")
	}

	for _, bad := range []string{"n=0", "n=-1", "n=11", "n=x", "n=1.5"} {
		code, _, _ = h.api(c, "GET", "/api/feeds/latest?"+bad, "")
		require.Equal(t, http.StatusBadRequest, code, bad)
	}
}

func TestFeedsLatestNeedsASession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do("GET", "/api/feeds/latest", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// A browser's fetch() sends fetch metadata and an Accept header; the endpoint answers it as it answers the test's
// plain request, and a read from another site is still refused by the cookie (SameSite) and not by a header rule, as
// for every other GET.
func TestFeedsLatestWithBrowserHeaders(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	h.addItem(h.addFeed("A", 0), seedItem{})
	browser := func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Sec-Fetch-Mode", "cors")
		r.Header.Set("Sec-Fetch-Dest", "empty")
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Referer", "http://example.com/feeds/latest")
	}
	code, body, rec := h.api(c, "GET", "/api/feeds/latest?n=3", "", browser)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, body["feeds"], 1)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
}
