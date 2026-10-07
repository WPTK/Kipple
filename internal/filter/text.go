package filter

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// truncate cuts s to at most n bytes at a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// normalize prepares text (or a term) for text matching: optionally NFKD with combining marks
// dropped, optionally lower-cased, and every run of whitespace collapsed to one space. It never
// trims: callers trim terms themselves. Haystack and terms go through the same function, so the
// two sides agree by construction.
func normalize(s string, fold, lower bool) string {
	if fold && !isASCII(s) {
		s = norm.NFKD.String(s)
	}
	// Fast path: nothing to change.
	clean := true
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= utf8.RuneSelf || (lower && c >= 'A' && c <= 'Z') {
			clean = false
			break
		}
		sp := c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
		if sp && (c != ' ' || prevSpace) {
			clean = false
			break
		}
		prevSpace = sp
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace = false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		if r == 0x2019 || r == 0x02BC {
			// Typographic apostrophes match the plain one, so a term typed with either finds titles using the other.
			r = 0x27
		}
		if fold && unicode.Is(unicode.Mn, r) {
			continue
		}
		if lower {
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isWordRune is what makes up a word: letters, digits and the combining marks that belong to
// them (marks survive only when FoldDiacritics is off). Runes of scripts written without spaces
// (Han, Kana, Thai, ...) are not word runes: each is its own boundary, so a CJK term matches as a
// substring and a Latin word glued to CJK text ("新iPhone发布") still matches as a whole word.
func isWordRune(r rune) bool {
	return (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)) && !unicode.IsOneOf(unspacedScripts, r)
}

var unspacedScripts = []*unicode.RangeTable{
	unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar,
}

// tokenEligible reports whether a normalized term can be answered by a word-set lookup: one
// token, every rune a word rune.
func tokenEligible(term string) bool {
	if term == "" {
		return false
	}
	for _, r := range term {
		if !isWordRune(r) {
			return false
		}
	}
	return true
}

// words splits normalized text into its maximal runs of word runes.
func words(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return !isWordRune(r) })
}

// containsTerm reports whether normalized text contains normalized term, honoring whole-word
// boundaries when whole is set. It is the reference implementation: the word-set fast path must
// agree with it (a fuzz target checks that).
func containsTerm(text, term string, whole bool) bool {
	if term == "" {
		return false
	}
	if !whole {
		return strings.Contains(text, term)
	}
	first, _ := utf8.DecodeRuneInString(term)
	last, _ := utf8.DecodeLastRuneInString(term)
	checkL, checkR := isWordRune(first), isWordRune(last)
	from := 0
	for from <= len(text)-len(term) {
		i := strings.Index(text[from:], term)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(term)
		ok := true
		if checkL && i > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:i])
			ok = !isWordRune(r)
		}
		if ok && checkR && end < len(text) {
			r, _ := utf8.DecodeRuneInString(text[end:])
			ok = !isWordRune(r)
		}
		if ok {
			return true
		}
		_, w := utf8.DecodeRuneInString(text[i:])
		from = i + w
	}
	return false
}
