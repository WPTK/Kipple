package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/store"
)

type settingIssue struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	s.writeSettings(w, r.Context(), http.StatusOK)
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
		spec, known := settingDefByKey[k]
		switch {
		case !known && strings.HasPrefix(k, "sys."):
			issues = append(issues, settingIssue{k, "read-only setting"})
		case !known:
			issues = append(issues, settingIssue{k, "unknown setting"})
		case spec.envOverride && envOverridden(k):
			issues = append(issues, settingIssue{k, "set by the " + envVarOf(k) + " environment variable; remove it to choose here"})
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
	if err := s.writeAccountSettings(ctx, set); err != nil {
		var ve *store.SavedSearchError
		if errors.As(err, &ve) {
			// A saved search names a feed or folder that does not exist (checked in the write).
			msg := ve.Field + ": " + ve.Message
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid_settings", "message": "invalid settings: " + store.SettingSavedSearches, "keys": []string{store.SettingSavedSearches},
				"issues": []settingIssue{{store.SettingSavedSearches, msg}}})
			return
		}
		s.serverError(w, "settings", err)
		return
	}
	if _, ok := set[store.SettingSavedSearches]; ok {
		s.publishSavedSearchesChanged() // a replaced or reset list, like every other change to it
	}
	after := s.db.FetchSettings(ctx)
	if _, ok := set["imgproxy.mode"]; ok {
		s.refreshImgMode(ctx) // the CSP img-src follows it
	}
	_, hosts := set[store.SettingAllowedHosts]
	_, lan := set[store.SettingOpenLAN]
	if hosts || lan {
		s.invalidateMode() // the Host gate and the open gate read them
	}
	if _, ok := set["imgproxy.cache_mb"]; ok {
		s.applyImgCacheCap(ctx) // a lower cap evicts, 0 turns the cache off and purges it
	}
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
	s.writeSettings(w, ctx, http.StatusOK)
}

// writeSettings answers GET/PATCH /api/settings: every user-visible key with its
// current value and the metadata a UI needs to render it (settingsmeta.go).
func (s *Server) writeSettings(w http.ResponseWriter, ctx context.Context, code int) {
	merged, err := s.db.MergedSettings(ctx)
	if err != nil {
		s.serverError(w, "settings", err)
		return
	}
	out := make([]settingView, 0, len(settingDefs))
	for _, d := range settingDefs {
		v := settingView{settingDef: d, Value: merged[d.Key], Default: store.DefaultSettings[d.Key]}
		if d.envOverride {
			v.EnvOverride = json.RawMessage("null")
			if name, ok := envValue(d.Key); ok {
				b, err := json.Marshal(name)
				if err != nil {
					s.serverError(w, "settings", err)
					return
				}
				v.EnvOverride = b
			}
		}
		out = append(out, v)
	}
	writeJSON(w, code, map[string]any{"settings": out, "values": merged})
}

// envValue is the environment variable that overrides an envOverride setting,
// when it is set. Only tz has one (TZ, docs/setup-wizard-design.md 7a).
func envValue(key string) (string, bool) {
	if key == store.SettingTZ {
		return store.EnvZone()
	}
	return "", false
}

func envOverridden(key string) bool {
	_, ok := envValue(key)
	return ok
}

func envVarOf(key string) string {
	if key == store.SettingTZ {
		return "TZ"
	}
	return "an"
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
