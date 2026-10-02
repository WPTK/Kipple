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
// A prefix is rendered "w"*. FTS5's porter tokenizer also stems the prefix query (measured on the
// real library: "running"* and "run"* both return the same 988 rows), so "w"* means stem-of-w
// followed by anything: "apple"* is "appl"* and reaches application and apply. That is wider than
// the plain word ("apple" 518 rows, "apple"* 851), so a prefix is only ever used where the user
// asked for it:
//
//   - an explicit word* (at least 3 runes, 2 for CJK; shorter is a literal word);
//   - with typing set (search-as-you-type), the last bare word when the text does not end in a
//     space and the word is long enough;
//   - the partial-match fallback (FallbackMatch), and only for words of that length.
//
// Saved searches, mark-read scopes and unread counts never set typing, so they count exactly the
// stemmed words. A query has at most maxPrefixTerms prefixes and maxSearchTerms terms: each prefix
// scans every term that shares the stem, and a one- or two-letter prefix matches almost the whole
// library.
//
// CJK: the unicode61 tokenizer does not segment Han/Kana/Hangul text, so a run of such characters
// is one token and only matches the same whole run or a prefix of it. Documented limit; there is
// no trigram index.
const (
	maxSearchTerms    = 12
	maxSearchTokenLen = 64  // runes
	maxSearchInput    = 512 // bytes
	minPrefixRunes    = 3   // shortest word that may be a prefix
	minPrefixCJK      = 2   // ... for Han, Kana and Hangul
	maxPrefixTerms    = 3   // prefixes per query
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

// prefixOK reports whether w is long enough to be searched as a prefix.
func prefixOK(w string) bool {
	n := utf8.RuneCountInString(w)
	return n >= minPrefixRunes || (n >= minPrefixCJK && hasCJK(w))
}

func cleanWord(w string) string {
	if r := []rune(w); len(r) > maxSearchTokenLen {
		w = string(r[:maxSearchTokenLen])
	}
	return w
}

// ParseSearch parses raw user text. It never fails: whatever is not searchable is dropped. typing
// makes an unfinished last word (the text does not end in a space) a prefix, for
// search-as-you-type; everything else, saved searches and counts included, passes false.
func ParseSearch(raw string, typing bool) SearchQuery {
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
	prefixes := 0
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
				if stars > 0 && prefixOK(t.words[0]) && prefixes < maxPrefixTerms {
					t.prefix = true // a shorter or surplus "w*" is just the word
					prefixes++
				}
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
	if typing && lastWasBare && endsOpen && len(sq.terms) > 0 && prefixes < maxPrefixTerms {
		if last := &sq.terms[len(sq.terms)-1]; prefixOK(last.words[0]) {
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
func (t searchTerm) expr() string {
	if t.phrase {
		return t.phraseText()
	}
	w := quote(t.words[0])
	if t.prefix {
		return w + "*"
	}
	return w
}

func (t searchTerm) render() string {
	e := t.expr()
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
		parts[i] = t.render()
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
		parts[i] = t.render()
	}
	return joinNeg(strings.Join(parts, " AND "), neg), true
}

// FallbackMatch is the partial-match expression used when the exact one finds nothing: the
// positive words, phrases split into their words, are ORed and the first maxPrefixTerms of them
// become prefixes; exclusions and column filters are kept. Words too short to be a prefix
// (under 3 runes, 2 for CJK) are dropped: ORed in, a one-letter word matches most of the library
// on every keystroke. Empty when no such word remains.
func (sq SearchQuery) FallbackMatch() string {
	pos, neg := sq.parts()
	var parts []string
	prefixes := 0
	for _, t := range pos {
		for _, w := range t.words {
			if len(parts) == maxSearchTerms {
				break
			}
			if !prefixOK(w) {
				continue
			}
			ft := searchTerm{col: t.col, words: []string{w}}
			if prefixes < maxPrefixTerms {
				ft.prefix = true
				prefixes++
			}
			parts = append(parts, ft.render())
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return joinNeg(strings.Join(parts, " OR "), neg)
}

// BuildFTSQuery is the exact-mode expression for raw user text (see SearchQuery.Match).
func BuildFTSQuery(raw string, typing bool) (match string, ok bool) {
	return ParseSearch(raw, typing).Match()
}
