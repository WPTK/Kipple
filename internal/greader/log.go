package greader

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// statusWriter records the response status for the request log and keeps
// http.Flusher / ResponseController working for streamed responses.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.NewResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// redactKeys never have their values logged, whatever the settings.
var redactKeys = map[string]bool{
	"passwd": true, "t": true, "auth": true, "sid": true, "lsid": true, "authorization": true,
}

const (
	logMaxValues   = 3
	logMaxValueLen = 200
)

// logRequest is the debug-level request log (design §6.1): method, path, query
// and form KEYS, user agent, content type, status and duration. Values are
// logged only with KIPPLE_LOG_GREADER_FORMS, truncated, and never for the
// password or any token.
func (a *API) logRequest(c *call, dur time.Duration) {
	ctx := c.r.Context()
	if !a.log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	attrs := []any{
		"method", c.r.Method, "path", c.path, "ua", c.r.UserAgent(),
		"content_type", c.r.Header.Get("Content-Type"),
		"status", c.w.status, "duration_ms", float64(dur.Microseconds()) / 1000,
	}
	if c.p != nil {
		attrs = append(attrs, "query_keys", c.p.queryKeys(), "form_keys", c.p.bodyKeys())
		if a.opt.LogForms {
			attrs = append(attrs, "form_values", c.p.loggedValues())
		}
	}
	a.log.LogAttrs(ctx, slog.LevelDebug, "greader request", toAttrs(attrs)...)
}

func toAttrs(kv []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, slog.Any(kv[i].(string), kv[i+1]))
	}
	return out
}

// loggedValues returns key -> first few (truncated) values plus a count, with
// secrets replaced by "[redacted]".
func (p *Params) loggedValues() map[string]any {
	out := map[string]any{}
	add := func(pairs []pair, scope string) {
		vals := map[string][]string{}
		for _, e := range pairs {
			vals[e.key] = append(vals[e.key], e.val)
		}
		for k, vs := range vals {
			name := scope + k
			if redactKeys[strings.ToLower(k)] {
				out[name] = "[redacted]"
				continue
			}
			shown := vs
			if len(shown) > logMaxValues {
				shown = shown[:logMaxValues]
			}
			cut := make([]string, len(shown))
			for i, v := range shown {
				if len(v) > logMaxValueLen {
					v = v[:logMaxValueLen] + "..."
				}
				cut[i] = v
			}
			out[name] = map[string]any{"count": len(vs), "values": cut}
		}
	}
	add(p.body, "body.")
	add(p.query, "query.")
	return out
}

// warnNoIDs logs the §6.1 warning when a write endpoint got a body but parsed
// no ids from it.
func (c *call) warnNoIDs(nIDs int) {
	if nIDs == 0 && !c.p.BodyEmpty() {
		c.a.log.LogAttrs(context.Background(), slog.LevelWarn, "greader: write parsed zero ids from a non-empty body",
			slog.String("path", c.path), slog.String("ua", c.r.UserAgent()),
			slog.String("content_type", c.r.Header.Get("Content-Type")))
	}
}
