package imgproxy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The per-host limit counts a site: two names of one site share a slot, another site has its own.
func TestHostLimiterCountsASiteNotAHostName(t *testing.T) {
	l := newHostLimiter(1)
	ctx := context.Background()
	release, ok := l.acquire(ctx, "a.one.test", nil)
	require.True(t, ok)

	_, ok = l.acquire(ctx, "b.one.test", time.After(50*time.Millisecond))
	require.False(t, ok, "another name of the same site waits for the slot")
	other, ok := l.acquire(ctx, "c.two.test", nil)
	require.True(t, ok, "another site has a slot of its own")
	other()

	release()
	again, ok := l.acquire(ctx, "B.One.Test", nil)
	require.True(t, ok, "the slot is free once the first fetch ends")
	again()
	require.Zero(t, l.active())
}
