package setup

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var displayRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){5}$`)

func TestNewTokenFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok, err := NewToken()
		require.NoError(t, err)
		require.Regexp(t, displayRE, tok)
		require.False(t, seen[tok], "tokens repeat")
		seen[tok] = true
		norm, ok := NormalizeToken(tok)
		require.True(t, ok)
		require.Len(t, norm, TokenLen)
		require.Equal(t, tok, FormatToken(norm))
	}
}

func TestNormalizeTokenFolding(t *testing.T) {
	const canon = "0123456789ABCDEFGHJKMNPQ"
	for _, in := range []string{
		canon,
		"0123-4567-89AB-CDEF-GHJK-MNPQ",
		"0123 4567 89ab cdef ghjk mnpq",
		"o123-4567-89ab-cdef-ghjk-mnpq", // O is 0
		"0I23-4567-89AB-CDEF-GHJK-MNPQ", // I is 1
		"0l23-4567-89AB-CDEF-GHJK-MNPQ", // l is 1
		"\t0123-4567-89AB-CDEF-GHJK-MNPQ ",
	} {
		got, ok := NormalizeToken(in)
		require.True(t, ok, in)
		require.Equal(t, canon, got, in)
	}
	for _, in := range []string{
		"", "0123", canon + "0", canon[:23],
		"U123456789ABCDEFGHJKMNPQ", // U is not in the alphabet
		"0123456789ABCDEFGHJKMNP!",
		"０123456789ABCDEFGHJKMNPQ", // a full-width digit
		strings.Repeat("-", 200),
	} {
		_, ok := NormalizeToken(in)
		require.False(t, ok, in)
	}
}

func TestTokenMatches(t *testing.T) {
	tok, err := NewToken()
	require.NoError(t, err)
	norm, _ := NormalizeToken(tok)
	h := tokenHash(norm)
	require.True(t, tokenMatches(tok, h))
	require.True(t, tokenMatches(strings.ToLower(strings.ReplaceAll(tok, "-", " ")), h))
	require.False(t, tokenMatches("", h))
	require.False(t, tokenMatches(tok[:len(tok)-1], h))
	// One symbol off.
	b := []byte(norm)
	if b[0] == '0' {
		b[0] = '2'
	} else {
		b[0] = '0'
	}
	require.False(t, tokenMatches(string(b), h))
	// The empty-string hash never matches malformed input, even by accident.
	require.False(t, tokenMatches("not a token", tokenHash("")))
}

func FuzzSetupToken(f *testing.F) {
	f.Add("0123-4567-89AB-CDEF-GHJK-MNPQ")
	f.Add("o123 4567 89ab cdef ghjk mnpq")
	f.Add("")
	f.Add("U")
	f.Add(strings.Repeat("-", 129))
	tok, _ := NewToken()
	norm, _ := NormalizeToken(tok)
	h := tokenHash(norm)
	f.Fuzz(func(t *testing.T, in string) {
		got, ok := NormalizeToken(in)
		if !ok {
			require.False(t, tokenMatches(in, h))
			return
		}
		require.Len(t, got, TokenLen)
		for i := 0; i < len(got); i++ {
			require.True(t, strings.IndexByte(crockford, got[i]) >= 0)
		}
		again, ok2 := NormalizeToken(FormatToken(got))
		require.True(t, ok2)
		require.Equal(t, got, again, "normalize(format(x)) == x")
		require.Equal(t, got == norm, tokenMatches(in, h))
	})
}
