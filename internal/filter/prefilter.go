package filter

import (
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Go's regexp has no DFA: an unanchored search costs tens of nanoseconds per byte, and every
// case-insensitive pattern (the default here) loses the literal-prefix shortcut. So each regex
// carries a prefilter: a set of literals such that every match must contain at least one of them.
// A haystack containing none of them cannot match, and that is decided with strings.Contains
// (SIMD) instead of the regex machine. The prefilter is sound by construction (it only ever says
// "cannot match"), and a test compares filtered and unfiltered results on random inputs.

type reqLit struct {
	s    string // canonical form when fold is set
	fold bool   // compare case-insensitively (in regexp's own simple-fold sense)
}

type cre struct {
	re  *regexp.Regexp
	req []reqLit // nil: no usable prefilter
}

const maxReqLits = 16

// canonRune maps r to the smallest rune of its simple-fold orbit, the same equivalence a (?i)
// literal uses in package regexp.
func canonRune(r rune) rune {
	m := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < m {
			m = f
		}
	}
	return m
}

func canon(s string) string {
	if isASCII(s) {
		var b []byte
		for i := 0; i < len(s); i++ {
			if c := s[i]; c >= 'a' && c <= 'z' {
				if b == nil {
					b = []byte(s)
				}
				b[i] = c - 'a' + 'A'
			}
		}
		if b == nil {
			return s
		}
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		sb.WriteRune(canonRune(r))
	}
	return sb.String()
}

// requiredLits returns literals of which every match of re contains at least one. ok is false when
// no such small set is known.
func requiredLits(re *syntax.Regexp) (lits []reqLit, ok bool) {
	switch re.Op {
	case syntax.OpLiteral:
		if len(re.Rune) == 0 {
			return nil, false
		}
		s := string(re.Rune)
		if re.Flags&syntax.FoldCase != 0 {
			return []reqLit{{s: canon(s), fold: true}}, true
		}
		return []reqLit{{s: s}}, true
	case syntax.OpCapture, syntax.OpPlus:
		return requiredLits(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return requiredLits(re.Sub[0])
		}
	case syntax.OpConcat:
		var best []reqLit
		bestScore := 0
		for _, sub := range re.Sub {
			l, ok := requiredLits(sub)
			if !ok {
				continue
			}
			score := 1 << 30
			for _, x := range l {
				score = min(score, utf8.RuneCountInString(x.s))
			}
			score = score*4 - len(l) // longer is better; fewer alternatives is better
			if best == nil || score > bestScore {
				best, bestScore = l, score
			}
		}
		return best, best != nil
	case syntax.OpAlternate:
		var all []reqLit
		for _, sub := range re.Sub {
			l, ok := requiredLits(sub)
			if !ok {
				return nil, false
			}
			all = append(all, l...)
			if len(all) > maxReqLits {
				return nil, false
			}
		}
		return all, len(all) > 0
	}
	return nil, false
}

// newCre compiles a regex (already validated) with its prefilter.
func newCre(re *regexp.Regexp, parsed *syntax.Regexp) cre {
	c := cre{re: re}
	if lits, ok := requiredLits(parsed.Simplify()); ok {
		c.req = lits
	}
	return c
}
