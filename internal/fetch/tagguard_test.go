package fetch

import (
	"encoding/xml"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

// genXML builds a well-formed fragment that uses every construct whose end the guard has to find the way the decoder
// does: quoted attribute values holding ">" and the other quote, comments, CDATA sections, instructions and
// directives that hold markup-looking text.
func genXML(r *rand.Rand, depth int) string {
	pick := func(s ...string) string { return s[r.IntN(len(s))] }
	pad := func() string { return strings.Repeat("p", r.IntN(30)) }
	fake := `<f g="` + strings.Repeat("q", 150) + `">` // longer than any real tag here, so counting it is seen
	var b strings.Builder
	name := pick("a", "b", "item", "x:y")
	b.WriteString("<" + name)
	for range r.IntN(4) {
		b.WriteString(pick(" ", "  ", "\n") + pick("k", "data-v", "w:z"))
		switch r.IntN(7) {
		case 0:
		case 1:
			b.WriteString(`="` + pick("", "x", ">", "a>b", "it's", "&amp;", "&gt;", "&#62;", "a/>b", "&", "&#") + pad() + `"`)
		case 2:
			b.WriteString(`='` + pick("", `say "hi">`, ">", "&lt;") + pad() + `'`)
		case 3:
			b.WriteString(` = "` + pad() + `"`)
		case 4:
			b.WriteString("=abc")
		default:
			b.WriteString(`="` + pad() + `"`)
		}
	}
	if r.IntN(3) == 0 || depth == 0 {
		b.WriteString(pick("/>", " />"))
		return b.String()
	}
	b.WriteString(">")
	for range r.IntN(5) {
		switch r.IntN(10) {
		case 0:
			b.WriteString("text > and &amp; " + pad())
		case 1:
			b.WriteString(`<!-- a > b <c d="e> - ` + pad() + fake + ` -->`)
		case 2:
			b.WriteString(`<![CDATA[ <a b="x> ]> ]] ` + pad() + fake + ` ]]>`)
		case 3:
			b.WriteString(`<?pi a>b ? <c d="` + pad() + `" ` + fake + `?>`)
		case 4:
			b.WriteString(`<!DOCTYPE x [ <!ENTITY a ">` + fake + `"> <!-- c > ` + fake + ` --> <!ENTITY b 'x"y>` + fake + `'> ]>`)
		case 5:
			b.WriteString(`<!ENTITY q "'"> <!DOCTYPE y [ <!ELEMENT a (b)> <!-- ` + fake + ` --> <!ATTLIST a c CDATA "` + fake + `"> ]> ` + pad())
		case 6:
			// Markup of its own inside a directive, after declarations and after a comment.
			b.WriteString(`<!DOCTYPE z [ <!ENTITY a "b"> ` + fake + ` <!-- c --> ` + fake + ` ]>`)
		default:
			b.WriteString(genXML(r, depth-1))
		}
	}
	b.WriteString("</" + name + pick("", " ", "\n") + ">")
	return b.String()
}

// longestTag is the length of the longest start or end tag of a well-formed document, read with the decoder gofeed
// uses (non-strict), by how far each tag moved its offset.
func longestTag(t *testing.T, doc string) int {
	d := xml.NewDecoder(strings.NewReader(doc))
	d.Strict = false
	longest, prev := 0, int64(0)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return longest
		}
		require.NoError(t, err, doc)
		switch tok.(type) {
		case xml.StartElement, xml.EndElement:
			longest = max(longest, int(d.InputOffset()-prev))
		}
		prev = d.InputOffset()
	}
}

// guarded reads a document through a tagGuard with the given limit, as checkNesting does, and returns how the
// token stream ended.
func guarded(doc string, limit int, oneByte bool) error {
	var src io.Reader = strings.NewReader(doc)
	if oneByte {
		src = iotest.OneByteReader(src)
	}
	d := xml.NewDecoder(&tagGuard{r: src, limit: limit})
	d.Strict = false
	for {
		if _, err := d.Token(); err != nil {
			return err
		}
	}
}

// The guard refuses exactly the documents that have a tag longer than its limit: one byte under the longest tag
// stops it, the longest tag itself does not, whatever the markup around the tag looks like and however the bytes are
// split across reads.
func TestTagGuardAgreesWithTheDecoder(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for i := range 400 {
		doc := `<?xml version="1.0"?><root a="1">` + genXML(r, 3) + genXML(r, 2) + `</root>`
		longest := longestTag(t, doc)
		oneByte := i%2 == 0
		require.ErrorIs(t, guarded(doc, longest, oneByte), io.EOF, "at the longest tag: %s", doc)
		require.ErrorIs(t, guarded(doc, longest-1, oneByte), errTagTooLong, "one under it: %s", doc)
	}
}

// The guard ends the stream within one byte of the limit, so a tag of any length is never read past it.
func TestTagGuardStopsAtTheLimit(t *testing.T) {
	// An endless tag: attributes with no end.
	g := &tagGuard{r: io.MultiReader(strings.NewReader("<x "), endless(" a=1"))}
	total, buf := 0, make([]byte, 4096)
	for {
		n, err := g.Read(buf)
		total += n
		if err != nil {
			require.ErrorIs(t, err, errTagTooLong)
			break
		}
		require.LessOrEqual(t, total, MaxTagBytes)
	}
	require.Equal(t, MaxTagBytes, total)
	n, err := g.Read(buf)
	require.Zero(t, n)
	require.ErrorIs(t, err, errTagTooLong, "it stays failed")
}

// endless reads the same bytes for ever.
type endless string

func (e endless) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		n += copy(p[n:], string(e))
	}
	return n, nil
}

// One start tag over the limit refuses the document; the same number of bytes in anything that is not a tag does not.
func TestRefusesOneOverlongTag(t *testing.T) {
	head, tail := `<x a="`, `"/>`
	tag := func(n int) string { return head + strings.Repeat("v", n-len(head)-len(tail)) + tail }
	doc := func(inner string) []byte {
		return []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>t</title>` + inner + `</channel></rss>`)
	}
	require.NoError(t, checkNesting(doc(tag(MaxTagBytes))))
	require.ErrorIs(t, checkNesting(doc(tag(MaxTagBytes+1))), errTagTooLong)
	// An end tag counts as well.
	require.ErrorIs(t, checkNesting(doc("<x></x"+strings.Repeat(" ", MaxTagBytes)+">")), errTagTooLong)
	// A flood of short attributes in one tag.
	flood := "<x" + strings.Repeat(" a=1", MaxTagBytes/4) + "/>"
	require.ErrorIs(t, checkNesting(doc(flood)), errTagTooLong)

	long := strings.Repeat("x", MaxTagBytes+1)
	for name, inner := range map[string]string{
		"text":        long,
		"comment":     "<!--" + long + "-->",
		"cdata":       "<![CDATA[" + long + "]]>",
		"instruction": "<?p " + long + "?>",
		"directive":   `<!DOCTYPE x [ <!ENTITY a "` + long + `"> ]>`,
	} {
		require.NoError(t, checkNesting(doc(inner)), name)
	}
}

// The first tag of a document is read before the type is known, by a decoder of its own.
func TestRefusesOverlongRootTag(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><rss version="2.0"` + strings.Repeat(" a=1", MaxTagBytes/4) + `><channel/></rss>`)
	require.ErrorIs(t, checkProlog(body), errTagTooLong)
	err := parseErr(string(body))
	require.Error(t, err)
	require.Contains(t, err.Error(), "tag longer")
}
