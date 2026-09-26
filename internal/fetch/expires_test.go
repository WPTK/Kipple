package fetch

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Expires is measured against the response's own Date, as Retry-After is, so a
// publisher clock that is off does not skew the hint.
func TestPublisherHintExpiresRelativeToDate(t *testing.T) {
	fast := t0.Add(3 * time.Hour) // the publisher's clock runs 3 h fast
	h := http.Header{}
	h.Set("Date", fast.Format(http.TimeFormat))
	h.Set("Expires", fast.Add(time.Hour).Format(http.TimeFormat))
	require.EqualValues(t, 3600, PublisherHintSeconds(true, 0, h, t0))

	h.Set("Date", "garbage")
	require.EqualValues(t, 4*3600, PublisherHintSeconds(true, 0, h, t0), "no valid Date: measured against now")
}
