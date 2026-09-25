package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/extract"
)

func TestFtFlightsClearsEntryOnPanic(t *testing.T) {
	var f ftFlights
	require.Panics(t, func() {
		_, _ = f.do(1, func() (extract.Result, error) { panic("boom") })
	})
	f.mu.Lock()
	_, stuck := f.m[1]
	f.mu.Unlock()
	require.False(t, stuck)
	_, err := f.do(1, func() (extract.Result, error) { return extract.Result{}, nil })
	require.NoError(t, err)
}
