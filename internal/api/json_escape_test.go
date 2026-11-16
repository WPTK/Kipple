package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Article HTML goes out as written: angle brackets and ampersands take one byte each, not six, and a client
// that parses the body gets exactly the same text either way.
func TestWriteJSONKeepsMarkupUnescaped(t *testing.T) {
	t.Parallel()
	html := "<p>Fish & chips <a href='/a?x=1&y=2'>link</a></p>"
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]any{"content_html": html})

	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "{'content_html':'"+html+"'}", strings.ReplaceAll(strings.TrimSpace(rec.Body.String()), "\"", "'"))
	var got map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, html, got["content_html"])

	// The line separators that break a script literal stay escaped.
	rec = httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]any{"s": "a" + string(rune(0x2028)) + "b"})
	require.Equal(t, "{'s':'a"+string(rune(92))+"u2028b'}", strings.ReplaceAll(strings.TrimSpace(rec.Body.String()), "\"", "'"))
}
