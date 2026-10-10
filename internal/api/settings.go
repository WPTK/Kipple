package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/reach"
	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/setup"
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
// key to its default. "current" is not a setting: it is the caller's current
// web password, which a write to the trusted proxies or Cloudflare Access must
// carry (guardedKeys).
func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeBody(w, r, &body, false) {
		return
	}
	current, _ := body["current"].(string)
	delete(body, "current")
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
		case body[k] == nil && slices.Contains(store.ReachKeys, k):
			// A reachability reset stores the default, so the row stays and its seed
			// variable can never fill it again.
			set[k] = store.DefaultSettings[k]
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
	if guarded(set) {
		// Who may name the client's address, and who may sign in: proven like an
		// account change (the web password; Access or the open gate without one).
		if _, ok := s.checkCurrent(w, r, current, false); !ok {
			return
		}
	}
	ctx := r.Context()
	before := s.db.FetchSettings(ctx)
	write := func() error { return s.writeAccountSettings(ctx, set) }
	reachSet := touchesReach(set)
	var err error
	if reachSet {
		// The reachability settings in force change with the write, under one lock.
		err = s.reach.Update(set, func(cur *reach.State) error { return s.checkReachChange(r, cur, set) }, write)
	} else {
		err = write()
	}
	if err != nil {
		if errors.Is(err, errProxyIsYou) {
			writeErrorMsg(w, http.StatusConflict, "proxy_is_you",
				"your own address is in this list: Kipple runs without a password, and it refuses every request from a trusted proxy, so saving it would lock you out. Leave your address out, or set a password first")
			return
		}
		if errors.Is(err, errAccessInUse) {
			writeErrorMsg(w, http.StatusConflict, "access_in_use",
				"this account has no web password and signs in through Cloudflare Access: set a web password first (Account), then change or turn off Access")
			return
		}
		var dup *store.DuplicateSavedSearchError
		if errors.As(err, &dup) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid_settings", "message": "invalid settings: " + store.SettingSavedSearches, "keys": []string{store.SettingSavedSearches},
				"issues": []settingIssue{{store.SettingSavedSearches, "this search is already saved as " + dup.Name}}})
			return
		}
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
	if reachSet {
		// The Host gate and the open gate read them: wake long-lived requests (an
		// /api/events stream) so they re-check the open gate at once.
		s.noteMode(ctx, nil)
	}
	if _, ok := set["imgproxy.cache_mb"]; ok {
		s.applyImgCacheCap(ctx) // a lower cap evicts, 0 turns the cache off and purges it
	}
	if after.IntervalMinutes < before.IntervalMinutes {
		// Reads are uncached, so new schedules already use it (a raised interval
		// applies from each feed's next fetch); pull due times in for a lowered
		// interval and nudge the dispatcher.
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
		out = append(out, v)
	}
	writeJSON(w, code, map[string]any{"settings": out, "values": merged})
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

// errAccessInUse refuses a change to the Cloudflare Access setting while the
// account signs in through it (no web password).
var errAccessInUse = errors.New("cloudflare access in use")

// touchesReach reports whether set writes a reachability setting.
func touchesReach(set map[string]any) bool {
	for _, k := range store.ReachKeys {
		if _, ok := set[k]; ok {
			return true
		}
	}
	return false
}

// errProxyIsYou refuses a trusted-proxies write in open mode that lists the
// caller's own address: the open gate refuses a trusted peer as forwarded, so
// the write would lock the caller out.
var errProxyIsYou = errors.New("trusted proxies include the caller")

// guardedKeys are the settings a write must prove the account for (checkCurrent).
var guardedKeys = []string{store.SettingTrustedProxies, store.SettingCloudflareAccess}

// guarded reports whether set writes one of guardedKeys.
func guarded(set map[string]any) bool {
	for _, k := range guardedKeys {
		if _, ok := set[k]; ok {
			return true
		}
	}
	return false
}

// checkReachChange refuses a reachability write that would lock the owner out
// (it runs under the reach lock, against the state in force):
//
//   - changing or turning off Cloudflare Access while the account has no web
//     password and signs in through it: that would lock the owner out, or hand
//     sign-in to whoever the new team admits. A password is set first
//     (Account), which needs a verified Access sign-in anyway.
//   - in open mode, a trusted-proxies list that contains the caller's own
//     address: the open gate would then refuse the caller as forwarded.
func (s *Server) checkReachChange(r *http.Request, cur *reach.State, set map[string]any) error {
	acct, exists, err := s.db.Account(r.Context())
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if v, ok := set[store.SettingCloudflareAccess]; ok {
		m, _ := v.(map[string]any)
		team, _ := m["team_domain"].(string)
		aud, _ := m["aud"].(string)
		if (store.AccessConfig{TeamDomain: team, AUD: aud}) != cur.Stored.Access && setup.DisplayMode(acct) == "access" {
			return errAccessInUse
		}
	}
	if v, ok := set[store.SettingTrustedProxies]; ok && acct.AuthMode == store.AuthOpen {
		peer, known := auth.Peer(r)
		l, _ := v.([]any)
		for _, e := range l {
			ps, err := auth.ParseProxies(fmt.Sprint(e))
			if known && err == nil && len(ps) == 1 && ps[0].Contains(peer) {
				return errProxyIsYou
			}
		}
	}
	return nil
}
