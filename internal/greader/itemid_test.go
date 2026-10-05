package greader

import (
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseItemIDVectors(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"tag:google.com,2005:reader/item/00063f8740c61a40", 1758700000123456, true},
		{"tag:google.com,2005:reader/item/63f8740c61a40", 1758700000123456, true}, // unpadded long form
		{"00063f8740c61a40", 1758700000123456, true},                              // bare hex ids
		{"1758700000123456", 1758700000123456, true},                              // decimal
		{"00058b10ce338909", 1560279178774793, true},                              // vectors from a client that sends unpadded hex ids
		{"tag:google.com,2005:reader/item/00058b10ce338909", 1560279178774793, true},
		{"tag:google.com,2005:reader/item/ffffffffffffcdef", -12817, true},
		{"ffffffffffffcdef", -12817, true},
		{"-12817", -12817, true},
		{"0000000000000000", 0, true},
		{"0", 0, true},
		{"0x63f8740c61a40", 1758700000123456, true},
		{"  1758700000123456  ", 1758700000123456, true},
		{"", 0, false},
		{"   ", 0, false},
		{"garbage", 0, false},
		{"tag:google.com,2005:reader/item/", 0, false},
		{"tag:google.com,2005:reader/item/zzzz", 0, false},
		{"00000000000000000", 0, false}, // 17 hex digits
		{"tag:google.com,2005:reader/item/10000000000000000", 0, false},
		{"99999999999999999999", 0, false}, // decimal overflow
		{"-", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseItemID(c.in)
		require.Equal(t, c.ok, ok, "%q", c.in)
		if c.ok {
			require.Equal(t, c.want, got, "%q", c.in)
		}
	}
}

func TestFormatsMatchDocumentedExamples(t *testing.T) {
	require.Equal(t, "00063f8740c61a40", FormatHex16(1758700000123456))
	require.Equal(t, "tag:google.com,2005:reader/item/00063f8740c61a40", FormatLongID(1758700000123456))
	require.Equal(t, "ffffffffffffcdef", FormatHex16(-12817))
	require.Equal(t, "1560279178774793", FormatDecimal(1560279178774793))
}

// Property: every emitted form round-trips for arbitrary int64 values; the
// bare-hex form is only unambiguous for ids Kipple allocates (positive, < 2^60,
// so the padded form always starts with 0).
func TestItemIDRoundTripProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	check := func(id int64) {
		got, ok := ParseItemID(FormatDecimal(id))
		require.True(t, ok)
		require.Equal(t, id, got, "decimal %d", id)
		got, ok = ParseItemID(FormatLongID(id))
		require.True(t, ok)
		require.Equal(t, id, got, "long %d", id)
		got, ok = ParseItemID(longIDPrefix + trimZeros(FormatHex16(id)))
		require.True(t, ok)
		require.Equal(t, id, got, "unpadded long %d", id)
		if id >= 0 && id < 1<<60 {
			got, ok = ParseItemID(FormatHex16(id))
			require.True(t, ok)
			require.Equal(t, id, got, "bare hex %d", id)
		}
	}
	for _, id := range []int64{0, 1, -1, 1<<63 - 1, -1 << 63, 1<<60 - 1, -12817, 1758700000123456} {
		check(id)
	}
	for i := 0; i < 20000; i++ {
		check(int64(rng.Uint64()))
		check(rng.Int64N(1 << 60))
		check(-rng.Int64N(1 << 40))
	}
}

func trimZeros(s string) string {
	for len(s) > 1 && s[0] == '0' {
		s = s[1:]
	}
	return s
}
