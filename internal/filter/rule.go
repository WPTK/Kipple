// Package filter is Kipple's keyword rules engine (backend-additions-round2 section 1): mute,
// mark_read, star and highlight rules, scoped to everything, a folder or a feed. It is pure: no
// I/O, no database, no clock. The store loads rules, NewSet compiles and validates them, and
// Set.Evaluate decides what happens to one item. The ingest path, the preview and the
// retroactive apply all call the same Evaluate, so they cannot disagree.
//
// Matching semantics
//
//   - text rules match literal terms. A multi-word term is a phrase: its words in order with any
//     whitespace between them. With WholeWord (the default) the runes on each side of a match must
//     not be letters, digits or combining marks; the check is skipped on a side where the term
//     itself starts or ends with a character that is not a word character. Runes of scripts written
//     without spaces (Han, Kana, Thai, Lao, Khmer, Myanmar) are never word characters: each is its own
//     boundary, so a CJK term matches as a substring and "iphone" matches in "新iPhone发布".
//   - Unless CaseSensitive, text and terms are lower-cased. Unless disabled, FoldDiacritics
//     applies NFKD and drops the combining marks (so "cafe" matches "Café").
//   - regex rules are Go RE2 patterns (linear time, no backreferences), compiled with (?i) unless
//     CaseSensitive, and searched on the raw (capped) text of each field.
//   - Invert makes a rule fire when the item does NOT match, on any of its fields. An inverted
//     rule therefore also fires on an item whose scanned fields are all empty. The exception is the
//     category field: items fetched before categories were stored (and feeds that carry none) have
//     no categories, which means unknown, not "does not match", so an inverted rule that scans
//     category never fires on an item without categories (a plain rule cannot fire on it either).
//
// Limits (all enforced by Validate and NewSet, all constants below): at most 200 rules, 25 enabled
// regex rules and 2000 enabled text terms in a set; 1 to 50 terms per rule; a text term is 1 to 100
// runes; a regex rule has 1 to 5 patterns of at most 256 bytes and at most 500 program
// instructions, with no counted repeat above 50 copies (nested repeats multiplied), and must not
// match the empty string; the enabled regex rules of a set stay under MaxRegexCost, so their
// worst-case evaluation time per item is bounded. Scanned text is truncated (at a rune
// boundary) before it is normalized or matched, so a giant item costs the same as a large one:
// content 32 KiB for text rules and 8 KiB for regex rules, every other field 4 KiB.
//
// Precedence (section 1.3): every enabled rule in scope is evaluated and the results combine as a
// set, so evaluation order never changes the outcome. star beats mute (a starred item is never
// muted), mute implies read, mark_read implies read, highlight has no stored effect. Rules are
// evaluated in ascending id order and MutedBy is the lowest id among the matching mute rules.
package filter

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits. See the package comment.
const (
	MaxRules            = 200
	MaxRegexRules       = 25
	MaxTextTerms        = 2000 // enabled text terms across a whole set
	MaxTermsPerRule     = 50
	MaxTermRunes        = 100 // text terms
	MaxRegexPatterns    = 5
	MaxRegexBytes       = 256
	MaxRegexProgInsts   = 500 // compiled program size of one pattern
	MaxRegexRepeat      = 50  // copies a counted repeat makes, nested repeats multiplied
	// MaxRegexCost bounds the worst-case evaluation time of the enabled regex rules of a set, which is
	// linear in program size times text scanned (Go's regexp has no DFA: up to one live thread per
	// instruction per byte). A rule costs the sum of its patterns' instructions times the KiB it scans
	// (8 for content, 4 for each other field); a set sums its enabled regex rules. At the limit the
	// worst case (TestRegexWorstCaseAtTheCostCap) measured about 180 ms per item on the development
	// machine, against 2 s for 25 rules before these limits; a typical rule costs well under a
	// thousand (the 25 rules x 5 patterns on content of the benchmarks cost 17,600 and take 16 ms).
	MaxRegexCost = 20000
	MaxNameBytes        = 200
	MaxTextContentScan  = 32 << 10 // content bytes scanned by text rules
	MaxRegexContentScan = 8 << 10  // content bytes scanned by regex rules
	MaxFieldScan        = 4 << 10  // bytes scanned for every non-content field
)

// Scope says where a rule applies.
type Scope string

const (
	ScopeGlobal Scope = "global"
	ScopeFolder Scope = "folder"
	ScopeFeed   Scope = "feed"
)

// Kind is how terms are interpreted.
type Kind string

const (
	KindText  Kind = "text"
	KindRegex Kind = "regex"
)

// Action is what a firing rule does.
type Action string

const (
	ActionMute      Action = "mute"
	ActionMarkRead  Action = "mark_read"
	ActionStar      Action = "star"
	ActionHighlight Action = "highlight"
)

// Field names one piece of an item a rule can look at.
type Field string

const (
	FieldTitle    Field = "title"
	FieldAuthor   Field = "author"
	FieldContent  Field = "content" // the item's plain text
	FieldURL      Field = "url"
	FieldCategory Field = "category" // the item's categories, one per line
	FieldFeed     Field = "feed"     // the feed's display title
)

var allFields = []Field{FieldTitle, FieldAuthor, FieldContent, FieldURL, FieldCategory, FieldFeed}

func validField(f Field) bool {
	for _, x := range allFields {
		if x == f {
			return true
		}
	}
	return false
}

// Rule is one stored filter. It mirrors the filters table (0004). Zero values are not
// defaults: the caller (the store or the API) applies WholeWord=true and FoldDiacritics=true
// for a new rule; see NewRule.
type Rule struct {
	ID             int64
	Name           string
	Enabled        bool
	Scope          Scope
	FolderID       int64 // scope folder
	FeedID         int64 // scope feed
	Kind           Kind
	Terms          []string
	Fields         []Field // empty means title only
	CaseSensitive  bool
	WholeWord      bool // text only
	FoldDiacritics bool // text only
	Invert         bool
	Action         Action
	Position       int // display order only; never affects evaluation
}

// NewRule returns a rule with the spec defaults (enabled, whole word, fold diacritics, title).
func NewRule(scope Scope, kind Kind, action Action, terms ...string) Rule {
	return Rule{Enabled: true, Scope: scope, Kind: kind, Action: action, Terms: terms,
		Fields: []Field{FieldTitle}, WholeWord: true, FoldDiacritics: true}
}

// Item is what a rule is evaluated against. FolderID is the feed's current folder.
type Item struct {
	FeedID     int64
	FolderID   int64
	FeedTitle  string
	Title      string
	Author     string
	URL        string
	Content    string // plain text
	Categories []string
}

// Error is a validation failure. Field names the offending part in the API's vocabulary
// ("terms[2]", "fields[1]", "scope"), so a handler can return it as {"error":"bad_filter",...}.
type Error struct {
	Field   string
	Message string
}

func (e *Error) Error() string { return "filter: " + e.Field + ": " + e.Message }

func bad(field, format string, args ...any) *Error {
	return &Error{Field: field, Message: fmt.Sprintf(format, args...)}
}

// SetError is a validation failure of one rule inside a set.
type SetError struct {
	Index  int // position in the slice given to NewSet
	RuleID int64
	Err    *Error
}

func (e *SetError) Error() string {
	return fmt.Sprintf("filter: rule %d (index %d): %s: %s", e.RuleID, e.Index, e.Err.Field, e.Err.Message)
}
func (e *SetError) Unwrap() error { return e.Err }

// Validate checks one rule on its own (everything except the set-wide limits). It compiles
// regexes, so a nil result means the rule will compile.
func Validate(r Rule) error {
	_, err := compileRule(r)
	if err != nil {
		return err
	}
	return nil
}

func validString(field, s string) *Error {
	if !utf8.ValidString(s) {
		return bad(field, "is not valid UTF-8")
	}
	if strings.ContainsRune(s, 0) {
		return bad(field, "contains a NUL character")
	}
	return nil
}
