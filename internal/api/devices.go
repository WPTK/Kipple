package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WPTK/kipple/internal/store"
)

// Per-device appearance profiles (backend-additions-round2 section 4, design 7.1).
//
// A device is a browser or an installed app, told apart by the HttpOnly kipple_device
// cookie. Its profile holds overrides only. The effective value of a key is
//
//	device override > account default (the ui.* settings row) > built-in default
//
// The profile carries two kinds of key: the device-scoped server settings (ui.theme and
// friends, validated by the same settingDef.check as PATCH /api/settings) and the
// client-only keys below, all named client.*, which only the web app reads. Their account
// defaults live in the hidden setting ui.device_defaults.

const (
	deviceCookieName = "kipple_device"
	deviceCookieTTL  = time.Duration(store.DeviceMaxAgeDays) * 24 * time.Hour
	maxDeviceName    = 64
	clientPrefix     = "client."
)

// clientDef is one client-only profile key: its validator and its built-in default
// (nil when the key has none and the client decides, for example from the device).
type clientDef struct {
	check func(any) (any, string)
	def   any
}

var idKeyRe = regexp.MustCompile(`^[0-9]{1,19}$`)

// floatOneOf accepts one of a fixed set of numbers.
func floatOneOf(vals ...float64) func(any) (any, string) {
	return func(v any) (any, string) {
		if f, ok := v.(float64); ok {
			for _, w := range vals {
				if f == w {
					return f, ""
				}
			}
		}
		return nil, "must be one of the listed numbers"
	}
}

var layoutIDs = []string{"magazine", "cards", "compact", "inbox", "headlines"}

func layoutMap(v any, max int) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) > max {
		return nil, false
	}
	out := make(map[string]any, len(m))
	for k, x := range m {
		s, isStr := x.(string)
		if !idKeyRe.MatchString(k) || !isStr || !contains(layoutIDs, s) {
			return nil, false
		}
		out[k] = s
	}
	return out, true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func boundedInt(lo, hi int) func(any) (any, string) { return intIn(lo, hi) }

// clientDefs are the appearance and behavior keys the web client used to keep in
// localStorage (kipple.device.v1 and kipple.prefs.v1), by name. Layout ids stay
// magazine/headlines internally (their labels are Editorial and Email - Compact).
var clientDefs = map[string]clientDef{
	"client.layout": {oneOf(layoutIDs...), "magazine"},
	"client.layout_overrides": {func(v any) (any, string) {
		const msg = `must be {"feed":{id:layout},"folder":{id:layout}} with numeric-string ids and known layouts (at most 200 each)`
		m, ok := v.(map[string]any)
		if !ok || len(m) > 2 {
			return nil, msg
		}
		out := map[string]any{"feed": map[string]any{}, "folder": map[string]any{}}
		for k, x := range m {
			if k != "feed" && k != "folder" {
				return nil, msg
			}
			lm, ok := layoutMap(x, 200)
			if !ok {
				return nil, msg
			}
			out[k] = lm
		}
		return out, ""
	}, map[string]any{"feed": map[string]any{}, "folder": map[string]any{}}},
	"client.order":             {oneOf("newest", "oldest"), "newest"},
	"client.inbox_thumbs":      {oneOf("auto", "off"), "auto"},
	"client.peek_seen":         {boolVal, false},
	"client.article_width":     {oneOf("narrow", "medium", "wide", "full"), "medium"},
	"client.list_width":        {boundedInt(260, 720), nil},
	"client.sidebar_width":     {boundedInt(200, 420), 240},
	"client.link_target":       {oneOf("new", "same"), nil},
	"client.unread_badge":      {oneOf("count", "dot", "off"), "count"},
	"client.text_size":         {floatOneOf(0.875, 1, 1.125, 1.25, 1.5), 1.0},
	"client.adjust_separately": {boolVal, false},
	"client.shortcuts":         {boolVal, nil},
	"client.spacing":           {oneOf("snug", "normal", "roomy"), "normal"},
	"client.motion":            {oneOf("system", "on", "off"), "system"},
	"client.large_targets":     {boolVal, false},
	"client.listen":            {boolVal, false},
	"client.voice": {func(v any) (any, string) {
		s, ok := v.(string)
		if !ok || len(s) > 200 || hasControl(s) {
			return nil, "must be a string of at most 200 characters without line breaks"
		}
		return s, ""
	}, ""},
	"client.rate": {floatOneOf(0.8, 1, 1.2, 1.5), 1.0},
	"client.collapsed_folders": {func(v any) (any, string) {
		const msg = "must be a list (at most 200) of numeric-string folder ids without repeats"
		arr, ok := v.([]any)
		if !ok || len(arr) > 200 {
			return nil, msg
		}
		seen := map[string]bool{}
		out := make([]any, 0, len(arr))
		for _, x := range arr {
			s, ok := x.(string)
			if !ok || !idKeyRe.MatchString(s) || seen[s] {
				return nil, msg
			}
			seen[s] = true
			out = append(out, s)
		}
		return out, ""
	}, []any{}},
}

// checkDeviceDefaults validates ui.device_defaults: an object of client.* keys.
func checkDeviceDefaults(v any) (any, string) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, "must be an object of client.* keys"
	}
	out := make(map[string]any, len(m))
	for k, x := range m {
		def, known := clientDefs[k]
		if !known {
			return nil, "unknown key " + k
		}
		if x == nil {
			continue
		}
		c, msg := def.check(x)
		if msg != "" {
			return nil, k + ": " + msg
		}
		out[k] = c
	}
	if b, _ := json.Marshal(out); len(b) > store.MaxDeviceProfileBytes {
		return nil, "too large"
	}
	return out, ""
}

// validateProfileKey checks one key of a device profile. A device may set the
// device-scoped server settings and the client.* keys, nothing else.
func validateProfileKey(k string, v any) (any, string) {
	if msg := profileKeyProblem(k); msg != "" {
		return nil, msg
	}
	if def, ok := settingDefByKey[k]; ok {
		return def.check(v)
	}
	return clientDefs[k].check(v)
}

// profileKeyProblem is why k cannot be in a device profile, or "".
func profileKeyProblem(k string) string {
	if def, ok := settingDefByKey[k]; ok {
		if def.Scope == scopeGlobal {
			return "not a device setting"
		}
		return ""
	}
	if _, ok := clientDefs[k]; ok {
		return ""
	}
	return "unknown setting"
}

// deviceDefaults are the values a device without an override gets: the account
// default of each device-scoped setting and of each client key, else the built-in one.
func (s *Server) deviceDefaults(merged map[string]any) map[string]any {
	out := map[string]any{}
	for k := range deviceScoped {
		out[k] = merged[k]
	}
	for k, d := range clientDefs {
		if d.def != nil {
			out[k] = d.def
		}
	}
	if m, ok := merged["ui.device_defaults"].(map[string]any); ok {
		for k, v := range m {
			if def, known := clientDefs[k]; known {
				if c, msg := def.check(v); msg == "" {
					out[k] = c
				}
			}
		}
	}
	return out
}

// deviceView is the JSON of GET /api/device.
func (s *Server) deviceView(r *http.Request, dv store.Device) (map[string]any, error) {
	merged, err := s.db.MergedSettings(r.Context())
	if err != nil {
		return nil, err
	}
	defaults := s.deviceDefaults(merged)
	eff := make(map[string]any, len(defaults)+len(dv.Profile))
	for k, v := range defaults {
		eff[k] = v
	}
	profile := map[string]any{}
	for k, v := range dv.Profile {
		if c, msg := validateProfileKey(k, v); msg == "" {
			profile[k] = c // keys a newer build stored and this one no longer knows are ignored
			eff[k] = c
		}
	}
	return map[string]any{
		"id": dv.ID, "name": dv.Name, "created_at": dv.CreatedAt, "last_seen_at": dv.LastSeenAt,
		"profile": profile, "defaults": defaults, "merged": eff,
	}, nil
}

func newDeviceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) setDeviceCookie(w http.ResponseWriter, r *http.Request, id string) {
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookieName, Value: id, Path: "/", MaxAge: int(deviceCookieTTL / time.Second),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.scheme(r) == "https",
	})
}

// currentDevice resolves the caller's device from its cookie and returns its row: a
// missing, malformed or unknown cookie registers a new device and issues a cookie; a
// known one has last_seen_at bumped at most daily (and the cookie's expiry slides then).
// Only routes that need the profile call it, so the cookie is set on the first bootstrap.
// The Reader API never reads the cookie.
func (s *Server) currentDevice(w http.ResponseWriter, r *http.Request) (store.Device, error) {
	ctx := r.Context()
	now := s.now().Unix()
	ua, cl := r.Header.Get("User-Agent"), client(r)
	if c, err := r.Cookie(deviceCookieName); err == nil && store.ValidDeviceID(c.Value) {
		dv, found, touched, err := s.db.TouchDevice(ctx, c.Value, ua, cl, now)
		if err != nil {
			return store.Device{}, err
		}
		if found {
			if touched {
				s.setDeviceCookie(w, r, dv.ID) // seen again: slide the cookie's expiry
			}
			return dv, nil
		}
	}
	// No cookie, a malformed one or an id nobody registered (evicted, deleted, or made
	// up): a new device under a server-made id. A client never chooses its own id.
	id, err := newDeviceID()
	if err != nil {
		return store.Device{}, err
	}
	dv, err := s.db.RegisterDevice(ctx, id, ua, cl, now)
	if err != nil {
		return store.Device{}, err
	}
	s.setDeviceCookie(w, r, id)
	return dv, nil
}

func (s *Server) writeDevice(w http.ResponseWriter, r *http.Request, dv store.Device) {
	v, err := s.deviceView(r, dv)
	if err != nil {
		s.serverError(w, "device", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// getDevice is GET /api/device.
func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	dv, err := s.currentDevice(w, r)
	if err != nil {
		s.serverError(w, "device", err)
		return
	}
	s.writeDevice(w, r, dv)
}

// patchDevice is PATCH /api/device: a partial profile update, all or nothing. A null
// value clears the override.
func (s *Server) patchDevice(w http.ResponseWriter, r *http.Request) {
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
		if body[k] == nil {
			if msg := profileKeyProblem(k); msg != "" {
				issues = append(issues, settingIssue{k, msg})
			} else {
				set[k] = nil
			}
			continue
		}
		v, msg := validateProfileKey(k, body[k])
		if msg != "" {
			issues = append(issues, settingIssue{k, msg})
		} else {
			set[k] = v
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
	dv, err := s.currentDevice(w, r)
	if err != nil {
		s.serverError(w, "device", err)
		return
	}
	if dv.Profile, err = s.db.PatchDeviceProfile(r.Context(), dv.ID, set); err != nil {
		s.deviceWriteError(w, err)
		return
	}
	s.writeDevice(w, r, dv)
}

func (s *Server) deviceWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrDeviceProfileTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": "too_large", "message": "The device profile would be larger than 8 KB."})
	case errors.Is(err, store.ErrDeviceNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	default:
		s.serverError(w, "device", err)
	}
}

// putDeviceName is PUT /api/device/name {name}. The name is trimmed; empty clears it.
func (s *Server) putDeviceName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name *string `json:"name"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	if body.Name == nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	name := strings.TrimSpace(*body.Name)
	if utf8.RuneCountInString(name) > maxDeviceName || hasControl(name) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "bad_request", "message": "The name must be at most 64 characters without line breaks."})
		return
	}
	dv, err := s.currentDevice(w, r)
	if err != nil {
		s.serverError(w, "device", err)
		return
	}
	if err := s.db.SetDeviceName(r.Context(), dv.ID, name); err != nil {
		s.deviceWriteError(w, err)
		return
	}
	dv.Name = name
	s.writeDevice(w, r, dv)
}

// listDevices is GET /api/devices.
func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	cur, err := s.currentDevice(w, r)
	if err != nil {
		s.serverError(w, "devices", err)
		return
	}
	all, err := s.db.ListDevices(r.Context())
	if err != nil {
		s.serverError(w, "devices", err)
		return
	}
	out := make([]map[string]any, 0, len(all))
	for _, dv := range all {
		out = append(out, map[string]any{
			"id": dv.ID, "name": dv.Name, "current": dv.ID == cur.ID, "user_agent": dv.UserAgent, "client": dv.Client,
			"created_at": dv.CreatedAt, "last_seen_at": dv.LastSeenAt, "overrides": len(dv.Profile),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// copyDeviceFrom is POST /api/device/copy-from/{id}: this device's overrides are
// replaced by another device's, or cleared for the id "defaults".
func (s *Server) copyDeviceFrom(w http.ResponseWriter, r *http.Request) {
	dv, err := s.currentDevice(w, r)
	if err != nil {
		s.serverError(w, "device", err)
		return
	}
	src := r.PathValue("id")
	profile := map[string]any{}
	if src != "defaults" {
		if src == dv.ID {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request", "message": "That is this device."})
			return
		}
		other, ok, err := s.db.GetDevice(r.Context(), src)
		if err != nil {
			s.serverError(w, "device", err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		for k, v := range other.Profile {
			if c, msg := validateProfileKey(k, v); msg == "" {
				profile[k] = c
			}
		}
	}
	if err := s.db.ReplaceDeviceProfile(r.Context(), dv.ID, profile); err != nil {
		s.deviceWriteError(w, err)
		return
	}
	dv.Profile = profile
	s.writeDevice(w, r, dv)
}

// makeDeviceDefault is POST /api/device/make-default: this device's overrides become
// the account defaults new devices start from (device-scoped settings go to their own
// rows, client.* keys into ui.device_defaults). The device keeps its overrides.
func (s *Server) makeDeviceDefault(w http.ResponseWriter, r *http.Request) {
	dv, err := s.currentDevice(w, r)
	if err != nil {
		s.serverError(w, "device", err)
		return
	}
	merged, err := s.db.MergedSettings(r.Context())
	if err != nil {
		s.serverError(w, "device", err)
		return
	}
	set := map[string]any{}
	clients := map[string]any{}
	if m, ok := merged["ui.device_defaults"].(map[string]any); ok {
		for k, v := range m {
			clients[k] = v
		}
	}
	for k, v := range dv.Profile {
		c, msg := validateProfileKey(k, v)
		if msg != "" {
			continue
		}
		if strings.HasPrefix(k, clientPrefix) {
			clients[k] = c
		} else {
			set[k] = c
		}
	}
	set["ui.device_defaults"] = clients
	if err := s.db.SetSettings(r.Context(), set); err != nil {
		s.serverError(w, "device", err)
		return
	}
	s.writeDevice(w, r, dv)
}

// deleteDevice is DELETE /api/devices/{id}: any device but the current one.
func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	cur, err := s.currentDevice(w, r)
	if err != nil {
		s.serverError(w, "device", err)
		return
	}
	id := r.PathValue("id")
	if id == cur.ID {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "current_device", "message": "This is the device you are using."})
		return
	}
	ok, err := s.db.DeleteDevice(r.Context(), id)
	if err != nil {
		s.serverError(w, "device", err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
