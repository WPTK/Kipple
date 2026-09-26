package store

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Search query builder v2 (design §2.4). User text is parsed into terms and rendered as an FTS5
// MATCH expression that is built only from double-quoted phrases, parentheses, the operators OR
// and NOT, and the two whitelisted column filters. Every word from the user reaches FTS5 inside a
// quoted phrase, where no operator, column filter, NEAR, '^' or '*' has any meaning, so user text
// can never produce an FTS syntax error or an unlisted column filter.
//
// Grammar (whitespace separated, left to right):
//
//	word            one word, stemmed by the porter tokenizer on both sides
//	"a phrase"      the words in order, adjacent
//	-word  -"a b"   exclusion; also NOT word (upper case NOT), only with at least one positive term
//	title:word      column filter; title: and author: only, any other "x:y" stays literal text
//	author:"j doe"  filters combine with quotes and exclusion (-title:word)
//	word*           prefix search
//
// The last bare word (at least 3 runes, 2 for CJK) is a prefix when the text does not end in a space
// (search-as-you-type). A prefix is rendered ("w" OR "w"*): FTS5 does not stem a prefix, so
// "running*" alone would miss the indexed stem "run".
//
// CJK: the unicode61 tokenizer does not segment Han/Kana/Hangul text, so a run of such characters
// is one token and only matches the same whole run or a prefix of it. Documented limit; there is
// no trigram index.
const (
	maxSearchTerms    = 16
	maxSearchTokens   = maxSearchTerms // kept for the old name
	maxSearchTokenLen = 64             // runes
	maxSearchInput    = 512            // bytes
	minImplicitPrefix = 3              // runes
)

// searchTerm is one parsed unit of user text.
type searchTerm struct {
	col    string   // "", "title" or "author"
	neg    bool     // exclusion
	words  []string // one word, or several for a phrase
	phrase bool
	prefix bool // bare word searched as a prefix too
}

// SearchQuery is parsed user search text.
type SearchQuery struct {
	terms []searchTerm
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// hasCJK reports Han, Kana or Hangul: one such character carries about a word, so two are enough
// for the search-as-you-type prefix.
func hasCJK(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
	})
}

func hasWordRune(s string) bool { return strings.ContainsFunc(s, isWordRune) }

func cleanWord(w string) string {
	if r := []rune(w); len(r) > maxSearchTokenLen {
		w = string(r[:maxSearchTokenLen])
	}
	return w
}

// ParseSearch parses raw user text. It never fails: whatever is not searchable is dropped.
func ParseSearch(raw string) SearchQuery {
	if len(raw) > maxSearchInput {
		raw = raw[:maxSearchInput]
	}
	src := []rune(strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, raw))
	endsOpen := len(src) > 0 && !unicode.IsSpace(src[len(src)-1])

	var sq SearchQuery
	pendingNot := false
	lastWasBare := false // the most recent lexed term is a positive bare word
	for i := 0; i < len(src) && len(sq.terms) < maxSearchTerms; {
		if unicode.IsSpace(src[i]) {
			i++
			continue
		}
		var t searchTerm
		if src[i] == '-' && i+1 < len(src) && !unicode.IsSpace(src[i+1]) {
			t.neg = true
			i++
		}
		if col, n := columnPrefix(src[i:]); n > 0 {
			t.col = col
			i += n
		}
		if i < len(src) && src[i] == '"' {
			j := i + 1
			for j < len(src) && src[j] != '"' {
				j++
			}
			for _, w := range strings.Fields(string(src[i+1 : j])) {
				if hasWordRune(w) && len(t.words) < maxSearchTerms {
					t.words = append(t.words, cleanWord(strings.ReplaceAll(w, `"`, "")))
				}
			}
			t.phrase = true
			i = j + 1
		} else {
			j := i
			for j < len(src) && !unicode.IsSpace(src[j]) {
				j++
			}
			tok := string(src[i:j])
			i = j
			stars := len(tok) - len(strings.TrimRight(tok, "*"))
			tok = strings.TrimRight(tok, "*")
			if hasWordRune(tok) {
				t.words = []string{cleanWord(tok)}
				t.prefix = stars > 0
			}
		}
		if len(t.words) == 0 {
			lastWasBare = false
			continue // nothing searchable (punctuation only, empty quotes)
		}
		if !t.neg && !t.phrase && len(t.words) == 1 && t.col == "" && t.words[0] == "NOT" && !t.prefix {
			pendingNot = true // NOT word: exclusion; a trailing NOT stays unused
			lastWasBare = false
			continue
		}
		if pendingNot {
			t.neg, pendingNot = true, false
		}
		lastWasBare = !t.neg && !t.phrase
		sq.terms = append(sq.terms, t)
	}
	// A trailing NOT with nothing after it was dropped above; search-as-you-type prefix:
	if lastWasBare && endsOpen && len(sq.terms) > 0 {
		last := &sq.terms[len(sq.terms)-1]
		if n := utf8.RuneCountInString(last.words[0]); n >= minImplicitPrefix || (n >= 2 && hasCJK(last.words[0])) {
			last.prefix = true
		}
	}
	return sq
}

// columnPrefix reports the whitelisted column named by a leading "title:" or "author:" followed
// by something to filter, and the number of runes to skip.
func columnPrefix(s []rune) (string, int) {
	for _, c := range [...]string{"title", "author"} {
		n := len(c)
		if len(s) > n+1 && s[n] == ':' && !unicode.IsSpace(s[n+1]) && strings.EqualFold(string(s[:n]), c) {
			return c, n + 1
		}
	}
	return "", 0
}

func quote(w string) string { return `"` + strings.ReplaceAll(w, `"`, `""`) + `"` }

func (t searchTerm) phraseText() string {
	q := make([]string, len(t.words))
	copy(q, t.words)
	return quote(strings.Join(q, " "))
}

// expr renders the term without its column filter or exclusion.
func (t searchTerm) expr(forcePrefix bool) string {
	if t.phrase {
		return t.phraseText()
	}
	w := quote(t.words[0])
	if t.prefix || forcePrefix {
		return "(" + w + " OR " + w + "*)"
	}
	return w
}

func (t searchTerm) render(forcePrefix bool) string {
	e := t.expr(forcePrefix)
	if t.col != "" {
		e = t.col + " : " + e
	}
	return e
}

func (sq SearchQuery) parts() (pos, neg []searchTerm) {
	for _, t := range sq.terms {
		if t.neg {
			neg = append(neg, t)
		} else {
			pos = append(pos, t)
		}
	}
	return pos, neg
}

func joinNeg(base string, neg []searchTerm) string {
	if len(neg) == 0 {
		return base
	}
	parts := make([]string, len(neg))
	for i, t := range neg {
		parts[i] = t.render(false)
	}
	return "(" + base + ") NOT (" + strings.Join(parts, " OR ") + ")"
}

// Match is the exact-mode MATCH expression: every positive term must match (implicit AND).
// ok is false when no positive term remains (exclusions alone match nothing).
func (sq SearchQuery) Match() (match string, ok bool) {
	pos, neg := sq.parts()
	if len(pos) == 0 {
		return "", false
	}
	parts := make([]string, len(pos))
	for i, t := range pos {
		parts[i] = t.render(false)
	}
	return joinNeg(strings.Join(parts, " AND "), neg), true
}

// FallbackMatch is the partial-match expression used when the exact one finds nothing: every
// positive word, phrases split into their words, becomes a prefix and they are ORed; exclusions
// and column filters are kept. Empty when there is nothing positive.
func (sq SearchQuery) FallbackMatch() string {
	pos, neg := sq.parts()
	var parts []string
	for _, t := range pos {
		for _, w := range t.words {
			if len(parts) == maxSearchTerms {
				break
			}
			parts = append(parts, searchTerm{col: t.col, words: []string{w}}.render(true))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return joinNeg(strings.Join(parts, " OR "), neg)
}

// BuildFTSQuery is the exact-mode expression for raw user text (see SearchQuery.Match).
func BuildFTSQuery(raw string) (match string, ok bool) { return ParseSearch(raw).Match() }
