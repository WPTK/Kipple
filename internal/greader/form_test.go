package greader

import (
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func postForm(body, ctype, query string) *Params {
	r := httptest.NewRequest(http.MethodPost, "/x?"+query, strings.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	return readParams(r, false)
}

func TestFormSemicolonEqualsPlusAndRepeats(t *testing.T) {
	t.Parallel()
	p := postForm("T=tok&t=a;b=c,d$e&a=user/-/label/News+Politics&a=user/-/label/x%2By&i=1&i=2&flag", "application/x-www-form-urlencoded", "")
	require.Equal(t, "a;b=c,d$e", p.Get("t"), "first '=' splits, the rest is value")
	require.Equal(t, []string{"user/-/label/News Politics", "user/-/label/x+y"}, p.All("a"), "+ is a space, %2B is a plus")
	require.Equal(t, []string{"user/-/label/News+Politics", "user/-/label/x%2By"}, p.AllRaw("a"))
	require.Equal(t, []string{"1", "2"}, p.All("i"))
	require.True(t, p.Has("flag"))
	require.Equal(t, "", p.Get("flag"))
}

func TestFormBodyBeforeQueryAndMerge(t *testing.T) {
	t.Parallel()
	p := postForm("i=body1&s=body", "application/x-www-form-urlencoded", "i=q1&i=q2&n=5")
	require.Equal(t, "body", p.Get("s"))
	require.Equal(t, []string{"body1", "q1", "q2"}, p.All("i"))
	require.Equal(t, "5", p.Get("n"))
	require.Equal(t, "body1", p.Get("i"))
}

func TestFormAnyContentTypeParsesAsUrlencoded(t *testing.T) {
	t.Parallel()
	for _, ct := range []string{
		"application/x-www-form-urlencoded; charset=utf-8", "text/plain", "application/octet-stream", "", "not a media type;;",
	} {
		p := postForm("T=abc&i=7", ct, "")
		require.Equal(t, "abc", p.Get("T"), "ctype %q", ct)
		require.Equal(t, []string{"7"}, p.All("i"), "ctype %q", ct)
	}
}

func TestFormBadEscapeKeepsRawText(t *testing.T) {
	t.Parallel()
	p := postForm("s=100%&t=%zz", "", "")
	require.Equal(t, "100%", p.Get("s"))
	require.Equal(t, "%zz", p.Get("t"))
}

func TestFormMultipart(t *testing.T) {
	t.Parallel()
	body := "--XX\r\nContent-Disposition: form-data; name=\"Email\"\r\n\r\nowner\r\n" +
		"--XX\r\nContent-Disposition: form-data; name=\"Passwd\"\r\n\r\npw\r\n--XX--\r\n"
	p := postForm(body, "multipart/form-data; boundary=XX", "")
	require.Equal(t, "owner", p.Get("Email"))
	require.Equal(t, "pw", p.Get("Passwd"))
}

func TestFormBodyCap(t *testing.T) {
	t.Parallel()
	big := "i=1&" + strings.Repeat("x", maxBody+1000)
	p := postForm(big, "", "")
	require.Equal(t, "1", p.Get("i"))
	require.LessOrEqual(t, len(p.RawBody()), maxBody)
}

func TestFormGETIgnoresBody(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/x?n=3&output=json", strings.NewReader("i=1"))
	p := readParams(r, false)
	require.Equal(t, "3", p.Get("n"))
	require.False(t, p.Has("i"))
}

func TestFormRawBodyForImport(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`<opml a="b&c"/>`))
	r.Header.Set("Content-Type", "text/xml")
	p := readParams(r, true)
	require.Equal(t, `<opml a="b&c"/>`, p.RawBody())
	require.False(t, p.Has("a"))
}

// A multipart value longer than maxPartValue is never cut silently: the parse
// is marked truncated so every handler refuses it (413), like an over-long body.
func TestMultipartOverlongValueIsTruncated(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	mw := multipart.NewWriter(&b)
	require.NoError(t, mw.WriteField("T", "tok"))
	require.NoError(t, mw.WriteField("i", strings.Repeat("1", maxPartValue+1)))
	require.NoError(t, mw.Close())
	p := postForm(b.String(), mw.FormDataContentType(), "")
	require.True(t, p.truncated, "an over-long part marks the parse truncated")

	b.Reset()
	mw = multipart.NewWriter(&b)
	require.NoError(t, mw.WriteField("i", strings.Repeat("1", maxPartValue)))
	require.NoError(t, mw.Close())
	p = postForm(b.String(), mw.FormDataContentType(), "")
	require.False(t, p.truncated, "exactly the cap is fine")
	require.Len(t, p.Get("i"), maxPartValue)

	// End to end: the handler answers 413 and does not act.
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	id := seedN(h, f, 1, nil)[0]
	b.Reset()
	mw = multipart.NewWriter(&b)
	require.NoError(t, mw.WriteField("i", strconv.FormatInt(id, 10)))
	require.NoError(t, mw.WriteField("a", readSt))
	require.NoError(t, mw.WriteField("pad", strings.Repeat("x", maxPartValue+1)))
	require.NoError(t, mw.Close())
	w := h.do(http.MethodPost, base+rd+"edit-tag", b.String(), map[string]string{"Content-Type": mw.FormDataContentType()})
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.False(t, isRead(h, id))
}
