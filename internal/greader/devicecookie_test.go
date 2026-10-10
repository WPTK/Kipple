package greader

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// The kipple_device cookie only selects an appearance profile for the web UI: the Reader API
// neither accepts it as credentials nor sets it.
func TestReaderAPIIgnoresDeviceCookie(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	w := h.do(http.MethodGet, base+rd+"token", "", map[string]string{"Authorization": "", "Cookie": "kipple_device=AAAAAAAAAAAAAAAAAAAAAA"})
	require.Equal(t, 401, w.Result().StatusCode)
	// An authorized call with the cookie behaves like one without, and sets no cookie.
	w = h.do(http.MethodGet, base+rd+"token", "", map[string]string{"Authorization": "GoogleLogin auth=" + h.tok, "Cookie": "kipple_device=AAAAAAAAAAAAAAAAAAAAAA"})
	require.Equal(t, 200, w.Code)
	require.Empty(t, w.Result().Cookies())
}
