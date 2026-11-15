package fetch

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/mmcdole/gofeed"
	"github.com/stretchr/testify/require"
)

// The pre-scan reads as much of a document as gofeed does to find its type, and no more: a root element that starts
// within the window is found and scanned, and one past it is not found by gofeed either. If gofeed's window grows,
// the second half of this fails, and detectionWindow must grow with it.
func TestDetectionWindowMatchesGofeed(t *testing.T) {
	doc := func(padding int, depth int) []byte {
		return []byte(`<?xml version="1.0"?><!--` + strings.Repeat("x", padding) + `--><rss version="2.0">` +
			strings.Repeat("<a>", depth-1) + strings.Repeat("</a>", depth-1) + `</rss>`)
	}
	within := doc(detectionWindow-200, MaxNesting+1)
	require.ErrorContains(t, checkNesting(within), "nests elements deeper", "a root inside the window is scanned")
	_, err := gofeed.NewParser().Parse(strings.NewReader(string(doc(detectionWindow-200, 2))))
	require.NoError(t, err)

	past := doc(detectionWindow+100, MaxNesting+1)
	require.NoError(t, checkNesting(past), "a document gofeed does not detect is not scanned")
	_, err = gofeed.NewParser().Parse(strings.NewReader(string(past)))
	require.ErrorIs(t, err, gofeed.ErrFeedTypeNotDetected)
}

// The pre-scan does not copy the body to find its type: reading a body of many small tokens allocates a small
// fraction of the body size.
func TestCheckNestingDoesNotCopyTheBody(t *testing.T) {
	body := []byte(`<rss version="2.0"><channel>` + strings.Repeat("<!--"+strings.Repeat("x", 200)+"-->", 50_000) + `</channel></rss>`)
	require.Greater(t, len(body), 9<<20)
	before := allocated()
	require.NoError(t, checkNesting(body))
	require.Less(t, allocated()-before, uint64(2<<20))
}

func parseErr(body string) error {
	_, err := ParseFeed([]byte(body), ParseOptions{FeedURL: "https://example.com/feed"})
	return err
}

// allocated is the total bytes allocated so far; it only grows.
func allocated() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}

// An instruction inside the root element is not rewritten by DecodeBody, and the parse refuses it: honouring it would
// convert bytes that are already converted, and each one wraps one more converter round the rest of the document.
func TestRefusesCharsetSwitchInsideTheDocument(t *testing.T) {
	pis := strings.Repeat(`<?xml encoding="windows-1252"?>`, 20)
	text := "<title>" + strings.Repeat("é", 64) + "</title></channel></rss>"
	for name, body := range map[string]string{
		"a stack":    `<rss version="2.0"><channel>` + pis + text,
		"one switch": `<rss version="2.0"><channel><?xml encoding="windows-1252"?>` + text,
	} {
		before := allocated()
		err := parseErr(body)
		require.ErrorIs(t, err, errCharsetSwitch, name)
		require.Less(t, allocated()-before, uint64(8<<20), name)
	}
}

// Every instruction before the root element is rewritten to utf-8 by DecodeBody, as the first one is, so none of them
// converts anything: the document parses, its text unchanged, however many there are.
func TestCharsetInstructionsBeforeTheRootAreRewritten(t *testing.T) {
	pis := strings.Repeat(`<?xml encoding="windows-1252"?>`, 20)
	rss := `<rss version="2.0"><channel><title>` + strings.Repeat("é", 64) + `</title></channel></rss>`
	for name, body := range map[string]string{
		"a stack before the root":    pis + rss,
		"after a declaration":        `<?xml version="1.0" encoding="utf-8"?>` + pis + rss,
		"a comment before it":        "<!-- generated -->\n" + `<?xml version="1.0" encoding="ISO-8859-1"?>` + rss,
		"a doubled header":           `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<?xml version="1.0" encoding="ISO-8859-1"?>` + rss,
		"a BOM after whitespace":     "\r\n\xef\xbb\xbf" + `<?xml version="1.0" encoding="ISO-8859-1"?>` + rss,
		"a second label spelled out": `<?xml version="1.0" encoding="utf-8"?><?xml version="1.0" encoding="utf8"?>` + rss,
	} {
		before := allocated()
		f, err := ParseFeed([]byte(body), ParseOptions{FeedURL: "https://example.com/feed"})
		require.NoError(t, err, name)
		require.Equal(t, strings.Repeat("é", 64), f.Title, name)
		require.Less(t, allocated()-before, uint64(8<<20), name)
	}
	// A leading declaration is rewritten by DecodeBody and is not a switch.
	require.NoError(t, parseErr(`<?xml version="1.0" encoding="windows-1252"?><rss version="2.0"><channel><title>caf`+"\xe9"+`</title></channel></rss>`))
	require.NoError(t, parseErr(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>t</title></channel></rss>`))
}

// A processing instruction of another target, or an encoding that is not quoted, is not a declaration.
func TestRewritePrologLeavesOtherInstructionsAlone(t *testing.T) {
	for _, in := range []string{
		`<?xml-stylesheet encoding="x"?><rss/>`,
		`<?xml version="1.0" encoding=latin1?><rss/>`,
		`<?xml version="1.0"?><rss/>`,
		`<?xml version="1.0" encoding="utf-8"?><rss/>`,
		`<rss><?xml encoding="x"?></rss>`,
	} {
		require.Equal(t, in, string(rewritePrologCopy(in)), in)
	}
	require.Equal(t, `<!-- a --><?xml encoding="utf-8"?><rss/>`, string(rewritePrologCopy(`<!-- a --><?xml encoding="latin1"?><rss/>`)))
}

func rewritePrologCopy(s string) []byte { return rewriteProlog([]byte(s)) }

// Values inside the arrays of a JSON Feed item cost memory like entries do, and they are not entries.
func TestRefusesJSONFeedWithTooManyArrayValues(t *testing.T) {
	doc := func(field string, n int) string {
		return `{"version":"https://jsonfeed.org/version/1.1","title":"t","items":[{"id":"1","` + field + `":[` + strings.Repeat("{},", n-1) + `{}]}]}`
	}
	for _, field := range []string{"attachments", "authors"} {
		err := parseErr(doc(field, MaxElements+1))
		require.Error(t, err, field)
		require.Contains(t, err.Error(), "array values", field)
		require.NoError(t, parseErr(doc(field, 1000)), field)
	}
	tags := `{"version":"https://jsonfeed.org/version/1.1","title":"t","items":[{"id":"1","tags":[` + strings.Repeat(`"a",`, MaxElements) + `"a"]}]}`
	require.Error(t, parseErr(tags))
}

func TestRefusesDocumentWithTooManyAttributes(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<rss version="2.0" xmlns:a="u"><channel><title>t</title>`)
	for range MaxAttributes/1000 + 1 {
		b.WriteString("<a:e")
		for i := range 1000 {
			fmt.Fprintf(&b, ` k%d="v"`, i)
		}
		b.WriteString("/>")
	}
	b.WriteString("</channel></rss>")
	err := parseErr(b.String())
	require.Error(t, err)
	require.Contains(t, err.Error(), "attributes")
}

func TestRefusesNamespaceFlood(t *testing.T) {
	doc := func(decls, depth int) string {
		var b strings.Builder
		b.WriteString(`<rss version="2.0"><channel`)
		for i := range decls {
			fmt.Fprintf(&b, ` xmlns:a%05d="u"`, i)
		}
		b.WriteString("><title>t</title>" + strings.Repeat("<x>", depth) + strings.Repeat("</x>", depth) + "</channel></rss>")
		return b.String()
	}
	err := parseErr(doc(6500, 500))
	require.Error(t, err)
	require.Contains(t, err.Error(), "namespace")
	require.NoError(t, parseErr(doc(MaxNamespaces, 400)))
	// Declarations spread over nested elements count where they are in scope.
	var b strings.Builder
	b.WriteString(`<rss version="2.0"><channel>`)
	for i := range MaxNamespaces + 1 {
		fmt.Fprintf(&b, `<x xmlns:a%d="u">`, i)
	}
	b.WriteString(strings.Repeat("</x>", MaxNamespaces+1) + "</channel></rss>")
	err = parseErr(b.String())
	require.Error(t, err)
	require.Contains(t, err.Error(), "namespace")
}

func TestRefusesDocumentWithTooManyEntries(t *testing.T) {
	rss := func(n int) string {
		return `<rss version="2.0"><channel><title>t</title>` + strings.Repeat("<item/>", n) + `</channel></rss>`
	}
	atom := func(n int) string {
		return `<feed xmlns="http://www.w3.org/2005/Atom"><title>t</title>` + strings.Repeat("<entry/>", n) + `</feed>`
	}
	js := func(n int) string {
		return `{"version":"https://jsonfeed.org/version/1","title":"t","items":[` + strings.Repeat("{},", n-1) + `{}]}`
	}
	for name, mk := range map[string]func(int) string{"rss": rss, "atom": atom, "json": js} {
		err := parseErr(mk(MaxDocumentEntries + 1))
		require.Error(t, err, name)
		require.Contains(t, err.Error(), "entries", name)
		f, err := ParseFeed([]byte(mk(MaxDocumentEntries)), ParseOptions{FeedURL: "https://example.com/feed"})
		require.NoError(t, err, name)
		require.LessOrEqual(t, len(f.Items), MaxItemsPerFetch, name)
	}
	// A repeated "items" key: the longest one counts.
	err := parseErr(`{"version":"https://jsonfeed.org/version/1","items":[` + strings.Repeat("{},", MaxDocumentEntries) + `{}],"items":[{}]}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "entries")
}

func TestRefusesDocumentWithTooManyElements(t *testing.T) {
	err := parseErr(`<rss version="2.0" xmlns:a="u"><channel><title>t</title>` + strings.Repeat("<a:b/>", MaxElements) + `</channel></rss>`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "elements")
}

// A legacy charset grows by up to half once converted: the cap on the converted body is twice the response limit, and
// every byte below 0x80 or in a three-byte character is within it.
func TestRefusesBodyOverTheCapOnceDecoded(t *testing.T) {
	head, tail := `<?xml version="1.0" encoding="windows-1252"?><rss version="2.0"><channel><title>`, `</title></channel></rss>`
	body := head + strings.Repeat("\x80", maxBodyBytes-len(head)-len(tail)) + tail // each byte becomes three
	require.Len(t, body, maxBodyBytes)
	require.ErrorIs(t, parseErr(body), ErrDecodedTooLarge)
	body = head + strings.Repeat("\xe9", maxBodyBytes-len(head)-len(tail)) + tail // each byte becomes two
	require.NoError(t, parseErr(body))
}

// Fetch applies the same cap to the response limit it enforces, so a small limit refuses a small body.
func TestFetchRefusesBodyOverTheCapOnceDecoded(t *testing.T) {
	body := `<?xml version="1.0" encoding="windows-1252"?><rss version="2.0"><channel><title>` + strings.Repeat("\x80", 800) + `</title></channel></rss>`
	srv, _ := feedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(body))
	})
	require.LessOrEqual(t, len(body), 1024)
	res := doFetch(t, NewClient(ClientOptions{MaxResponseBody: 1024}), snapFor(srv.URL))
	require.Equal(t, ClassTooLarge, res.ErrClass, res.ErrMsg)
	res = doFetch(t, NewClient(ClientOptions{MaxResponseBody: 2048}), snapFor(srv.URL))
	require.NotEqual(t, ClassTooLarge, res.ErrClass, res.ErrMsg)
}
