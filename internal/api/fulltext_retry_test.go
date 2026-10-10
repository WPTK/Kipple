package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFulltextTransientFailureRetriesAfterAnHour(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	site := newFTSite(t)
	site.fail.Store(true) // HTTP 500: transient
	_, id := h.ftItem(site, true, true)
	url := "/api/items/" + sid(id) + "/fulltext"

	_, body, _ := h.api(c, "POST", url, "")
	require.Equal(t, "error", body["status"])
	require.EqualValues(t, 1, site.hits.Load())
	var class string
	require.NoError(t, h.db.Reader().QueryRow("SELECT error_class FROM item_fulltext WHERE item_id = ?", id).Scan(&class))
	require.Equal(t, "transient", class)

	h.clk.Advance(59 * time.Minute)
	_, body, _ = h.api(c, "POST", url, "")
	require.Equal(t, "error", body["status"])
	require.EqualValues(t, 1, site.hits.Load(), "still sticky inside the hour")

	// Past the hour it retries by itself; still failing, so the clock restarts.
	h.clk.Advance(2 * time.Minute)
	_, body, _ = h.api(c, "POST", url, "")
	require.Equal(t, "error", body["status"])
	require.EqualValues(t, 2, site.hits.Load())
	h.clk.Advance(30 * time.Minute)
	h.api(c, "POST", url, "")
	require.EqualValues(t, 2, site.hits.Load(), "the attempt time moved forward")

	// Once the site is back, the next retry heals the item.
	site.fail.Store(false)
	h.clk.Advance(61 * time.Minute)
	_, body, _ = h.api(c, "POST", url, "")
	require.Equal(t, "ok", body["status"])
	require.Contains(t, body["content_html"], "quick brown fox")
	require.EqualValues(t, 3, site.hits.Load())
	var errCol, cls *string
	require.NoError(t, h.db.Reader().QueryRow("SELECT error, error_class FROM item_fulltext WHERE item_id = ?", id).Scan(&errCol, &cls))
	require.Nil(t, errCol)
	require.Nil(t, cls)
}

func TestFulltextPermanentAndUnclassifiedFailuresStaySticky(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusNotFound)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t)
	c := h.login()
	site := &ftSite{Server: srv}
	_, id := h.ftItem(site, true, true)
	url := "/api/items/" + sid(id) + "/fulltext"

	_, body, _ := h.api(c, "POST", url, "")
	require.Equal(t, "error", body["status"])
	require.Contains(t, body["error"], "HTTP 404")
	var class string
	require.NoError(t, h.db.Reader().QueryRow("SELECT error_class FROM item_fulltext WHERE item_id = ?", id).Scan(&class))
	require.Equal(t, "permanent", class)
	h.clk.Advance(48 * time.Hour)
	h.api(c, "POST", url, "")
	require.EqualValues(t, 1, hits.Load(), "a 404 is never retried on its own")

	// An error stored before the class column existed reads as permanent.
	status.Store(http.StatusInternalServerError)
	h.exec("UPDATE item_fulltext SET error = 'the page answered HTTP 500', error_class = NULL WHERE item_id = ?", id)
	h.clk.Advance(48 * time.Hour)
	_, body, _ = h.api(c, "POST", url, "")
	require.Equal(t, "error", body["status"])
	require.EqualValues(t, 1, hits.Load(), "unknown class is permanent")

	// refresh=1 always retries, whatever the class or age.
	h.api(c, "POST", url+"?refresh=1", "")
	require.EqualValues(t, 2, hits.Load())
}
