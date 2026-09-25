package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/store"
)

// settingSpec validates one user-writable setting (design §2.2 whitelist). check
// returns the normalized value to store, or a message when v is unacceptable.
type settingSpec struct {
	check func(v any) (any, string)
}

var (
	retentionValues = map[int]bool{0: true, 50: true, 100: true, 250: true, 500: true, 1000: true}
	uiThemes        = []string{"white", "off-white", "sepia", "soft-green", "brown", "dark", "system"}
	// uiFonts are the bundled and system faces of CLAUDE.md; "" = the platform default.
	uiFonts = []string{"", "Literata", "Charter", "Vollkorn", "Gentium Book Plus", "Source Serif 4", "Arvo",
		"Inter", "Manrope", "Source Sans 3", "JetBrains Mono", "Source Code Pro",
		"New York", "SF Pro", "SF Mono", "Georgia", "Menlo"}
)

const maxLayoutsBytes = 4096

func intIn(lo, hi int) func(any) (any, string) {
	return func(v any) (any, string) {
		f, ok := v.(float64)
		if !ok || f != math.Trunc(f) || f < float64(lo) || f > float64(hi) {
			return nil, fmt.Sprintf("must be an integer from %d to %d", lo, hi)
		}
		return int(f), ""
	}
}

func boolVal(v any) (any, string) {
	b, ok := v.(bool)
	if !ok {
		return nil, "must be true or false"
	}
	return b, ""
}

func oneOf(vals ...string) func(any) (any, string) {
	return func(v any) (any, string) {
		if s, ok := v.(string); ok {
			for _, w := range vals {
				if s == w {
					return s, ""
				}
			}
		}
		q := make([]string, 0, len(vals))
		for _, w := range vals {
			q = append(q, fmt.Sprintf("%q", w))
		}
		return nil, "must be one of " + strings.Join(q, ", ")
	}
}

var settingSpecs = map[string]settingSpec{
	"refresh.interval_minutes": {intIn(5, 1440)},
	"retention.default": {func(v any) (any, string) {
		if f, ok := v.(float64); ok && f == math.Trunc(f) && retentionValues[int(f)] {
			return int(f), ""
		}
		return nil, "must be 50, 100, 250, 500, 1000 or 0 (unlimited)"
	}},
	"retention.restore_days": {intIn(0, store.MaxRestoreDays)},
	"fetch.user_agent": {func(v any) (any, string) {
		s, ok := v.(string)
		if !ok || len(s) > 200 || strings.ContainsAny(s, "\r\n\x00") {
			return nil, "must be a string of at most 200 characters without line breaks"
		}
		return strings.TrimSpace(s), ""
	}},
	"fetch.honor_publisher_ttl":        {boolVal},
	"greader.icon_urls":                {boolVal},
	"greader.ot_includes_user_changes": {boolVal},
	"greader.subscribe_fetch_now":      {boolVal},
	"stats.api_single_read_is_open":    {boolVal},
	"imgproxy.mode":                    {oneOf("http_only", "all")},
	"tz": {func(v any) (any, string) {
		s, ok := v.(string)
		if !ok || s == "" || s == "Local" || len(s) > 64 {
			return nil, "must be an IANA time zone name such as America/New_York"
		}
		if _, err := time.LoadLocation(s); err != nil {
			return nil, "unknown time zone"
		}
		return s, ""
	}},
	"ui.theme":               {oneOf(uiThemes...)},
	"ui.font_body":           {oneOf(uiFonts...)},
	"ui.font_ui":             {oneOf(uiFonts...)},
	"ui.font_size":           {intIn(12, 32)},
	"ui.content_width":       {intIn(400, 1400)},
	"ui.mark_read_on_scroll": {boolVal},
	"ui.line_height": {func(v any) (any, string) {
		f, ok := v.(float64)
		if !ok || f < 1.1 || f > 2.5 {
			return nil, "must be a number from 1.1 to 2.5"
		}
		return f, ""
	}},
	"ui.layouts": {func(v any) (any, string) {
		m, ok := v.(map[string]any)
		if ok {
			for k, x := range m {
				if s, isStr := x.(string); !isStr || len(k) > 64 || len(s) > 32 {
					ok = false
				}
			}
			if b, _ := json.Marshal(m); len(b) > maxLayoutsBytes {
				ok = false
			}
		}
		if !ok {
			return nil, "must be an object of short string values (at most 4 KB)"
		}
		return m, ""
	}},
}

type settingIssue struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.db.MergedSettings(r.Context())
	if err != nil {
		s.serverError(w, "settings", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// patchSettings is PATCH /api/settings: all-or-nothing. A null value resets the
// key to its default.
func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeBody(w, r, &body, false) {
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var issues []settingIssue
	set := make(map[string]any, len(body))
	for _, k := range keys {
		spec, known := settingSpecs[k]
		switch {
		case !known && strings.HasPrefix(k, "sys."):
			issues = append(issues, settingIssue{k, "read-only setting"})
		case !known:
			issues = append(issues, settingIssue{k, "unknown setting"})
		case body[k] == nil:
			set[k] = nil
		default:
			v, msg := spec.check(body[k])
			if msg != "" {
				issues = append(issues, settingIssue{k, msg})
			} else {
				set[k] = v
			}
		}
	}
	if len(issues) > 0 {
		bad := make([]string, len(issues))
		for i, is := range issues {
			bad[i] = is.Key
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid_settings", "message": "invalid settings: " + strings.Join(bad, ", "), "keys": bad, "issues": issues})
		return
	}
	ctx := r.Context()
	before := s.db.FetchSettings(ctx)
	if err := s.db.SetSettings(ctx, set); err != nil {
		s.serverError(w, "settings", err)
		return
	}
	after := s.db.FetchSettings(ctx)
	if before.IntervalMinutes != after.IntervalMinutes {
		// Reads are uncached, so new schedules already use it; pull due times in
		// for a lowered interval and nudge the dispatcher.
		if _, err := s.db.PullInSchedule(ctx, after.IntervalMinutes); err != nil {
			s.log.Warn("api: settings: reschedule", "err", err)
		}
		s.opt.Sched.Wake()
	}
	if before.RetentionDefault != after.RetentionDefault {
		if _, err := s.opt.Sched.ApplyRetention(false); err != nil && !errors.Is(err, sched.ErrStopped) {
			s.log.Warn("api: settings: retention run", "err", err)
		}
	}
	merged, err := s.db.MergedSettings(ctx)
	if err != nil {
		s.serverError(w, "settings", err)
		return
	}
	writeJSON(w, http.StatusOK, merged)
}

// retentionApply is POST /api/retention/apply: "Apply retention now" over every feed.
func (s *Server) retentionApply(w http.ResponseWriter, r *http.Request) {
	info, err := s.opt.Sched.ApplyRetention(true)
	if err != nil {
		if errors.Is(err, sched.ErrStopped) {
			writeError(w, http.StatusServiceUnavailable, "shutting_down")
			return
		}
		s.serverError(w, "retention apply", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": fmt.Sprint(info.RunID), "total": info.Total})
}
