package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A session lookup that fails (here: a cancelled request context) is a server
// error, never 401: a 401 signs the web app out although the session is valid.
func TestSessionLookupErrorIsNotUnauthorized(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	rec := h.do("GET", "/api/bootstrap", "", withCookie(c), func(r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		*r = *r.WithContext(ctx)
	})
	require.NotEqual(t, http.StatusUnauthorized, rec.Code)
	require.GreaterOrEqual(t, rec.Code, 500)
	rec = h.do("GET", "/api/bootstrap", "", withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code, "the session is still good")
}
