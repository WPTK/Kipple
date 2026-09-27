package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/backup"
)

// A gated harness: the build blocks in its free-space check until gate is closed.
func slowBackupHarness(t *testing.T, gate chan struct{}, space *atomic.Uint64) *harness {
	return newHarness(t, func(o *Options) {
		o.Backups = backup.New(backup.Options{DB: o.DB, SyncWait: 100 * time.Millisecond,
			FreeBytes: func(string) (uint64, error) {
				if gate != nil {
					<-gate
				}
				if space != nil {
					return space.Load(), nil
				}
				return 1 << 40, nil
			}})
	})
}

func TestBackupJobLifecycle(t *testing.T) {
	gate := make(chan struct{})
	h := slowBackupHarness(t, gate, nil)
	c := h.backupSetup(t)

	// The build outlasts the sync window: 202 with a job id, and the client
	// hanging up does not cancel it.
	ctx, cancel := context.WithCancel(context.Background())
	rec := h.do("POST", "/api/backup", "", withCookie(c), func(r *http.Request) { *r = *r.WithContext(ctx) })
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	_ = rec
	code, body, _ := h.api(c, "POST", "/api/backup", "")
	require.Equal(t, http.StatusConflict, code, "one export at a time: %v", body)
	cancel()

	// decode the 202 answer
	var job string
	{
		require.Contains(t, rec.Body.String(), `"status":"building"`)
		i := strings.Index(rec.Body.String(), `"job_id":"`)
		require.GreaterOrEqual(t, i, 0)
		job = rec.Body.String()[i+len(`"job_id":"`):]
		job = job[:strings.Index(job, `"`)]
	}
	code, body, _ = h.api(c, "GET", "/api/backup/jobs/"+job, "")
	require.Equal(t, 200, code)
	require.Equal(t, "building", body["status"])
	require.NotContains(t, body, "token")

	// session and same-origin are required, the client header too
	require.Equal(t, 401, h.do("GET", "/api/backup/jobs/"+job, "").Code)
	code, _, _ = h.api(c, "GET", "/api/backup/jobs/"+job, "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	require.Equal(t, 403, code)
	code, _, _ = h.api(c, "GET", "/api/backup/jobs/"+job, "", func(r *http.Request) { r.Header.Del("X-Kipple-Client") })
	require.Equal(t, 403, code)
	code, _, _ = h.api(c, "GET", "/api/backup/jobs/nope", "")
	require.Equal(t, 404, code)

	close(gate)
	var ready map[string]any
	require.Eventually(t, func() bool {
		_, b, _ := h.api(c, "GET", "/api/backup/jobs/"+job, "")
		ready = b
		return b["status"] == "ready"
	}, 20*time.Second, 25*time.Millisecond)
	tok := ready["token"].(string)
	require.Equal(t, "/api/backup/"+tok, ready["url"])
	require.Regexp(t, `^kipple-backup-`, ready["filename"])
	require.Greater(t, ready["bytes"], float64(0))
	require.InDelta(t, time.Now().Add(5*time.Minute).Unix(), ready["expires_at"], 10, "TTL counted from ready")
	require.Contains(t, ready["warning"], "password hashes")
	require.EqualValues(t, 1, ready["contents"].(map[string]any)["items"])

	dl := h.do("GET", "/api/backup/"+tok, "", withCookie(c))
	require.Equal(t, 200, dl.Code)
	require.Greater(t, dl.Body.Len(), 0)
	code, _, _ = h.api(c, "GET", "/api/backup/jobs/"+job, "")
	require.Equal(t, 404, code, "gone once downloaded")
}

func TestBackupJobFailure(t *testing.T) {
	var space atomic.Uint64
	space.Store(1 << 20) // too little
	gate := make(chan struct{})
	h := slowBackupHarness(t, gate, &space)
	c := h.backupSetup(t)

	rec := h.do("POST", "/api/backup", "", withCookie(c))
	require.Equal(t, 202, rec.Code)
	i := strings.Index(rec.Body.String(), `"job_id":"`)
	job := rec.Body.String()[i+len(`"job_id":"`):]
	job = job[:strings.Index(job, `"`)]
	close(gate)
	var body map[string]any
	require.Eventually(t, func() bool {
		_, body, _ = h.api(c, "GET", "/api/backup/jobs/"+job, "")
		return body["status"] == "failed"
	}, 20*time.Second, 25*time.Millisecond)
	require.Equal(t, "no_space", body["error"])
	require.Contains(t, body["message"], "not enough free disk space")

}

// A build inside the sync window still answers 200 with the token, as before.
func TestBackupSyncFastPath(t *testing.T) {
	h := newHarness(t)
	c := h.backupSetup(t)
	code, body, _ := h.api(c, "POST", "/api/backup", "")
	require.Equal(t, 200, code, "%v", body)
	require.Equal(t, "ready", body["status"])
	require.NotEmpty(t, body["token"])
}

// HEAD is routed to the GET handler; it must neither spend the token nor skip the origin rule.
func TestBackupHeadDoesNotSpendTheToken(t *testing.T) {
	h := newHarness(t)
	body := fresh(t, h)
	c := body["cookie"].(*http.Cookie)
	tok := body["token"].(string)

	head := func(path string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
		return h.do("HEAD", path, "", append([]func(*http.Request){withCookie(c), func(r *http.Request) { r.Header.Del("X-Kipple-Client") }}, mod...)...)
	}
	cross := func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }
	require.Equal(t, http.StatusForbidden, head("/api/backup/"+tok, cross).Code, "the origin rule covers HEAD")
	require.Equal(t, http.StatusForbidden, head("/api/opml", cross).Code)
	require.Equal(t, http.StatusForbidden, head("/api/stats/export", cross).Code)
	rec := head("/api/backup/" + tok)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, "GET", rec.Header().Get("Allow"))

	dl := h.do("GET", "/api/backup/"+tok, "", withCookie(c), func(r *http.Request) { r.Header.Del("X-Kipple-Client") })
	require.Equal(t, http.StatusOK, dl.Code, "the token is still good after HEAD")
}
