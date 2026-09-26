package filter

import (
	"math/rand"
	"regexp/syntax"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mustParse(t *testing.T, p string) *syntax.Regexp {
	t.Helper()
	re, err := syntax.Parse(p, syntax.Perl)
	require.NoError(t, err)
	return re
}

// Large counted repeats over broad classes keep hundreds of NFA threads alive at every byte: the
// review measured 67 ms per 8 KiB item for [a-z ]{1,400}[0-9]{3} and 2 s per item for 25 such rules.
func TestRegexLargeRepeatsAreRefused(t *testing.T) {
	for _, p := range []string{`[a-z ]{1,400}[0-9]{3}`, `\w{1,300}\d{4}`, `(?:\pL|\s){2,300}\d{5}`, `(?:[a-z ]{1,20}){1,20}\d`, `[a-z]{200}`, `x{51,}`} {
		err := Validate(NewRule(ScopeGlobal, KindRegex, ActionMute, p))
		var fe *Error
		require.ErrorAs(t, err, &fe, p)
		require.Equal(t, "terms[0]", fe.Field)
		require.Contains(t, fe.Message, "repeats", p)
	}
	for _, p := range []string{`[a-z ]{1,50}[0-9]{3}`, `(?:ab){2,}`, `\d{4}-\d{2}`, `(.*b){20}`, `(?:a{5}){10}`, `x+y*z?`} {
		require.NoError(t, Validate(NewRule(ScopeGlobal, KindRegex, ActionMute, p)), p)
	}
	require.Equal(t, 1, maxRepeat(mustParse(t, `a+b*`)))
	require.Equal(t, 50, maxRepeat(mustParse(t, `(?:a{5}){10}`)))
	require.Equal(t, 1000, maxRepeat(mustParse(t, `(?:a{10}){100}`)), "nested repeats multiply")
}

func TestRegexCostIsCappedPerRuleAndPerSet(t *testing.T) {
	// One rule on every field with five heavy patterns is over the limit on its own.
	const p = `[a-z ]{1,50}[0-9]{3}[a-z ]{1,50}[0-9]{3}`
	heavy := NewRule(ScopeGlobal, KindRegex, ActionMute, p+"a", p+"b", p+"c", p+"d", p+"e")
	heavy.Fields = allFields
	err := Validate(heavy)
	var fe *Error
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "terms", fe.Field)
	require.Contains(t, fe.Message, "too expensive")

	// The set adds up its enabled regex rules; a disabled one costs nothing.
	one := NewRule(ScopeGlobal, KindRegex, ActionMute, p+"a", p+"b", p+"c")
	one.Fields = []Field{FieldContent}
	c, err := CompileRule(one)
	require.NoError(t, err)
	n := MaxRegexSetCost/c.cost + 1
	require.LessOrEqual(t, n, MaxRegexRules, "the cost cap, not the count cap, must be what stops this set")
	var rs []Rule
	for i := 1; i <= n; i++ {
		r := one
		r.ID = int64(i)
		rs = append(rs, r)
	}
	_, err = NewSet(rs)
	var se *SetError
	require.ErrorAs(t, err, &se)
	require.Equal(t, "terms", se.Err.Field)
	require.Contains(t, se.Err.Message, "together")
	for i := se.Index; i < len(rs); i++ {
		rs[i].Enabled = false
	}
	_, err = NewSet(rs)
	require.NoError(t, err)
}

// The worst case the limits allow stays bounded: fill the set with the costliest pattern shape the
// review found (a broad class under the largest allowed repeat) until NewSet refuses, then time one
// item with a full content scan of letters and spaces, which keeps every thread alive.
func TestRegexWorstCaseAtTheCostCap(t *testing.T) {
	skipTimingUnderRace(t)
	var rs []Rule
	for i := 1; i <= MaxRegexRules; i++ {
		r := NewRule(ScopeGlobal, KindRegex, ActionMute, `[a-z ]{1,50}[0-9]{3}`, `(?:\pL|\s){1,50}\d{5}`)
		r.ID, r.Fields = int64(i), []Field{FieldContent}
		if _, err := NewSet(append(append([]Rule(nil), rs...), r)); err != nil {
			break
		}
		rs = append(rs, r)
	}
	require.NotEmpty(t, rs)
	s, err := NewSet(rs)
	require.NoError(t, err)
	it := Item{Content: sentence(rand.New(rand.NewSource(4)), 3000)}
	const rounds = 3
	start := time.Now()
	for i := 0; i < rounds; i++ {
		s.Evaluate(it)
	}
	per := time.Since(start) / rounds
	t.Logf("%d regex rules at the cost cap, 8 KiB of letters: %v per item", len(rs), per)
	require.Less(t, per, 2*time.Second) // about 0.4 s measured; loose for loaded CI runners
}

// keywordRule is the typical regex rule the set cap has to admit: a 15-word alternation, case
// insensitive, on title and content.
func keywordRule(r *rand.Rand, id int64) Rule {
	ws := make([]string, 15)
	for i := range ws {
		ws[i] = vocab[r.Intn(len(vocab))]
	}
	x := NewRule(ScopeGlobal, KindRegex, ActionMute, `(?:`+strings.Join(ws, "|")+`)`)
	x.ID, x.Fields = id, []Field{FieldTitle, FieldContent}
	return x
}

// The review's regression: a 15-word alternation costs about 1,450 on title and content, so 13 of
// them passed the old 20,000 set cap where 25 regex rules had been allowed. 40 must fit now, and
// they stay cheap even with their literal prefilter defeated (content that contains a keyword).
func TestRegexSetCapAdmitsKeywordRules(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	var rs []Rule
	for i := 1; i <= 40; i++ {
		rs = append(rs, keywordRule(r, int64(i)))
	}
	c, err := CompileRule(rs[0])
	require.NoError(t, err)
	t.Logf("one 15-word alternation on title and content costs %d", c.cost)
	require.NoError(t, ValidateEdit(rs, len(rs)-1))
	s, err := NewSet(rs)
	require.NoError(t, err)
	if raceEnabled {
		return
	}
	for _, x := range s.rules {
		for i := range x.res {
			x.res[i].req = nil // as if every item contained one of the keywords
		}
	}
	it := Item{Title: sentence(r, 12), Content: sentence(r, 3000)}
	start := time.Now()
	for i := 0; i < 5; i++ {
		s.Evaluate(it)
	}
	per := time.Since(start) / 5
	t.Logf("40 keyword rules, prefilter off, 8 KiB content: %v per item", per)
	require.Less(t, per, 250*time.Millisecond) // about 13 ms measured
}

// legacyRegex was accepted before the repeat and cost limits (it repeats a class 400 times).
const legacyRegex = `[a-z ]{1,400}[0-9]{3}`

// A stored rule from an older version that no longer compiles must not block the edit of another
// rule, whether it is enabled (ingest skips it) or not; the edited rule itself is held to every limit,
// and the error names it.
func TestValidateEditJudgesOnlyTheEditedRule(t *testing.T) {
	legacyOn := NewRule(ScopeGlobal, KindRegex, ActionMute, legacyRegex)
	legacyOn.ID = 1
	legacyOff := legacyOn
	legacyOff.ID, legacyOff.Enabled = 2, false
	fresh := NewRule(ScopeGlobal, KindText, ActionMute, "sponsored")
	fresh.ID = 3
	rules := []Rule{legacyOn, legacyOff, fresh}
	require.NoError(t, ValidateEdit(rules, 2))
	_, err := NewSet(rules[1:])
	require.NoError(t, err, "a disabled rule that no longer compiles does not fail the set")

	err = ValidateEdit(rules, 0)
	var se *SetError
	require.ErrorAs(t, err, &se)
	require.EqualValues(t, 1, se.RuleID)
	require.Contains(t, se.Err.Message, "repeats")

	// Set-wide: the edited rule is refused when it pushes the enabled rules past the cap, and only then.
	const p = `[a-z ]{1,50}[0-9]{3}`
	heavy := NewRule(ScopeGlobal, KindRegex, ActionMute, p+"a", p+"b", p+"c")
	heavy.Fields = []Field{FieldContent}
	c, err := CompileRule(heavy)
	require.NoError(t, err)
	var set []Rule
	for i := 1; (i-1)*c.cost <= MaxRegexSetCost-c.cost; i++ {
		x := heavy
		x.ID = int64(10 + i)
		set = append(set, x)
	}
	extra := heavy
	extra.ID = 99
	set = append(set, extra)
	err = ValidateEdit(set, len(set)-1)
	require.ErrorAs(t, err, &se)
	require.EqualValues(t, 99, se.RuleID)
	require.Contains(t, se.Err.Message, "together")
	set[len(set)-1].Enabled = false
	require.NoError(t, ValidateEdit(set, len(set)-1), "a disabled edit adds nothing to the sums")
	require.NoError(t, ValidateEdit(set, 0), "an earlier rule is not blamed for a later one")
}

// Sanitize names the stored rules to disable: one that no longer compiles (enabled or not) and the
// newest enabled ones past a set-wide cap.
func TestSanitizeNamesRulesToDisable(t *testing.T) {
	legacy := NewRule(ScopeGlobal, KindRegex, ActionMute, legacyRegex)
	legacy.ID = 5
	off := legacy
	off.ID, off.Enabled = 6, false
	ok := NewRule(ScopeGlobal, KindText, ActionMute, "a")
	ok.ID = 7
	got := Sanitize([]Rule{ok, legacy, off})
	require.Len(t, got, 2)
	require.Contains(t, got[1], "repeats")
	require.Contains(t, got[2], "repeats")

	var rs []Rule
	for i := MaxRegexRules + 2; i >= 1; i-- { // listed newest first: the order must not matter
		x := NewRule(ScopeGlobal, KindRegex, ActionMute, `ab\d`)
		x.ID = int64(i)
		rs = append(rs, x)
	}
	got = Sanitize(rs)
	require.Len(t, got, 2)
	require.Contains(t, got[0], "more than")
	require.Contains(t, got[1], "more than")
	require.EqualValues(t, MaxRegexRules+2, rs[0].ID)
	require.EqualValues(t, MaxRegexRules+1, rs[1].ID)
}
