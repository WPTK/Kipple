package sanitize

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Re-sanitizing what the pipeline already produced changes nothing, so a pass over stored rows
// rewrites only the rows an older or weaker policy left behind.
func TestResanitizeKeepsWhatThePipelineProduced(t *testing.T) {
	for _, c := range corpus {
		once, _ := Content(c.in, "https://e.example/post")
		require.Equal(t, once, Resanitize(once), c.name)
	}
	const keep = `<p>See <a href="#fn1">1</a> and <a href="https://e.example/a">a</a></p><p><img src="https://e.example/i.png" alt="x"></p>`
	once, _ := Content(keep)
	require.Equal(t, once, Resanitize(once))
}

func TestResanitizeStripsWhatTheStoredRowKept(t *testing.T) {
	got := Resanitize(`<p onclick="x()">hi</p><script>alert(1)</script><a href="javascript:alert(1)">l</a>`)
	require.Equal(t, `<p>hi</p>l`, got)
}
