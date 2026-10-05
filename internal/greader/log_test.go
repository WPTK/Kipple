package greader

import (
	"bytes"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func debugLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestRequestLogKeysOnlyByDefault(t *testing.T) {
	var buf bytes.Buffer
	h := newHarness(t, harnessOpts{logger: debugLogger(&buf)})
	h.api.routes["echo-write"] = route{post: true, h: func(c *call) { c.ok() }}
	login(h, "owner", testPass)
	h.do(http.MethodPost, base+rd+"echo-write?client=x", "T="+h.tok+"&i=SECRETID&a=user/-/state/com.google/read", map[string]string{"User-Agent": "ClientX/5"})

	out := buf.String()
	require.Contains(t, out, `"msg":"greader request"`)
	require.Contains(t, out, `"form_keys":["T","a","i"]`)
	require.Contains(t, out, `"query_keys":["client"]`)
	require.Contains(t, out, `"ua":"ClientX/5"`)
	require.Contains(t, out, `"status":200`)
	require.Contains(t, out, `"duration_ms"`)
	require.NotContains(t, out, testPass, "password never logged")
	require.NotContains(t, out, h.tok, "token never logged")
	require.NotContains(t, out, "SECRETID", "values are not logged without KIPPLE_LOG_GREADER_FORMS")
	require.NotContains(t, out, "form_values")
}

func TestRequestLogFormValuesGatedAndRedacted(t *testing.T) {
	var buf bytes.Buffer
	h := newHarness(t, harnessOpts{logger: debugLogger(&buf), logForms: true})
	h.api.routes["echo-write"] = route{post: true, h: func(c *call) { c.ok() }}
	login(h, "owner", testPass)
	h.do(http.MethodPost, base+rd+"echo-write", "T="+h.tok+"&i=VISIBLEID&a=user/-/state/com.google/read", nil)

	out := buf.String()
	require.Contains(t, out, "VISIBLEID")
	require.Contains(t, out, "[redacted]")
	require.NotContains(t, out, testPass)
	require.NotContains(t, out, h.tok)
}

func TestNoRequestLogAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	h := newHarness(t, harnessOpts{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))})
	h.get(rd + "token")
	require.NotContains(t, buf.String(), "greader request")
}

func TestZeroIDsFromNonEmptyBodyWarns(t *testing.T) {
	var buf bytes.Buffer
	h := newHarness(t, harnessOpts{logger: debugLogger(&buf)})
	h.api.routes["echo-write"] = route{post: true, h: func(c *call) { c.warnNoIDs(len(c.p.All("i"))); c.ok() }}
	h.do(http.MethodPost, base+rd+"echo-write", "T="+h.tok+"&a=x", map[string]string{"User-Agent": "Weird/1"})
	require.Contains(t, buf.String(), `"level":"WARN"`)
	require.Contains(t, buf.String(), "zero ids")
}
