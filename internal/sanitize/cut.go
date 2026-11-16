package sanitize

import (
	"strings"
	"unicode/utf8"
)

// MaxItemHTMLBytes is the most HTML one feed item, or one extracted page's article, hands to Content. The cleaner holds
// many times the size of what it reads (20 to 30 times for markup built to make it), so an item of the 10 MiB a feed
// document may have needs more memory than a small server has. A real article is far below it.
const MaxItemHTMLBytes = 2 << 20

// CutHTML cuts s to at most max bytes without splitting a rune, a tag or a character reference, and reports whether
// it cut anything. A literal '<' after the last '>' means the cut fell inside a tag (in cleaned output a literal '<'
// only opens a tag and a literal '>' only closes one, as attribute values and text are escaped): the cut moves back to
// that '<'. An '&' after the last ';' and the last '>' is a reference the cut would end inside: the cut moves back to
// it. On markup that was not cleaned, an attribute value holding a '>' can make the cut land a little earlier or leave
// half a tag at the end, which the cleaner drops.
func CutHTML(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if open := strings.LastIndexByte(s[:cut], '<'); open > strings.LastIndexByte(s[:cut], '>') {
		cut = open
	}
	// A cut in text does not end inside a character reference ("&am").
	if amp := strings.LastIndexByte(s[:cut], '&'); amp > strings.LastIndexByte(s[:cut], ';') && amp > strings.LastIndexByte(s[:cut], '>') {
		cut = amp
	}
	return s[:cut], true
}
