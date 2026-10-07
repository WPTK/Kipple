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

// NextOnSuccess implements design §4.6. A feed's slots are fixed in time,
// interval apart, at a phase taken from phaseKey (PhaseKey), and the next fetch
// is the first slot at or after now + lead. The lead is the publisher hint
// (capped at 24 h) when it is longer than the interval, so the hint is a floor;
// otherwise it is half the interval, so a feed fetched on its slot, or up to
// half an interval late, gets its next slot one interval on. Feeds fetched
// together (a restore, downtime, a refresh of everything) therefore spread over
// the interval within one fetch and stay spread. The grid depends on the
// interval alone, so a hint that changes from fetch to fetch never moves it.
// hintS is the publisher hint in seconds (0 when none).
func NextOnSuccess(now time.Time, phaseKey uint64, intervalS, hintS int64) (time.Time, int64) {
	intervalS = max(intervalS, 1)
	lead := intervalS / 2
	if h := min(hintS, maxBackoffS); h > intervalS {
		lead = h
	}
	earliest := now.Unix() + lead
	t := earliest + posMod(slotPhase(phaseKey, intervalS)-earliest, intervalS)
	d := t - now.Unix()
	return now.Add(time.Duration(d) * time.Second), d
}

// PhaseKey is what places a feed's slots: its id offset by the installation's
// random salt (sys.fetch_slot_salt), so two installations that imported the
// same list, with the same ids, do not fetch a publisher in the same second.
func PhaseKey(feedID, salt int64) uint64 { return uint64(feedID) + uint64(salt) }

// slotPhase is the slot offset in [0, intervalS): Fibonacci hashing of the key,
// so consecutive ids (an import, a restore) land far apart and cover the
// interval evenly.
func slotPhase(key uint64, intervalS int64) int64 {
	frac := float64((key*0x9E3779B97F4A7C15)>>11) / (1 << 53)
	return int64(frac * float64(intervalS))
}

func posMod(a, m int64) int64 {
	r := a % m
	if r < 0 {
		r += m
	}
	return r
}

// PublisherHintSeconds is max(RSS ttl x 60, Cache-Control max-age - Age,
// Expires - Date (Expires - now when the response has no valid Date), 0). It is 0 when honor is false. no-cache/no-store/private
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
	for _, part := range strings.Split(cc, ",") {
		tok, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if tok == "no-cache" || tok == "no-store" || tok == "private" {
			return hint
		}
	}
	if ma, ok := directive(cc, "s-maxage"); ok {
		hint = max(hint, ma-headerAge(h))
	} else if ma, ok := directive(cc, "max-age"); ok {
		hint = max(hint, ma-headerAge(h))
	}
	if e := h.Get("Expires"); e != "" {
		if t, err := http.ParseTime(e); err == nil {
			// Measured against the response's own Date when it has one, as
			// ParseRetryAfter does, so a publisher whose clock is off does not
			// stretch (or cancel) the hint.
			ref := now
			if d := responseDate(h); !d.IsZero() {
				ref = d
			}
			hint = max(hint, int64(t.Sub(ref).Seconds()))
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
// to 1500 s when absent or invalid and clamping to [60 s, 24 h]. An HTTP-date is
// measured against the response's own Date header (respDate, zero when absent
// or unparseable), which tolerates a publisher whose clock is off; without one
// it is measured against now.
func ParseRetryAfter(v string, now, respDate time.Time) time.Duration {
	s := int64(defaultRetryS)
	v = strings.TrimSpace(v)
	if v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			s = n
		} else if t, err := http.ParseTime(v); err == nil {
			ref := now
			if !respDate.IsZero() {
				ref = respDate
			}
			s = int64(math.Ceil(t.Sub(ref).Seconds()))
		}
	}
	s = max(minRetryAfterS, min(s, maxRetryAfterS))
	return time.Duration(s) * time.Second
}
