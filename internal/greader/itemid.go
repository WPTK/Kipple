// Package greader serves the Google Reader compatible sync API under
// /api/greader.php (design §6).
package greader

import (
	"strconv"
	"strings"
)

// longIDPrefix is the prefix of the long-form item id emitted in item bodies.
const longIDPrefix = "tag:google.com,2005:reader/item/"

// FormatDecimal is the itemRefs[].id form: a decimal string.
func FormatDecimal(id int64) string { return strconv.FormatInt(id, 10) }

// FormatHex16 is the zero-padded 16 digit hex form (two's complement for negatives).
func FormatHex16(id int64) string {
	const digits = "0123456789abcdef"
	var b [16]byte
	u := uint64(id)
	for i := 15; i >= 0; i-- {
		b[i] = digits[u&0xf]
		u >>= 4
	}
	return string(b[:])
}

// FormatLongID is the items[].id form.
func FormatLongID(id int64) string { return longIDPrefix + FormatHex16(id) }

// ParseItemID parses an inbound i= value (design §3): long form (padded or
// not), decimal (negatives allowed), or bare/0x hex. ok is false for values
// that cannot be an id; callers skip those and never answer with a 4xx.
func ParseItemID(s string) (id int64, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return parseHex(s[i+1:])
	}
	if s[0] == '-' {
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil
	}
	if allDigits(s) && s[0] != '0' {
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	return parseHex(s)
}

// parseHex accepts 1-16 hex digits and bitcasts the unsigned value to int64.
func parseHex(s string) (int64, bool) {
	if len(s) < 1 || len(s) > 16 {
		return 0, false
	}
	u, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, false
	}
	return int64(u), true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
