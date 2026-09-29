// Package setup holds the pieces of first-run setup (docs/setup-wizard-design.md):
// the one-time setup token, the Host gate against DNS rebinding, the open gate
// for passwordless "open" mode, and account creation shared by the environment
// path (KIPPLE_USERNAME / KIPPLE_PASSWORD) and the browser wizard.
package setup

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"strings"
)

// crockford is Crockford's base32 alphabet: no I, L, O or U.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// TokenLen is the number of significant characters in a setup token: 24
// characters of base32 carry 120 bits.
const TokenLen = 24

// NewToken returns a fresh token in display form, XXXX-XXXX-XXXX-XXXX-XXXX-XXXX.
func NewToken() (string, error) {
	b := make([]byte, TokenLen*5/8) // 15 bytes = 120 bits = 24 symbols
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var sym [TokenLen]byte
	var acc uint32
	bits, n := 0, 0
	for _, x := range b {
		acc = acc<<8 | uint32(x)
		bits += 8
		for bits >= 5 {
			bits -= 5
			sym[n] = crockford[(acc>>uint(bits))&31]
			n++
		}
	}
	return FormatToken(string(sym[:])), nil
}

// FormatToken groups a normalized token in fours with dashes.
func FormatToken(norm string) string {
	var sb strings.Builder
	for i := 0; i < len(norm); i++ {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(norm[i])
	}
	return sb.String()
}

// NormalizeToken turns hand-typed input into the canonical 24 symbols: case,
// spaces and dashes are ignored, and O folds to 0 and I and L to 1 as Crockford
// specifies. ok is false for anything that cannot be a token (a wrong length or
// a character outside the alphabet, U included).
func NormalizeToken(in string) (string, bool) {
	if len(in) > 128 {
		return "", false
	}
	out := make([]byte, 0, TokenLen)
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case c == '-' || c == ' ' || c == '\t':
			continue
		case c >= 'a' && c <= 'z':
			c -= 'a' - 'A'
		}
		switch c {
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		if strings.IndexByte(crockford, c) < 0 {
			return "", false
		}
		if len(out) == TokenLen {
			return "", false
		}
		out = append(out, c)
	}
	if len(out) != TokenLen {
		return "", false
	}
	return string(out), true
}

// tokenHash is what is kept in memory: sha256 of the normalized token.
func tokenHash(norm string) [32]byte { return sha256.Sum256([]byte(norm)) }

// tokenMatches compares hand-typed input with a stored hash in constant time
// (over the hash, so the input's length says nothing either).
func tokenMatches(in string, want [32]byte) bool {
	norm, ok := NormalizeToken(in)
	if !ok {
		norm = "" // still hash and compare, so malformed input costs the same
	}
	got := tokenHash(norm)
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1 && ok
}
