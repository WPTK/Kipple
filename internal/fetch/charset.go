package fetch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Decoded is a feed body converted to UTF-8.
type Decoded struct {
	Body     []byte // UTF-8, BOM stripped, XML declaration rewritten to utf-8
	Source   string // charset the bytes were decoded from (canonical name)
	Repaired bool   // the declared/HTTP charset was overridden because it lied
	BodyHash string // hex sha256 of Body (feeds.body_hash)
}

var xmlDeclRe = regexp.MustCompile(`(?is)\A\s*<\?xml\b[^>]*?\bencoding\s*=\s*(["'])([^"']+)(["'])`)

// DecodeBody implements design §4.4 "Charset": gofeed only honours the XML
// declaration, so we decode to UTF-8 ourselves and hand it clean bytes.
//
//  1. A BOM wins.
//  2. Else the XML declaration's encoding, else the HTTP charset, else UTF-8.
//  3. If UTF-8 was chosen but the bytes are not valid UTF-8, retry with the
//     HTTP charset, then windows-1252.
//  4. Decode, rewrite encoding= to utf-8, sha256 the decoded bytes.
//
// One addition to the design (reported in the step notes): a declared
// windows-1252/ISO-8859-1/ASCII body that is in fact valid UTF-8 with
// multi-byte sequences is decoded as UTF-8. That is the common "declares
// latin1, sends UTF-8" lie, and honouring the label would produce mojibake.
func DecodeBody(body []byte, httpCharset string) Decoded {
	var (
		enc      encoding.Encoding
		name     string
		repaired bool
	)

	switch {
	case bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}):
		body = body[3:]
		enc, name = encoding.Nop, "utf-8"
	case bytes.HasPrefix(body, []byte{0xFF, 0xFE}):
		enc, name = unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM), "utf-16le"
		body = body[2:]
	case bytes.HasPrefix(body, []byte{0xFE, 0xFF}):
		enc, name = unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM), "utf-16be"
		body = body[2:]
	}

	if enc == nil {
		head := body
		if len(head) > 1024 {
			head = head[:1024]
		}
		declared := ""
		if m := xmlDeclRe.FindSubmatch(head); m != nil {
			declared = string(m[2])
		}
		enc, name = lookup(declared)
		if enc == nil {
			enc, name = lookup(httpCharset)
		}
		if enc == nil {
			enc, name = encoding.Nop, "utf-8"
		}

		if name == "utf-8" {
			if !utf8.Valid(body) {
				repaired = true
				if e, n := lookup(httpCharset); e != nil && n != "utf-8" {
					enc, name = e, n
				} else {
					enc, name = lookup("windows-1252")
				}
			}
		} else if name == "windows-1252" && utf8.Valid(body) && hasMultiByte(body) {
			enc, name, repaired = encoding.Nop, "utf-8", true
		}
	}

	out := body
	if enc != encoding.Nop {
		if dec, _, err := transform.Bytes(enc.NewDecoder(), body); err == nil {
			out = dec
		}
	}
	out = rewriteDecl(out)

	sum := sha256.Sum256(out)
	return Decoded{Body: out, Source: name, Repaired: repaired, BodyHash: hex.EncodeToString(sum[:])}
}

// lookup maps a label to an encoding and its canonical name; (nil, "") when
// the label is empty or unknown.
func lookup(label string) (encoding.Encoding, string) {
	if label == "" {
		return nil, ""
	}
	e, n := charset.Lookup(label)
	if e == nil {
		return nil, ""
	}
	if n == "utf-8" {
		return encoding.Nop, n
	}
	return e, n
}

func hasMultiByte(b []byte) bool {
	for _, c := range b {
		if c >= 0xC2 {
			return true
		}
	}
	return false
}

// rewriteDecl replaces the declared encoding with utf-8 so gofeed's XML
// decoder does not try to convert an already-converted body again.
func rewriteDecl(b []byte) []byte {
	loc := xmlDeclRe.FindSubmatchIndex(b)
	if loc == nil {
		return b
	}
	// loc[4]:loc[5] is the encoding value.
	out := make([]byte, 0, len(b))
	out = append(out, b[:loc[4]]...)
	out = append(out, "utf-8"...)
	out = append(out, b[loc[5]:]...)
	return out
}
