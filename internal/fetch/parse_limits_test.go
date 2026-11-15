package fetch

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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

func TestRefusesCharsetSwitchAfterDecoding(t *testing.T) {
	pis := strings.Repeat(`<?xml encoding="windows-1252"?>`, 20)
	text := "<title>" + strings.Repeat("é", 64) + "</title></channel></rss>"
	for name, body := range map[string]string{
		"inside the document": `<rss version="2.0"><channel>` + pis + text,
		"before the root":     pis + `<rss version="2.0"><channel>` + text,
		"after a declaration": `<?xml version="1.0" encoding="utf-8"?>` + pis + `<rss version="2.0"><channel>` + text,
		"one switch":          `<rss version="2.0"><channel><?xml encoding="windows-1252"?>` + text,
	} {
		before := allocated()
		err := parseErr(body)
		require.Error(t, err, name)
		require.Contains(t, err.Error(), "charset", name)
		require.Less(t, allocated()-before, uint64(8<<20), name)
	}
	// A leading declaration is rewritten by DecodeBody and is not a switch.
	require.NoError(t, parseErr(`<?xml version="1.0" encoding="windows-1252"?><rss version="2.0"><channel><title>caf`+"\xe9"+`</title></channel></rss>`))
	require.NoError(t, parseErr(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>t</title></channel></rss>`))
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

func TestRefusesBodyOverTheCapOnceDecoded(t *testing.T) {
	head, tail := `<?xml version="1.0" encoding="windows-1252"?><rss version="2.0"><channel><title>`, `</title></channel></rss>`
	body := head + strings.Repeat("\x80", maxBodyBytes-len(head)-len(tail)) + tail
	require.Len(t, body, maxBodyBytes)
	err := parseErr(body)
	require.Error(t, err)
	require.Contains(t, err.Error(), "large")
}
