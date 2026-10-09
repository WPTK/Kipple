package fetch

import (
	"net/http"
	"strings"
)

// UnaskedCoding reports whether a response still names a Content-Encoding after the transport has done its work.
// The transport asks for gzip and decodes it, removing the header; any coding left is one nobody asked for
// (br, deflate, zstd), and its bytes are not the text the reader expects. A junk value ("none", "utf-8") on a plain
// body is left alone.
func UnaskedCoding(h http.Header) bool {
	for _, v := range h.Values("Content-Encoding") {
		for _, c := range strings.Split(strings.ToLower(v), ",") {
			switch strings.TrimSpace(c) {
			case "gzip", "x-gzip", "deflate", "br", "zstd", "compress", "x-compress":
				return true
			}
		}
	}
	return false
}
