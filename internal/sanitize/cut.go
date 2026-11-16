package sanitize

import (
	"strings"
	"unicode/utf8"
)

// MaxItemHTMLBytes is the most HTML one feed item, or one extracted page's article, hands to Content. The cleaner holds
// many times the size of what it reads (20 to 30 times for markup built to make it), so an item of the 10 MiB a feed
// document may have needs more memory than a small server has. A real article is far below it.
const MaxItemHTMLBytes = 2 << 20

// maxRefBytes is the longest character reference a cut waits for: the longest named one ("&CounterClockwiseContourIntegral;")
// is 33 bytes.
const maxRefBytes = 34

// CutHTML cuts s to at most max bytes without splitting a rune, a tag, a comment or a character reference, and reports
// whether it cut anything. It reads s from the start the way a parser does: a '<' opens a tag only when a letter (or a
// '/' and a letter, '!' or '?') follows it, a '>' inside a quoted attribute value does not close the tag, and a comment
// ends at "-->". A cut inside any of these moves back to where it starts. A '<' or an '&' in text that does not begin
// markup or a reference ("a < b", "AT&T ") costs nothing. A cut in text right after the start of a reference (an '&'
// and at most maxRefBytes letters, digits or '#') moves back to the '&'.
func CutHTML(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if start, inside := markupAt(s, cut); inside {
		return s[:start], true
	}
	return s[:refStart(s, cut)], true
}

// markupAt reports whether position cut of s falls inside a tag, a comment or another '<!' or '<?' construct, and
// where that construct starts.
func markupAt(s string, cut int) (start int, inside bool) {
	for i := 0; i < cut; i++ {
		if s[i] != '<' || i+1 >= len(s) {
			continue
		}
		c := s[i+1]
		var end int
		switch {
		case isLetter(c):
			end = tagEnd(s, i+2)
		case c == '/' && i+2 < len(s) && isLetter(s[i+2]):
			end = tagEnd(s, i+3)
		case c == '!' && strings.HasPrefix(s[i:], "<!--"):
			if n := strings.Index(s[i+4:], "-->"); n >= 0 {
				end = i + 4 + n + 3
			} else {
				end = -1
			}
		case c == '!' || c == '?':
			if n := strings.IndexByte(s[i+2:], '>'); n >= 0 {
				end = i + 2 + n + 1
			} else {
				end = -1
			}
		default:
			continue // a literal '<' in text
		}
		if end < 0 || end > cut {
			return i, true
		}
		i = end - 1
	}
	return 0, false
}

// tagEnd is the index after the '>' that ends the tag whose name starts before from, or -1 when s ends first. A quote
// opens a value only after an '=', as in HTML, and a '>' inside one is part of the value.
func tagEnd(s string, from int) int {
	var quote byte
	for j := from; j < len(s); j++ {
		c := s[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '>':
			return j + 1
		case c == '=':
			k := j + 1
			for k < len(s) && isSpace(s[k]) {
				k++
			}
			if k < len(s) && (s[k] == '"' || s[k] == '\'') {
				quote, j = s[k], k
			}
		}
	}
	return -1
}

// refStart is where a cut in text at cut should fall: at the '&' of a reference that the cut would end inside, else
// at cut.
func refStart(s string, cut int) int {
	for j := cut - 1; j >= 0 && cut-j <= maxRefBytes; j-- {
		c := s[j]
		if c == '&' {
			return j
		}
		if !isLetter(c) && (c < '0' || c > '9') && c != '#' {
			break
		}
	}
	return cut
}

func isLetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }
