package filter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// An inverted rule that scans the category field never fires on an item without categories:
// no stored categories means unknown, not "does not match".
func TestInvertedCategoryRuleNeedsCategories(t *testing.T) {
	inv := func(fields ...Field) Rule {
		return Rule{ID: 1, Enabled: true, Scope: ScopeGlobal, Kind: KindText, Terms: []string{"news"}, Fields: fields,
			WholeWord: true, FoldDiacritics: true, Invert: true, Action: ActionMute}
	}
	require.False(t, fires(t, inv(FieldCategory), Item{Title: "x"}), "no categories: skipped")
	require.False(t, fires(t, inv(FieldCategory), Item{Title: "x", Categories: []string{}}))
	require.True(t, fires(t, inv(FieldCategory), Item{Categories: []string{"sports"}}))
	require.False(t, fires(t, inv(FieldCategory), Item{Categories: []string{"news"}}))
	require.False(t, fires(t, inv(FieldTitle, FieldCategory), Item{Title: "x"}), "a rule that scans category at all")
	// other inverted rules keep their meaning: absent in an empty title is a match
	require.True(t, fires(t, inv(FieldTitle), Item{}))
}
