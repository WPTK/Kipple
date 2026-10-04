package api

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/sched"
)

// A browser form upload sends the OPML as a multipart file part.
func TestOPMLImportMultipart(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	doc := `<?xml version="1.0"?><opml version="2.0"><head/><body>
	<outline type="rss" text="Beta" xmlUrl="https://b.example/feed.xml"/></body></opml>`

	form := func(field, filename, content string) (string, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("note", "ignored")
		var part interface{ Write([]byte) (int, error) }
		if filename != "" {
			part, _ = mw.CreateFormFile(field, filename)
		} else {
			part, _ = mw.CreateFormField(field)
		}
		_, _ = part.Write([]byte(content))
		_ = mw.Close()
		return buf.String(), mw.FormDataContentType()
	}
	ct := func(v string) func(*http.Request) { return func(r *http.Request) { r.Header.Set("Content-Type", v) } }

	body, typ := form("upload", "subs.opml", doc)
	rec := h.do("POST", "/api/opml", body, withCookie(c), ct(typ))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"feeds_added":1`)

	// A plain field named "opml" also counts as the file.
	body, typ = form("opml", "", strings.Replace(doc, "b.example", "c.example", 1))
	rec = h.do("POST", "/api/opml", body, withCookie(c), ct(typ))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"feeds_added":1`)

	// No file part at all.
	body, typ = form("other", "", doc)
	require.Equal(t, http.StatusBadRequest, h.do("POST", "/api/opml", body, withCookie(c), ct(typ)).Code)

	// Over the size limit.
	big := strings.Repeat(" ", maxOPMLBody+1)
	require.Equal(t, http.StatusRequestEntityTooLarge, h.do("POST", "/api/opml", big, withCookie(c)).Code)
}

func TestRefreshErrors(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.sched.mu.Lock()
	h.sched.refreshErr = sched.ErrStopped
	h.sched.mu.Unlock()
	rec := h.do("POST", "/api/refresh", "", withCookie(c))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "shutting_down")

	h.sched.mu.Lock()
	h.sched.refreshErr = errors.New("boom")
	h.sched.mu.Unlock()
	require.Equal(t, http.StatusInternalServerError, h.do("POST", "/api/refresh", "", withCookie(c)).Code)
}
