package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A well-formed file that is not OPML, and an OPML with nothing in it, are refused with a reason the dialog can
// show, not answered with an import that added nothing.
func TestOPMLImportRefusesNonOPMLAndEmpty(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	for _, tc := range []struct{ name, body, code string }{
		{"rss", `<?xml version="1.0"?><rss version="2.0"><channel><title>x</title></channel></rss>`, "not_opml"},
		{"html", `<html><body>hi</body></html>`, "not_opml"},
		{"truncated", `<?xml version="1.0"?><opml version="2.0"><body><outline text="a"`, "bad_opml"},
		{"empty opml", `<?xml version="1.0"?><opml version="2.0"><head/><body/></opml>`, "empty_opml"},
		// Issue #487: an empty file, a JSON body and a file of folders alone each get their own reason.
		{"0 bytes", ``, `"empty_opml","message":"that file is empty"`},
		{"json", `{"error":"not_found"}`, "not_opml"},
		{"folders only", `<opml><body><outline text="Empty"/></body></opml>`, `"message":"that OPML file has no feeds in it"`},
		// Issue #467: an entity nobody defines refuses the file.
		{"entity", `<opml><body><outline text="&x;"><outline xmlUrl="https://a.test/f"/></outline></body></opml>`, `"opml_entity","message":"\u0026x; on line 1 is not an entity XML or HTML defines"`},
	} {
		rec := h.do("POST", "/api/opml", tc.body, withCookie(c))
		require.Equal(t, http.StatusBadRequest, rec.Code, tc.name)
		require.Contains(t, rec.Body.String(), tc.code, tc.name)
	}
	var n int
	require.NoError(t, h.db.Reader().QueryRow("SELECT count(*) FROM folders WHERE name = 'Empty'").Scan(&n))
	require.Zero(t, n, "a refused file creates nothing")
}
