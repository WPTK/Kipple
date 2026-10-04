package fetch

import (
	"bytes"
	"strings"
	"testing"
)

// rewriteDeclCopy is the straightforward copying form rewriteDecl must match
// byte for byte (body_hash depends on it).
func rewriteDeclCopy(b []byte) []byte {
	loc := xmlDeclRe.FindSubmatchIndex(b)
	if loc == nil {
		return b
	}
	out := make([]byte, 0, len(b))
	out = append(out, b[:loc[4]]...)
	out = append(out, "utf-8"...)
	out = append(out, b[loc[5]:]...)
	return out
}

func TestRewriteDeclMatchesCopy(t *testing.T) {
	const tail = `<rss><t>café</t></rss>`
	for _, label := range []string{"utf-8", "UTF-8", "Utf-8", "ascii", "utf8", "latin1", "windows-1252", "ISO-8859-2", "x"} {
		for _, q := range []string{`"`, `'`} {
			in := `<?xml version="1.0" encoding=` + q + label + q + `?>` + tail
			want := rewriteDeclCopy([]byte(in))
			got := rewriteDecl([]byte(in))
			if !bytes.Equal(got, want) {
				t.Errorf("label %q: got %q want %q", label, got, want)
			}
			if !strings.Contains(string(got), "encoding="+q+"utf-8"+q) {
				t.Errorf("label %q: declaration not utf-8: %q", label, got)
			}
		}
	}
	for _, in := range []string{"", "<rss/>", `<?xml version="1.0"?><rss/>`} {
		if got := rewriteDecl([]byte(in)); string(got) != in {
			t.Errorf("no declared encoding changed %q to %q", in, got)
		}
	}
}

func TestRewriteDeclAllocation(t *testing.T) {
	same := []byte(`<?xml version="1.0" encoding="utf-8"?><rss/>`)
	if &rewriteDecl(same)[0] != &same[0] {
		t.Error("utf-8 declaration copied the body")
	}
	five := []byte(`<?xml version="1.0" encoding="UTF-8"?><rss/>`)
	if got := rewriteDecl(five); &got[0] != &five[0] {
		t.Error("same-length declaration copied the body")
	}
	long := []byte(`<?xml version="1.0" encoding="windows-1252"?><rss/>`)
	if got := rewriteDecl(long); &got[0] == &long[0] {
		t.Error("different-length declaration should allocate")
	}
}

func benchBody(label string) []byte {
	return []byte(`<?xml version="1.0" encoding="` + label + `"?><rss><channel>` + strings.Repeat(`<item><title>café</title></item>`, 8000) + `</channel></rss>`)
}

func BenchmarkRewriteDecl(b *testing.B) {
	for _, label := range []string{"utf-8", "UTF-8", "windows-1252"} {
		src := benchBody(label)
		b.Run(label+"/new", func(b *testing.B) {
			b.ReportAllocs()
			buf := make([]byte, len(src))
			for i := 0; i < b.N; i++ {
				copy(buf, src)
				rewriteDecl(buf)
			}
		})
		b.Run(label+"/old", func(b *testing.B) {
			b.ReportAllocs()
			buf := make([]byte, len(src))
			for i := 0; i < b.N; i++ {
				copy(buf, src)
				rewriteDeclCopy(buf)
			}
		})
	}
}
