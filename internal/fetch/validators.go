package fetch

import (
	"net/http"
	"strings"
)

// maxETagLen bounds a stored entity tag; real ones are a few dozen bytes.
const maxETagLen = 1024

// CleanValidators returns the cache validators of a response in the form they
// are stored and sent back (If-None-Match, If-Modified-Since), or "" for one
// that cannot be: an entity tag of printable ASCII without spaces, at most
// maxETagLen bytes (unquoted tags are kept, since servers send them and sending
// one back is harmless), and a Last-Modified date in any of the HTTP formats,
// stored in the preferred one. A fetch that gets nothing usable stores nothing.
func CleanValidators(etag, lastModified string) (string, string) {
	if len(etag) > maxETagLen || strings.ContainsFunc(etag, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		etag = ""
	}
	if lastModified != "" {
		lastModified = cleanHTTPDate(lastModified)
	}
	return etag, lastModified
}

func cleanHTTPDate(s string) string {
	if len(s) > 64 {
		return ""
	}
	t, err := http.ParseTime(s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(http.TimeFormat)
}
