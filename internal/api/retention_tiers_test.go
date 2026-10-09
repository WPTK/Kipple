package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// The settings metadata, the retention.default validator and the per-feed PATCH
// all read retentionLimits, so they accept exactly the same values.
func TestRetentionTiersAreDefinedOnce(t *testing.T) {
	var offered []int64
	for _, o := range retentionOptions() {
		offered = append(offered, int64(o.Value.(int)))
	}
	require.Equal(t, []int64{50, 100, 250, 500, 1000, 0}, offered)
	for _, n := range offered {
		require.True(t, isRetentionChoice(n), n)
	}
	for _, n := range []int64{-1, 1, 75, 2000} {
		require.False(t, isRetentionChoice(n), n)
	}
	require.Equal(t, "50, 100, 250, 500 or 1000", retentionChoicesText())
}

// A fetch keeps at most fetch.MaxItemsPerFetch entries; that cut must never reach what a retention setting keeps.
func TestItemCapIsAboveEveryRetentionTier(t *testing.T) {
	require.Greater(t, fetch.MaxItemsPerFetch, retentionLimits[len(retentionLimits)-1])
}
