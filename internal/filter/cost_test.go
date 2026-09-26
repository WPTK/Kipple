package filter

import (
	"math/rand"
	"regexp/syntax"
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
	var rs []Rule
	for i := 1; i <= MaxRegexRules; i++ {
		r := NewRule(ScopeGlobal, KindRegex, ActionMute, `[a-z ]{1,50}[0-9]{3}`)
		r.ID, r.Fields = int64(i), []Field{FieldContent}
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
	require.Less(t, per, 2*time.Second) // about 180 ms measured; loose for loaded CI runners
}
