package filter

import (
	"regexp"
	"regexp/syntax"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Compiled is one validated, ready-to-run rule. It is immutable and safe for concurrent use.
type Compiled struct {
	rule   Rule
	fields []Field
	// hasCategory: the rule scans the category field (an inverted one never fires on an item without categories).
	hasCategory bool
	text        *textMatcher
	res         []cre
}

type textTerm struct {
	text string
	tok  string // first word when it is a token-eligible word (phrase prefilter), else ""
}

type textMatcher struct {
	fold, lower, whole bool
	singleSet          map[string]struct{} // token-eligible single-word terms (whole word only)
	singles            []string
	others             []textTerm // phrases, punctuation terms, CJK terms, every term when not whole word
}

// Rule returns a copy of the rule this was compiled from.
func (c *Compiled) Rule() Rule {
	r := c.rule
	r.Terms = append([]string(nil), r.Terms...)
	r.Fields = append([]Field(nil), r.Fields...)
	return r
}

// CompileRule validates and compiles one rule. The error, when not nil, is an *Error.
func CompileRule(r Rule) (*Compiled, error) {
	c, err := compileRule(r)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func compileRule(r Rule) (*Compiled, *Error) {
	if e := validString("name", r.Name); e != nil {
		return nil, e
	}
	if len(r.Name) > MaxNameBytes {
		return nil, bad("name", "is longer than %d bytes", MaxNameBytes)
	}
	switch r.Scope {
	case ScopeGlobal:
		if r.FolderID != 0 || r.FeedID != 0 {
			return nil, bad("scope", "a global rule has no folder or feed")
		}
	case ScopeFolder:
		if r.FolderID <= 0 || r.FeedID != 0 {
			return nil, bad("scope", "a folder rule needs exactly a folder")
		}
	case ScopeFeed:
		if r.FeedID <= 0 || r.FolderID != 0 {
			return nil, bad("scope", "a feed rule needs exactly a feed")
		}
	default:
		return nil, bad("scope", "must be global, folder or feed")
	}
	switch r.Kind {
	case KindText, KindRegex:
	default:
		return nil, bad("kind", "must be text or regex")
	}
	switch r.Action {
	case ActionMute, ActionMarkRead, ActionStar, ActionHighlight:
	default:
		return nil, bad("action", "must be mute, mark_read, star or highlight")
	}
	if r.Action == ActionHighlight && r.Kind != KindText {
		return nil, bad("action", "highlight works with text rules only (the client's regex engine is not RE2)")
	}

	if r.Action == ActionHighlight && r.Invert {
		return nil, bad("action", "highlight can't be inverted: there is no text to mark in items that don't match")
	}

	fields := r.Fields
	if len(fields) == 0 {
		fields = []Field{FieldTitle}
	}
	seen := map[Field]bool{}
	var fs []Field
	for i, f := range fields {
		if !validField(f) {
			return nil, bad(fieldIdx("fields", i), "must be one of title, author, content, url, category, feed")
		}
		if !seen[f] {
			seen[f] = true
			fs = append(fs, f)
		}
	}

	if len(r.Terms) == 0 {
		return nil, bad("terms", "needs at least one term")
	}
	if len(r.Terms) > MaxTermsPerRule && r.Kind == KindText {
		return nil, bad("terms", "has more than %d terms", MaxTermsPerRule)
	}
	if len(r.Terms) > MaxRegexPatterns && r.Kind == KindRegex {
		return nil, bad("terms", "has more than %d patterns", MaxRegexPatterns)
	}

	c := &Compiled{rule: r, fields: fs}
	c.rule.Terms = append([]string(nil), r.Terms...)
	c.rule.Fields = fs
	c.hasCategory = slices.Contains(fs, FieldCategory)
	if r.Kind == KindRegex {
		for i, p := range r.Terms {
			re, e := compileRegex(fieldIdx("terms", i), p, r.CaseSensitive)
			if e != nil {
				return nil, e
			}
			c.res = append(c.res, re)
		}
		return c, nil
	}

	tm := &textMatcher{fold: r.FoldDiacritics, lower: !r.CaseSensitive, whole: r.WholeWord, singleSet: map[string]struct{}{}}
	dup := map[string]bool{}
	for i, t := range r.Terms {
		field := fieldIdx("terms", i)
		if e := validString(field, t); e != nil {
			return nil, e
		}
		n := utf8.RuneCountInString(t)
		if n == 0 {
			return nil, bad(field, "is empty")
		}
		if n > MaxTermRunes {
			return nil, bad(field, "is longer than %d characters", MaxTermRunes)
		}
		nt := strings.TrimSpace(normalize(t, tm.fold, tm.lower))
		if nt == "" {
			return nil, bad(field, "has nothing to match (blank once whitespace and accents are dropped)")
		}
		if dup[nt] {
			continue
		}
		dup[nt] = true
		if tm.whole && tokenEligible(nt) {
			tm.singles = append(tm.singles, nt)
			tm.singleSet[nt] = struct{}{}
			continue
		}
		tt := textTerm{text: nt}
		if tm.whole {
			if w := words(nt); len(w) > 1 && tokenEligible(w[0]) && strings.HasPrefix(nt, w[0]+" ") {
				tt.tok = w[0]
			}
		}
		tm.others = append(tm.others, tt)
	}
	c.text = tm
	return c, nil
}

func fieldIdx(name string, i int) string {
	return name + "[" + strconv.Itoa(i) + "]"
}

func compileRegex(field, p string, caseSensitive bool) (cre, *Error) {
	if e := validString(field, p); e != nil {
		return cre{}, e
	}
	if p == "" {
		return cre{}, bad(field, "is empty")
	}
	if len(p) > MaxRegexBytes {
		return cre{}, bad(field, "is longer than %d bytes", MaxRegexBytes)
	}
	full := p
	if !caseSensitive {
		full = "(?i)" + p
	}
	parsed, err := syntax.Parse(full, syntax.Perl)
	if err != nil {
		return cre{}, bad(field, "%s", regexReason(err))
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return cre{}, bad(field, "%s", regexReason(err))
	}
	if len(prog.Inst) > MaxRegexProgInsts {
		return cre{}, bad(field, "is too complex (%d instructions, the limit is %d)", len(prog.Inst), MaxRegexProgInsts)
	}
	re, err := regexp.Compile(full)
	if err != nil {
		return cre{}, bad(field, "%s", regexReason(err))
	}
	if re.MatchString("") {
		return cre{}, bad(field, "matches the empty string, so it would match every article")
	}
	return newCre(re, parsed), nil
}

func regexReason(err error) string {
	if se, ok := err.(*syntax.Error); ok {
		return "invalid pattern: " + string(se.Code) + ": " + se.Expr
	}
	return "invalid pattern: " + err.Error()
}

// applies reports whether the rule's scope covers the item.
func (c *Compiled) applies(it *Item) bool {
	switch c.rule.Scope {
	case ScopeFolder:
		return it.FolderID == c.rule.FolderID
	case ScopeFeed:
		return it.FeedID == c.rule.FeedID
	}
	return true
}

// Set is an immutable, compiled rule set. Build one per filters generation and share it.
type Set struct {
	rules []*Compiled // ascending id
}

// NewSet validates and compiles rules, enforces the set-wide limits and orders them by id.
// Disabled rules are validated and kept (Len counts them) but never evaluated and do not
// count toward the regex and term caps. The error, when not nil, is a *SetError.
func NewSet(rules []Rule) (*Set, error) {
	if len(rules) > MaxRules {
		return nil, &SetError{Index: MaxRules, Err: bad("rules", "more than %d filters", MaxRules)}
	}
	ids := map[int64]int{}
	s := &Set{}
	regexN, termN := 0, 0
	for i, r := range rules {
		c, err := compileRule(r)
		if err != nil {
			return nil, &SetError{Index: i, RuleID: r.ID, Err: err}
		}
		if j, dup := ids[r.ID]; dup && r.ID != 0 {
			return nil, &SetError{Index: i, RuleID: r.ID, Err: bad("id", "duplicates the rule at index %d", j)}
		}
		ids[r.ID] = i
		if r.Enabled {
			if r.Kind == KindRegex {
				regexN++
				if regexN > MaxRegexRules {
					return nil, &SetError{Index: i, RuleID: r.ID, Err: bad("kind", "more than %d enabled regex filters", MaxRegexRules)}
				}
			} else {
				termN += len(r.Terms)
				if termN > MaxTextTerms {
					return nil, &SetError{Index: i, RuleID: r.ID, Err: bad("terms", "more than %d enabled text terms in all filters", MaxTextTerms)}
				}
			}
		}
		s.rules = append(s.rules, c)
	}
	sort.SliceStable(s.rules, func(i, j int) bool { return s.rules[i].rule.ID < s.rules[j].rule.ID })
	return s, nil
}

// Len is the number of rules in the set, enabled or not.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.rules)
}
