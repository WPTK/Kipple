package store

import "time"

// Feed status vocabulary (design §4.6). One function decides it for both
// /api/bootstrap and /api/health/feeds.
const (
	StatusArchive    = "archive"
	StatusDead       = "dead"
	StatusDisabled   = "disabled"
	StatusFailing    = "failing"
	StatusErroring   = "erroring"
	StatusThrottled  = "throttled"
	StatusRedirect   = "redirecting"
	StatusSilent     = "silent"
	StatusOK         = "ok"
	failingAt        = 14 // consecutive failures at which "erroring" becomes "failing"
	silentAfter      = 90 * 24 * time.Hour
	reasonGone       = "gone"
	reasonArchive    = "archive"
	redirectPermKind = "permanent"
)

// StatusRow is the slice of a feed row FeedStatus needs.
type StatusRow struct {
	DisabledReason      *string
	Enabled             bool
	ConsecutiveFailures int64
	RedirectKind        *string // "permanent", "temporary" or nil
	RedirectTo          *string
	// LastNewItemsAt is when the feed last produced a new item, or its creation
	// time when it never has (so a new feed is not "silent").
	LastNewItemsAt int64
}

// FeedStatus returns one feed's health status, first match wins:
// archive, dead, disabled, failing (>= 14 failures), erroring (1-13),
// throttled (hostUntil in the future), redirecting (a permanent redirect
// pending; a temporary one is only a notice), silent (healthy but no new items
// for 90 days), ok. hostUntil is the zero time when the host is not held.
func FeedStatus(r StatusRow, hostUntil, now time.Time) string {
	switch {
	case r.DisabledReason != nil && *r.DisabledReason == reasonArchive:
		return StatusArchive
	case r.DisabledReason != nil && *r.DisabledReason == reasonGone:
		return StatusDead
	case r.DisabledReason != nil || !r.Enabled:
		return StatusDisabled // "user", any other reason, or enabled=0 without one
	case r.ConsecutiveFailures >= failingAt:
		return StatusFailing
	case r.ConsecutiveFailures > 0:
		return StatusErroring
	case hostUntil.After(now):
		return StatusThrottled
	case r.RedirectKind != nil && *r.RedirectKind == redirectPermKind && r.RedirectTo != nil:
		return StatusRedirect
	case r.LastNewItemsAt > 0 && r.LastNewItemsAt < now.Add(-silentAfter).Unix():
		return StatusSilent
	}
	return StatusOK
}

// StatusEnv is what feed listings need beyond the row: the current time and the
// scheduler's per-host deadlines (host -> until; nil = none held).
type StatusEnv struct {
	Now       time.Time
	HostUntil map[string]time.Time
}
