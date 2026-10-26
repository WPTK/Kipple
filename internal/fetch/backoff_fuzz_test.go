package fetch

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// FuzzRetryAfter: for any header value, clock and response Date, the wait is a whole number of seconds inside
// [60 s, 24 h], and a value that is neither delta-seconds nor an HTTP-date gives the 1500 s default.
func FuzzRetryAfter(f *testing.F) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, v := range []string{
		"", " ", "0", "1", "59", "60", "120", "86400", "86401", "-5", "+30", "9223372036854775807", "9223372036854775808",
		"99999999999999999999", "1e3", "0x10", "1.5", "abc", "Sun, 06 Nov 1994 08:49:37 GMT", "Sunday, 06-Nov-94 08:49:37 GMT",
		"Sun Nov  6 08:49:37 1994", "Mon, 26 Oct 2026 12:00:00 GMT", "Wed, 21 Oct 2015 07:28:00 GMT",
		"Fri, 31 Dec 9999 23:59:59 GMT", "Mon, 01 Jan 0001 00:00:00 GMT",
	} {
		f.Add(v, now.Unix(), int64(0))
		f.Add(v, now.Unix(), now.Add(-time.Hour).Unix())
	}
	f.Fuzz(func(t *testing.T, v string, nowUnix, dateUnix int64) {
		now := time.Unix(nowUnix, 0)
		var date time.Time
		if dateUnix != 0 {
			date = time.Unix(dateUnix, 0)
		}
		d := ParseRetryAfter(v, now, date)
		if d < 60*time.Second || d > 24*time.Hour {
			t.Fatalf("ParseRetryAfter(%q) = %v, outside [1m, 24h]", v, d)
		}
		if d%time.Second != 0 {
			t.Fatalf("ParseRetryAfter(%q) = %v, not whole seconds", v, d)
		}
		if _, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err != nil {
			if _, err := http.ParseTime(strings.TrimSpace(v)); err != nil && d != 1500*time.Second {
				t.Fatalf("ParseRetryAfter(%q) = %v, want the 1500 s default for a value that is neither seconds nor a date", v, d)
			}
		}
	})
}
