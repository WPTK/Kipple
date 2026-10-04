package sanitize

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWordCountMatchesFields(t *testing.T) {
	for _, s := range []string{
		"", " ", "\n\t ", "one", " one ", "two words", "a  b\tc\nd\r\ne",
		"nbsp joined", "nel\u0085x", "line sep para", "em space　ideo", "zws​not-space",
		" ", "café über", "bad \xff\xfe bytes", "trail ", " ogham ",
		strings.Repeat("word ", 1000),
	} {
		require.Equal(t, len(strings.Fields(s)), WordCount(s), "%q", s)
	}
}

func BenchmarkWordCount(b *testing.B) {
	s := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 200)
	b.Run("new", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			WordCount(s)
		}
	})
	b.Run("fields", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = len(strings.Fields(s))
		}
	})
}
