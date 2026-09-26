package fetch

import (
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestFailureDelayTable(t *testing.T) {
	m := func(mins int) int64 { return int64(mins) * 60 }
	// 30 min: 30 m, 1 h, 2 h, 4 h, 8 h, 16 h, then 24 h.
	want30 := []int64{1800, 3600, 7200, 14400, 28800, 57600, 86400, 86400, 86400, 86400}
	for i, w := range want30 {
		require.Equal(t, w, FailureDelaySeconds(m(30), i+1), "30m n=%d", i+1)
	}
	// 5 min
	want5 := []int64{300, 600, 1200, 2400, 4800, 9600, 19200, 38400, 76800, 86400}
	for i, w := range want5 {
		require.Equal(t, w, FailureDelaySeconds(m(5), i+1), "5m n=%d", i+1)
	}
	// 7 days: always 7 days, a failing feed never polls faster than a healthy one.
	for n := 1; n <= 3; n++ {
		require.EqualValues(t, 7*86400, FailureDelaySeconds(m(10080), n))
	}
	require.EqualValues(t, 86400, FailureDelaySeconds(1800, 1000), "no overflow at large n")
}

func TestJitterBounds(t *testing.T) {
	now := t0
	lo, hi := func() float64 { return 0 }, func() float64 { return 0.999999999 }
	next, d := NextOnFailure(now, 1800, 1, 0, time.Time{}, lo)
	require.EqualValues(t, 1530, d) // 0.85 x 1800
	require.Equal(t, now.Add(1530*time.Second), next)
	_, d = NextOnFailure(now, 1800, 1, 0, time.Time{}, hi)
	require.EqualValues(t, 2070, d) // 1.15 x 1800
	_, d = NextOnFailure(now, 1800, 7, 0, time.Time{}, lo)
	require.EqualValues(t, 73440, d) // 0.85 x 86400

	_, d = NextOnSuccess(now, 1800, 0, lo)
	require.EqualValues(t, 1710, d) // 0.95
	_, d = NextOnSuccess(now, 1800, 0, hi)
	require.EqualValues(t, 1890, d) // 1.05
}

func TestHostUntilAndRetryAfterDominate(t *testing.T) {
	half := func() float64 { return 0.5 }
	until := t0.Add(5 * time.Hour)
	next, d := NextOnFailure(t0, 1800, 1, 0, until, half)
	require.Equal(t, until, next)
	require.EqualValues(t, 1800, d, "current_delay_s stays the jittered backoff")

	next, _ = NextOnFailure(t0, 1800, 1, 3*time.Hour, until, half)
	require.Equal(t, until, next, "the later of retry-after and host deadline")
	next, _ = NextOnFailure(t0, 1800, 1, 6*time.Hour, until, half)
	require.Equal(t, t0.Add(6*time.Hour), next)
	// a backoff longer than the deadline wins
	next, _ = NextOnFailure(t0, 1800, 7, 0, t0.Add(time.Hour), half)
	require.Equal(t, t0.Add(86400*time.Second), next)
}

func TestSuccessScheduleHints(t *testing.T) {
	half := func() float64 { return 0.5 }
	// hint below the interval loses; above wins; capped at 24 h.
	_, d := NextOnSuccess(t0, 1800, 600, half)
	require.EqualValues(t, 1800, d)
	_, d = NextOnSuccess(t0, 1800, 7200, half)
	require.EqualValues(t, 7200, d)
	_, d = NextOnSuccess(t0, 1800, 10*86400, half)
	require.EqualValues(t, 86400, d)
	// a 7-day interval is never shortened by a hint
	_, d = NextOnSuccess(t0, 7*86400, 3600, half)
	require.EqualValues(t, 7*86400, d)
}

func TestPublisherHint(t *testing.T) {
	h := func(kv ...string) http.Header {
		hh := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			hh.Set(kv[i], kv[i+1])
		}
		return hh
	}
	require.EqualValues(t, 0, PublisherHintSeconds(false, 90, h("Cache-Control", "max-age=9999"), t0), "setting off")
	require.EqualValues(t, 5400, PublisherHintSeconds(true, 90, h(), t0), "rss ttl x 60")
	require.EqualValues(t, 3000, PublisherHintSeconds(true, 0, h("Cache-Control", "public, max-age=3600", "Age", "600"), t0))
	require.EqualValues(t, 7200, PublisherHintSeconds(true, 0, h("Expires", t0.Add(2*time.Hour).Format(http.TimeFormat)), t0))
	require.EqualValues(t, 7200, PublisherHintSeconds(true, 0,
		h("Cache-Control", "max-age=600", "Expires", t0.Add(2*time.Hour).Format(http.TimeFormat)), t0), "the max of the three")
	require.EqualValues(t, 0, PublisherHintSeconds(true, 0, h("Expires", "0"), t0), "Expires: 0 is no hint")
	require.EqualValues(t, 0, PublisherHintSeconds(true, 0, h("Cache-Control", "no-cache, max-age=3600"), t0))
	require.EqualValues(t, 0, PublisherHintSeconds(true, 0, h("Cache-Control", "private, max-age=3600"), t0), "private")
	require.EqualValues(t, 0, PublisherHintSeconds(true, 0, h("Cache-Control", "max-age=3600, private"), t0), "private")
	require.EqualValues(t, 5400, PublisherHintSeconds(true, 90, h("Cache-Control", "private, max-age=9999"), t0), "private keeps the rss ttl")
	require.EqualValues(t, 0, PublisherHintSeconds(true, 0, h("Cache-Control", "max-age=10", "Age", "50"), t0), "never negative")
}

func TestParseRetryAfter(t *testing.T) {
	require.Equal(t, 300*time.Second, ParseRetryAfter("300", t0, time.Time{}))
	require.Equal(t, 1500*time.Second, ParseRetryAfter("", t0, time.Time{}))
	require.Equal(t, 1500*time.Second, ParseRetryAfter("soon", t0, time.Time{}))
	require.Equal(t, 60*time.Second, ParseRetryAfter("0", t0, time.Time{}))
	require.Equal(t, 24*time.Hour, ParseRetryAfter("9999999", t0, time.Time{}))
	require.Equal(t, time.Hour, ParseRetryAfter(t0.Add(time.Hour).Format(http.TimeFormat), t0, time.Time{}))
	require.Equal(t, 60*time.Second, ParseRetryAfter(t0.Add(-time.Hour).Format(http.TimeFormat), t0, time.Time{}), "a past date clamps up")

	// An HTTP-date is relative to the response's Date header, so a publisher clock
	// that is 3 hours fast still gets the intended one hour.
	fast := t0.Add(3 * time.Hour)
	require.Equal(t, time.Hour, ParseRetryAfter(fast.Add(time.Hour).Format(http.TimeFormat), t0, fast))
	require.Equal(t, 4*time.Hour, ParseRetryAfter(fast.Add(time.Hour).Format(http.TimeFormat), t0, time.Time{}), "without a Date header it is measured against now")
	require.Equal(t, 300*time.Second, ParseRetryAfter("300", t0, fast), "delta-seconds ignore Date")
}

func TestDecideRedirect(t *testing.T) {
	p := func(from, to string, status int) Hop { return Hop{Status: status, From: from, To: to} }
	feed := "https://a.example/feed"

	// 301 x3 then migrate
	final := "https://b.example/feed"
	hops := []Hop{p(feed, final, 301)}
	d := DecideRedirect(feed, RedirectState{}, final, hops)
	require.Equal(t, RedirectDecision{Action: RedirectSet, To: final, Kind: "permanent", Count: 1}, d)
	d = DecideRedirect(feed, RedirectState{To: final, Kind: "permanent", Count: 1}, final, hops)
	require.Equal(t, RedirectDecision{Action: RedirectSet, To: final, Kind: "permanent", Count: 2}, d)
	d = DecideRedirect(feed, RedirectState{To: final, Kind: "permanent", Count: 2}, final, hops)
	require.Equal(t, RedirectMigrate, d.Action)

	// a changing target resets the count
	d = DecideRedirect(feed, RedirectState{To: "https://old.example/f", Kind: "permanent", Count: 2}, final, hops)
	require.Equal(t, 1, d.Count)
	require.Equal(t, RedirectSet, d.Action)

	// same-host http -> https migrates at once
	d = DecideRedirect("http://a.example/feed?x=1", RedirectState{}, "https://a.example/feed?x=1",
		[]Hop{p("http://a.example/feed?x=1", "https://a.example/feed?x=1", 301)})
	require.Equal(t, RedirectMigrate, d.Action)
	// ...but not when the path changed as well
	d = DecideRedirect("http://a.example/feed", RedirectState{}, "https://a.example/new",
		[]Hop{p("http://a.example/feed", "https://a.example/new", 301)})
	require.Equal(t, RedirectSet, d.Action)

	// 302 never persists as permanent
	d = DecideRedirect(feed, RedirectState{To: final, Kind: "permanent", Count: 2}, final, []Hop{p(feed, final, 302)})
	require.Equal(t, RedirectDecision{Action: RedirectSet, To: final, Kind: "temporary", Count: 0}, d)

	// https -> http downgrade is refused (temporary, never migrates)
	dn := "http://a.example/feed2"
	d = DecideRedirect(feed, RedirectState{To: dn, Kind: "permanent", Count: 2}, dn, []Hop{p(feed, dn, 301)})
	require.Equal(t, "temporary", d.Kind)
	require.Equal(t, RedirectSet, d.Action)

	// mixed 301 + 302 chain counts as temporary
	d = DecideRedirect(feed, RedirectState{}, final, []Hop{p(feed, "https://m.example/x", 301), p("https://m.example/x", final, 302)})
	require.Equal(t, "temporary", d.Kind)

	// final == url clears
	require.Equal(t, RedirectClear, DecideRedirect(feed, RedirectState{To: final}, "HTTPS://A.example:443/feed", nil).Action)
}

func TestClassifyTooLargeNamesTheRealLimit(t *testing.T) {
	for limit, want := range map[int64]string{
		10 << 20:  "response larger than 10 MiB",
		3 << 20:   "response larger than 3 MiB",
		512 << 10: "response larger than 512 KiB",
		1000:      "response larger than 1000 bytes",
	} {
		class, msg := Classify(&http.MaxBytesError{Limit: limit})
		require.Equal(t, ClassTooLarge, class)
		require.Equal(t, want, msg)
	}
}
