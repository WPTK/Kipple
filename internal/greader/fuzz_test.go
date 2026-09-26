package greader

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// FuzzReadParams: the request parameter reader never panics on any query,
// body or content type, and never returns more body than the limit allows.
func FuzzReadParams(f *testing.F) {
	ct := []string{"application/x-www-form-urlencoded", "multipart/form-data; boundary=b", "text/plain", ""}
	f.Add("a=1&b=%zz", "T=x&s=user/-/label/A&B&i=1", 0)
	f.Add("", "--b\r\nContent-Disposition: form-data; name=\"s\"\r\n\r\nv\r\n--b--\r\n", 1)
	f.Add("s=feed/http://x/y?a=b&c", "s=user/-/label/R&", 0)
	f.Add("%", strings.Repeat("a=b&", 1000), 2)
	f.Fuzz(func(t *testing.T, query, body string, ctIdx int) {
		if _, err := url.Parse("/x?" + query); err != nil || strings.ContainsAny(query, " \r\n") {
			return // not a valid request target
		}
		c := ct[uint(ctIdx)%uint(len(ct))]
		for _, repair := range []bool{false, true} {
			r, err := http.NewRequest("POST", "/x?"+query, strings.NewReader(body))
			if err != nil {
				return
			}
			r.Header.Set("Content-Type", c)
			p := readParamsLimit(r, false, 4096, repair)
			_ = p.Get("s")
			_ = p.All("i")
			_ = p.Has("T")
			if len(p.RawBody()) > 4096 {
				t.Fatalf("raw body %d exceeds the limit", len(p.RawBody()))
			}
		}
	})
}
