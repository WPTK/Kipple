package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestApplyTZ(t *testing.T) {
	old := time.Local
	t.Cleanup(func() { time.Local = old })

	require.NoError(t, applyTZ("America/New_York"))
	noon := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	require.Equal(t, 8, noon.In(time.Local).Hour(), "EDT is UTC-4 (tzdata is embedded)")
	require.Equal(t, 7, time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC).In(time.Local).Hour(), "EST is UTC-5")

	require.NoError(t, applyTZ("America/Chicago"))
	require.Equal(t, 7, noon.In(time.Local).Hour())

	before := time.Local
	require.Error(t, applyTZ("Not/AZone"))
	require.Same(t, before, time.Local, "a bad zone leaves time.Local alone")
}
