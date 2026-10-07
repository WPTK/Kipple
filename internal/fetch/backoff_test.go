package fetch

import (
	"net/http"
	"net/netip"
	"strconv"
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

const testSalt = 0x2545F4914F6CDD1D

// key is the phase key of feed id on the test installation.
func key(id int64) uint64 { return PhaseKey(id, testSalt) }

// gaps runs n successes of feed id, each one made on time, and returns the gaps.
func gaps(id, intervalS, hintS int64, n int) []int64 {
	at, _ := NextOnSuccess(t0, key(id), intervalS, hintS)
	var out []int64
	for range n {
		next, d := NextOnSuccess(at, key(id), intervalS, hintS)
		out, at = append(out, d), next
	}
	return out
}

func TestSuccessScheduleHints(t *testing.T) {
	for id := int64(1); id <= 50; id++ {
		// A hint at or below the interval does not matter.
		require.Equal(t, []int64{1800, 1800, 1800}, gaps(id, 1800, 600, 3))
		require.Equal(t, []int64{1800, 1800, 1800}, gaps(id, 1800, 1800, 3))
		// A longer hint is a floor: the first slot at or after it, so at most
		// one interval later.
		for _, d := range gaps(id, 1800, 7200, 3) {
			require.GreaterOrEqual(t, d, int64(7200))
			require.Less(t, d, int64(7200+1800))
		}
		// The hint counts for at most 24 h.
		for _, d := range gaps(id, 1800, 10*86400, 3) {
			require.GreaterOrEqual(t, d, int64(86400))
			require.Less(t, d, int64(86400+1800))
		}
		// A 7-day interval is never shortened by a hint.
		require.Equal(t, []int64{7 * 86400, 7 * 86400}, gaps(id, 7*86400, 3600, 2))
	}
}

func TestSuccessSlotBounds(t *testing.T) {
	for id := int64(1); id <= 200; id++ {
		for off := int64(0); off < 1800; off += 97 {
			now := t0.Add(time.Duration(off) * time.Second)
			next, d := NextOnSuccess(now, key(id), 1800, 0)
			require.GreaterOrEqual(t, d, int64(900), "id %d: never sooner than half the interval", id)
			require.Less(t, d, int64(2700), "id %d: always sooner than one and a half intervals", id)
			require.Equal(t, now.Add(time.Duration(d)*time.Second), next)
			// The next fetch, made on the slot or up to half an interval late
			// (queueing, a slow publisher), keeps the slot.
			for _, late := range []int64{0, 1, 300, 900} {
				_, d2 := NextOnSuccess(next.Add(time.Duration(late)*time.Second), key(id), 1800, 0)
				require.EqualValues(t, 1800-late, d2, "id %d late %d", id, late)
			}
		}
	}
}

// A feed behind a cache whose Age grows from fetch to fetch has a different
// hint every time. The slots stay put (they come from the interval alone) and
// no fetch comes before a hint longer than the interval runs out.
func TestSuccessVaryingHintKeepsGridAndFloor(t *testing.T) {
	const interval = 1800
	for id := int64(1); id <= 20; id++ {
		at := t0
		phase := int64(-1)
		for k := range 60 {
			age := (int64(k)*613 + id*97) % 7200
			h := http.Header{}
			h.Set("Cache-Control", "public, max-age=7200")
			h.Set("Age", strconv.FormatInt(age, 10))
			hint := PublisherHintSeconds(true, 0, h, at)
			require.EqualValues(t, 7200-age, hint)
			next, d := NextOnSuccess(at, key(id), interval, hint)
			if hint > interval {
				require.GreaterOrEqual(t, d, hint, "id %d fetch %d: never before a hint longer than the interval", id, k)
			} else {
				require.GreaterOrEqual(t, d, int64(interval/2), "id %d fetch %d", id, k)
			}
			if p := next.Unix() % interval; phase < 0 {
				phase = p
			} else {
				require.Equal(t, phase, p, "id %d fetch %d: the slot did not move", id, k)
			}
			at = next.Add(time.Duration(k%7) * time.Second) // a few seconds of lateness
		}
	}
}

// A restored library of 500 feeds is fetched in one burst of a few minutes.
// One fetch later the feeds are spread over the whole interval, and they stay
// there. The old schedule (now + interval x 0.95-1.05) kept them inside about
// eight minutes for good.
func TestSuccessSpreadsABurst(t *testing.T) {
	const n, interval = 500, 1800
	for _, salt := range []int64{0, testSalt} {
		perMinute := map[int64]int{}
		for id := int64(1); id <= n; id++ {
			done := t0.Add(time.Duration(id) * 400 * time.Millisecond) // 500 fetches in 200 s
			next, _ := NextOnSuccess(done, PhaseKey(id, salt), interval, 0)
			again, d := NextOnSuccess(next, PhaseKey(id, salt), interval, 0)
			require.EqualValues(t, interval, d, "id %d stays on its slot", id)
			require.Equal(t, next.Add(interval*time.Second), again)
			perMinute[next.Unix()/60%(interval/60)]++
		}
		require.Len(t, perMinute, interval/60, "salt %d: every minute of the interval has fetches", salt)
		for m, c := range perMinute {
			require.LessOrEqual(t, c, 2*n/(interval/60), "salt %d: minute %d is not a hot spot", salt, m)
		}
	}
}

// Two installations that imported the same list (the same ids) fetch each
// feed at different times.
func TestSlotSaltSeparatesInstallations(t *testing.T) {
	same := 0
	for id := int64(1); id <= 500; id++ {
		a, _ := NextOnSuccess(t0, PhaseKey(id, 12345), 1800, 0)
		b, _ := NextOnSuccess(t0, PhaseKey(id, 987654321), 1800, 0)
		if a.Sub(b).Abs() < time.Minute {
			same++
		}
	}
	require.Less(t, same, 50, "about 1 in 15 by chance, not all of them")
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
