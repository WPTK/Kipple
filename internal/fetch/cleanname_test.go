package fetch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fillers and blanks that render as nothing are dropped; marks and variation selectors need a base,
// tag characters a flag.
func TestCleanNameFillersBasesAndTags(t *testing.T) {
	require.Equal(t, "", CleanName("\u3164\u115F\u1160\uFFA0\u2800\u2061\u2062\u2063\u2064\uFFF9\uFFFA\uFFFB\u034F\u180B\u180C\u180D"))
	require.Equal(t, "Hidden name", CleanName("Hidden\u3164 \u2800name"))
	require.Equal(t, "", CleanName("\u0301\u0308 \uFE0F \U000E0067\U000E0062\U000E007F"))
	require.Equal(t, "cafe\u0301 ❤\uFE0F", CleanName("cafe\u0301 ❤\uFE0F"))
	require.Equal(t, "x", CleanName("x \u200D\uFE0F"), "a selector after a joiner has no base")
	scotland := "\U0001F3F4\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F"
	require.Equal(t, "Go "+scotland, CleanName("Go "+scotland), "a flag tag sequence stays whole")
	require.Equal(t, "ab", CleanName("a\U000E0067\U000E0062b"), "stray tags go")
}

// A cluster longer than the limit is cut at the limit, never down to a lone ellipsis.
func TestCleanNameNeverOnlyAnEllipsis(t *testing.T) {
	got := []rune(CleanName("a" + strings.Repeat("\u0301", 300)))
	require.Len(t, got, MaxTitleRunes)
	require.Equal(t, 'a', got[0])
	require.Equal(t, rune(0x2026), got[len(got)-1])

	got = []rune(CleanName("\U0001F3F4" + strings.Repeat("\U000E0067", 300)))
	require.Len(t, got, MaxTitleRunes)
	require.Equal(t, rune(0x1F3F4), got[0])

	require.Equal(t, "", CleanName(strings.Repeat("\U000E0067", 300)), "tags with no flag are nothing")
}

// NameRunes counts what a typed name is once cleaned, before any cut.
func TestNameRunes(t *testing.T) {
	require.Equal(t, 3, NameRunes(" abc\u200B\u200E "))
	require.Equal(t, 500, NameRunes(strings.Repeat("x", 500)))
}
