package greader

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A correctly form-encoded label followed by a stray '&' is not glued into its name; raw names
// keep their repair.
func TestRepairDoesNotGlueStrayAmpersandAfterEncodedLabel(t *testing.T) {
	for _, tc := range []struct{ in, key, want string }{
		{"a=user%2F-%2Flabel%2FMy+Folder&", "a", "user/-/label/My Folder"},
		{"T=tok&s=user%2F-%2Flabel%2FMy+Folder&", "s", "user/-/label/My Folder"},
		{"s=user%2F-%2Flabel%2FMy+Folder&&&T=tok", "s", "user/-/label/My Folder"},
		{"s=user/-/label/R&", "s", "user/-/label/R&"},
		{"s=user/-/label/A&&B&T=tok", "s", "user/-/label/A&&B"},
		{"s=user/-/label/My%20News&100%&T=tok", "s", "user/-/label/My News&100%"},
	} {
		got, ok := splitPairsLimit(tc.in, true)
		require.True(t, ok)
		var val string
		for _, p := range got {
			if p.key == tc.key {
				val = p.val
			}
		}
		require.Equal(t, tc.want, val, tc.in)
	}
	got, _ := splitPairsLimit("s=user%2F-%2Flabel%2FMy+Folder&&T=tok", true)
	require.Equal(t, "tok", (&Params{body: got}).Get("T"), "the parameter after a stray & survives")
}
