package greader

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// Reader clients retry a 503: an edit-tag refused during the search index rebuild is answered so,
// with Retry-After, instead of a 500.
func TestServerErrorMapsMaintenanceTo503(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	c := &call{a: &API{log: slog.New(slog.DiscardHandler)}, w: &statusWriter{ResponseWriter: rec}, path: "/reader/api/0/edit-tag"}
	c.serverError("edit-tag", fmt.Errorf("x: %w", store.ErrMaintenance))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "15", rec.Header().Get("Retry-After"))
}
