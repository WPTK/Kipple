package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFeedStatusTable(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	s := func(v string) *string { return &v }
	fresh := now.Add(-time.Hour).Unix()
	stale := now.Add(-91 * 24 * time.Hour).Unix()
	future := now.Add(time.Minute)
	ok := StatusRow{Enabled: true, LastNewItemsAt: fresh}
	with := func(f func(*StatusRow)) StatusRow { r := ok; f(&r); return r }

	for _, tc := range []struct {
		name  string
		row   StatusRow
		until time.Time
		want  string
	}{
		{"healthy", ok, time.Time{}, "ok"},
		{"archive beats everything", with(func(r *StatusRow) { r.DisabledReason = s("archive"); r.ConsecutiveFailures = 20 }), future, "archive"},
		{"gone is dead", with(func(r *StatusRow) { r.Enabled = false; r.DisabledReason = s("gone") }), time.Time{}, "dead"},
		{"user is disabled", with(func(r *StatusRow) { r.Enabled = false; r.DisabledReason = s("user") }), time.Time{}, "disabled"},
		{"enabled=0 without a reason is disabled", with(func(r *StatusRow) { r.Enabled = false }), time.Time{}, "disabled"},
		{"disabled beats failing", with(func(r *StatusRow) { r.Enabled = false; r.ConsecutiveFailures = 30 }), time.Time{}, "disabled"},
		{"14 failures is failing", with(func(r *StatusRow) { r.ConsecutiveFailures = 14 }), time.Time{}, "failing"},
		{"13 failures is erroring", with(func(r *StatusRow) { r.ConsecutiveFailures = 13 }), time.Time{}, "erroring"},
		{"one failure is erroring", with(func(r *StatusRow) { r.ConsecutiveFailures = 1 }), time.Time{}, "erroring"},
		{"erroring beats throttled", with(func(r *StatusRow) { r.ConsecutiveFailures = 1 }), future, "erroring"},
		{"host held is throttled", ok, future, "throttled"},
		{"an expired hold is not throttled", ok, now.Add(-time.Second), "ok"},
		{"throttled beats redirecting", with(func(r *StatusRow) { r.RedirectKind, r.RedirectTo = s("permanent"), s("https://b/") }), future, "throttled"},
		{"permanent redirect pending", with(func(r *StatusRow) { r.RedirectKind, r.RedirectTo = s("permanent"), s("https://b/") }), time.Time{}, "redirecting"},
		{"temporary redirect is only a notice", with(func(r *StatusRow) { r.RedirectKind, r.RedirectTo = s("temporary"), s("https://b/") }), time.Time{}, "ok"},
		{"permanent without a target is not pending", with(func(r *StatusRow) { r.RedirectKind = s("permanent") }), time.Time{}, "ok"},
		{"no new items for 91 days is silent", with(func(r *StatusRow) { r.LastNewItemsAt = stale }), time.Time{}, "silent"},
		{"redirecting beats silent", with(func(r *StatusRow) {
			r.LastNewItemsAt = stale
			r.RedirectKind, r.RedirectTo = s("permanent"), s("https://b/")
		}), time.Time{}, "redirecting"},
		{"89 days is not silent", with(func(r *StatusRow) { r.LastNewItemsAt = now.Add(-89 * 24 * time.Hour).Unix() }), time.Time{}, "ok"},
		{"no timestamp at all is not silent", with(func(r *StatusRow) { r.LastNewItemsAt = 0 }), time.Time{}, "ok"},
	} {
		require.Equal(t, tc.want, FeedStatus(tc.row, tc.until, now), tc.name)
	}
}
