package greader

import (
	"net/http"
	"net/http/httptest"
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
	p := postForm("T=tok&t=a;b=c,d$e&a=user/-/label/News+Politics&a=user/-/label/x%2By&i=1&i=2&flag", "application/x-www-form-urlencoded", "")
	require.Equal(t, "a;b=c,d$e", p.Get("t"), "first '=' splits, the rest is value")
	require.Equal(t, []string{"user/-/label/News Politics", "user/-/label/x+y"}, p.All("a"), "+ is a space, %2B is a plus")
	require.Equal(t, []string{"user/-/label/News+Politics", "user/-/label/x%2By"}, p.AllRaw("a"))
	require.Equal(t, []string{"1", "2"}, p.All("i"))
	require.True(t, p.Has("flag"))
	require.Equal(t, "", p.Get("flag"))
}

func TestFormBodyBeforeQueryAndMerge(t *testing.T) {
	p := postForm("i=body1&s=body", "application/x-www-form-urlencoded", "i=q1&i=q2&n=5")
	require.Equal(t, "body", p.Get("s"))
	require.Equal(t, []string{"body1", "q1", "q2"}, p.All("i"))
	require.Equal(t, "5", p.Get("n"))
	require.Equal(t, "body1", p.Get("i"))
}

func TestFormAnyContentTypeParsesAsUrlencoded(t *testing.T) {
	for _, ct := range []string{
		"application/x-www-form-urlencoded; charset=utf-8", "text/plain", "application/octet-stream", "", "not a media type;;",
	} {
		p := postForm("T=abc&i=7", ct, "")
		require.Equal(t, "abc", p.Get("T"), "ctype %q", ct)
		require.Equal(t, []string{"7"}, p.All("i"), "ctype %q", ct)
	}
}

func TestFormBadEscapeKeepsRawText(t *testing.T) {
	p := postForm("s=100%&t=%zz", "", "")
	require.Equal(t, "100%", p.Get("s"))
	require.Equal(t, "%zz", p.Get("t"))
}

func TestFormMultipart(t *testing.T) {
	body := "--XX\r\nContent-Disposition: form-data; name=\"Email\"\r\n\r\nowner\r\n" +
		"--XX\r\nContent-Disposition: form-data; name=\"Passwd\"\r\n\r\npw\r\n--XX--\r\n"
	p := postForm(body, "multipart/form-data; boundary=XX", "")
	require.Equal(t, "owner", p.Get("Email"))
	require.Equal(t, "pw", p.Get("Passwd"))
}

func TestFormBodyCap(t *testing.T) {
	big := "i=1&" + strings.Repeat("x", maxBody+1000)
	p := postForm(big, "", "")
	require.Equal(t, "1", p.Get("i"))
	require.LessOrEqual(t, len(p.RawBody()), maxBody)
}

func TestFormGETIgnoresBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x?n=3&output=json", strings.NewReader("i=1"))
	p := readParams(r, false)
	require.Equal(t, "3", p.Get("n"))
	require.False(t, p.Has("i"))
}

func TestFormRawBodyForImport(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`<opml a="b&c"/>`))
	r.Header.Set("Content-Type", "text/xml")
	p := readParams(r, true)
	require.Equal(t, `<opml a="b&c"/>`, p.RawBody())
	require.False(t, p.Has("a"))
}
