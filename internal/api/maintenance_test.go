package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// A write refused because the search index rebuild owns the writer is a temporary 503 with
// Retry-After, not a 500; anything else stays a 500.
func TestServerErrorMapsMaintenanceTo503(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler)}
	rec := httptest.NewRecorder()
	s.serverError(rec, "mark read", fmt.Errorf("wrapped: %w", store.ErrMaintenance))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "15", rec.Header().Get("Retry-After"))
	require.JSONEq(t, `{"error":"maintenance"}`, rec.Body.String())

	rec = httptest.NewRecorder()
	s.serverError(rec, "mark read", errors.New("disk on fire"))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Empty(t, rec.Header().Get("Retry-After"))
}
