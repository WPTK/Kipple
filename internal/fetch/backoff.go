package fetch

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Rand returns a uniform value in [0, 1). Injected so tests can pin jitter.
type Rand func() float64

const (
	// FailingThreshold is the consecutive-failure count that flags a feed as failing.
	FailingThreshold = 14

	maxBackoffS    = 86400
	defaultRetryS  = 1500
	minRetryAfterS = 60
	maxRetryAfterS = 86400
)

// IntervalSeconds is 60 x the feed's interval in minutes.
func IntervalSeconds(intervalMinutes int) int64 { return int64(intervalMinutes) * 60 }

func jitter(base float64, lo, hi float64, rnd Rand) int64 {
	return int64(math.Round(base * (lo + (hi-lo)*rnd())))
}

// FailureDelaySeconds is the un-jittered backoff for the n-th consecutive
// failure (n counted after incrementing): min(interval x 2^(n-1), max(86400, interval)).
func FailureDelaySeconds(intervalS int64, n int) int64 {
	cap := int64(maxBackoffS)
	if intervalS > cap {
		cap = intervalS
	}
	d := intervalS
	for i := 1; i < n; i++ {
		d *= 2
		if d >= cap {
			return cap
		}
	}
	if d > cap {
		d = cap
	}
	return d
}

// NextOnFailure implements design §4.6: jitter 0.85-1.15 around the backoff,
// then the later of that, now+retryAfter (429/503) and the host deadline.
// It returns the next fetch time and the jittered delay (current_delay_s).
func NextOnFailure(now time.Time, intervalS int64, n int, retryAfter time.Duration, hostUntil time.Time, rnd Rand) (time.Time, int64) {
	d := jitter(float64(FailureDelaySeconds(intervalS, n)), 0.85, 1.15, rnd)
	next := now.Add(time.Duration(d) * time.Second)
	if retryAfter > 0 {
		if t := now.Add(retryAfter); t.After(next) {
			next = t
		}
	}
	if hostUntil.After(next) {
		next = hostUntil
	}
	return next, d
}

// NextOnSuccess implements design §4.6: max(interval, min(hint, 24 h)) with
// +-5 % jitter. hintS is the publisher hint in seconds (0 when none).
func NextOnSuccess(now time.Time, intervalS, hintS int64, rnd Rand) (time.Time, int64) {
	base := intervalS
	if h := min(hintS, maxBackoffS); h > base {
		base = h
	}
	d := jitter(float64(base), 0.95, 1.05, rnd)
	return now.Add(time.Duration(d) * time.Second), d
}

// PublisherHintSeconds is max(RSS ttl x 60, Cache-Control max-age - Age,
// Expires - now, 0). It is 0 when honor is false. no-cache/no-store/private
// responses and an unparseable Expires (such as "0") contribute nothing.
func PublisherHintSeconds(honor bool, ttlMinutes int, h http.Header, now time.Time) int64 {
	if !honor {
		return 0
	}
	hint := int64(0)
	if ttlMinutes > 0 {
		hint = int64(ttlMinutes) * 60
	}
	cc := strings.ToLower(h.Get("Cache-Control"))
	if strings.Contains(cc, "no-cache") || strings.Contains(cc, "no-store") {
		return hint
	}
	if ma, ok := directive(cc, "s-maxage"); ok {
		hint = max(hint, ma-headerAge(h))
	} else if ma, ok := directive(cc, "max-age"); ok {
		hint = max(hint, ma-headerAge(h))
	}
	if e := h.Get("Expires"); e != "" {
		if t, err := http.ParseTime(e); err == nil {
			hint = max(hint, int64(t.Sub(now).Seconds()))
		}
	}
	return hint
}

func headerAge(h http.Header) int64 {
	if a, err := strconv.ParseInt(strings.TrimSpace(h.Get("Age")), 10, 64); err == nil && a > 0 {
		return a
	}
	return 0
}

func directive(cc, name string) (int64, bool) {
	for _, part := range strings.Split(cc, ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, name+"="); ok {
			n, err := strconv.ParseInt(strings.Trim(v, `"`), 10, 64)
			if err == nil && n >= 0 {
				return n, true
			}
		}
	}
	return 0, false
}

// ParseRetryAfter reads Retry-After (delta-seconds or HTTP-date), defaulting
// to 1500 s when absent or invalid and clamping to [60 s, 24 h].
func ParseRetryAfter(v string, now time.Time) time.Duration {
	s := int64(defaultRetryS)
	v = strings.TrimSpace(v)
	if v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			s = n
		} else if t, err := http.ParseTime(v); err == nil {
			s = int64(math.Ceil(t.Sub(now).Seconds()))
		}
	}
	s = max(minRetryAfterS, min(s, maxRetryAfterS))
	return time.Duration(s) * time.Second
}
