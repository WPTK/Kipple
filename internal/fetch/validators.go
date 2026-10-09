package fetch

import "strings"

const (
	// maxETagLen bounds a stored entity tag; real ones are a few dozen bytes.
	maxETagLen = 1024
	// maxLastModifiedLen bounds a stored Last-Modified; an HTTP date is under 40 bytes.
	maxLastModifiedLen = 64
)

// CleanValidators returns the cache validators of a response in the form they
// are stored and sent back (If-None-Match, If-Modified-Since), or "" for one
// that cannot be. Both are opaque strings that the server compares with what it
// sent, so a kept value is never rewritten: an entity tag is printable ASCII
// without spaces (unquoted tags are kept, since servers send them and sending
// one back is harmless), and a Last-Modified is printable ASCII, so a date in
// any spelling a server uses is kept as is. Both are bounded in length. A fetch
// that gets nothing usable stores nothing.
func CleanValidators(etag, lastModified string) (string, string) {
	if len(etag) > maxETagLen || strings.ContainsFunc(etag, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		etag = ""
	}
	if len(lastModified) > maxLastModifiedLen || strings.ContainsFunc(lastModified, func(r rune) bool { return r < 0x20 || r > 0x7e }) {
		lastModified = ""
	}
	return etag, lastModified
}
