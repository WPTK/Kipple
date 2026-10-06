package opml

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"

	"github.com/WPTK/kipple/internal/fetch"
)

// MaxFileBytes is the largest OPML file an import accepts.
const MaxFileBytes = 8 << 20

// LooksLikeOPML reports whether head, the start of a file, is XML whose root
// element is <opml>. It reads only the first element, so head may be cut
// anywhere after it.
func LooksLikeOPML(head []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(fetch.DecodeBody(head, "").Body))
	dec.Strict = false
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return strings.EqualFold(t.Name.Local, "opml")
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return false // text before the root: not XML
			}
		}
	}
}
