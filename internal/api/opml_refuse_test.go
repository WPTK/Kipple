package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A well-formed file that is not OPML, and an OPML with nothing in it, are refused with a reason the dialog can
// show, not answered with an import that added nothing.
func TestOPMLImportRefusesNonOPMLAndEmpty(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	for _, tc := range []struct{ name, body, code string }{
		{"rss", `<?xml version="1.0"?><rss version="2.0"><channel><title>x</title></channel></rss>`, "not_opml"},
		{"html", `<html><body>hi</body></html>`, "not_opml"},
		{"truncated", `<?xml version="1.0"?><opml version="2.0"><body><outline text="a"`, "bad_opml"},
		{"empty opml", `<?xml version="1.0"?><opml version="2.0"><head/><body/></opml>`, "empty_opml"},
	} {
		rec := h.do("POST", "/api/opml", tc.body, withCookie(c))
		require.Equal(t, http.StatusBadRequest, rec.Code, tc.name)
		require.Contains(t, rec.Body.String(), tc.code, tc.name)
	}
}
