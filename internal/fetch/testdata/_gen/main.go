//go:build ignore

// Regenerates the binary charset fixtures: go run ./internal/fetch/testdata/_gen
package main

import (
	"os"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/unicode"
)

func doc(decl, title, body string) string {
	return `<?xml version="1.0"` + decl + `?>
<rss version="2.0"><channel><title>` + title + `</title><link>https://cs.example.com/</link><description>d</description>
<item><title>` + body + `</title><link>https://cs.example.com/1</link><guid>1</guid><pubDate>Mon, 21 Sep 2026 10:00:00 GMT</pubDate><description>` + body + `</description></item>
</channel></rss>
`
}

func write(name string, e encoding.Encoding, s string) {
	b := []byte(s)
	if e != nil {
		var err error
		if b, err = e.NewEncoder().Bytes(b); err != nil {
			panic(err)
		}
	}
	if err := os.WriteFile("internal/fetch/testdata/"+name, b, 0o644); err != nil {
		panic(err)
	}
}

func main() {
	const latin = "Café ‘quoted’ – naïve €5"
	// Declares iso-8859-1 (really windows-1252: curly quotes, en dash, euro).
	write("charset-cp1252.xml", charmap.Windows1252, doc(` encoding="iso-8859-1"`, "Café feed", latin))
	// Declares iso-8859-1 but the bytes are UTF-8.
	write("charset-utf8-labelled-latin1.xml", nil, doc(` encoding="iso-8859-1"`, "Café feed", latin))
	// Declares utf-8 but the bytes are windows-1252; HTTP header names the truth.
	write("charset-cp1252-labelled-utf8.xml", charmap.Windows1252, strings.Replace(doc(` encoding="utf-8"`, "Café feed", latin), "x", "x", 1))
	// No declaration at all, invalid UTF-8, no HTTP charset: windows-1252 fallback.
	write("charset-undeclared-cp1252.xml", charmap.Windows1252, strings.Replace(doc("", "Café feed", latin), `<?xml version="1.0"?>`, `<?xml version="1.0"?>`, 1))
	// Shift_JIS declared correctly.
	write("charset-shiftjis.xml", japanese.ShiftJIS, doc(` encoding="Shift_JIS"`, "日本語のフィード", "こんにちは世界"))
	// UTF-16LE with BOM, declaration says utf-16.
	le := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM)
	write("charset-utf16le-bom.xml", le, doc(` encoding="utf-16"`, "Café feed", latin))
	// UTF-8 with BOM.
	write("charset-utf8-bom.xml", nil, "\xef\xbb\xbf"+doc(` encoding="utf-8"`, "Café feed", latin))
}
