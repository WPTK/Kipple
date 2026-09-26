package api

import (
	"fmt"
	"net/http"
	"time"
)

const (
	unreadWarnAt   = 10000
	snapshotStale  = 48 * time.Hour
	clockWarnAfter = time.Hour
)

type warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// bootstrap is GET /api/bootstrap: everything the app needs to paint its first
// screen (design §7.1).
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fail := func(err error) {
		s.serverError(w, "bootstrap", err)
	}
	acct, _, err := s.db.Account(ctx)
	if err != nil {
		fail(err)
		return
	}
	settings, err := s.db.MergedSettings(ctx)
	if err != nil {
		fail(err)
		return
	}
	dv, err := s.currentDevice(w, r) // issues the kipple_device cookie on the first request
	if err != nil {
		fail(err)
		return
	}
	dview, err := s.deviceView(r, dv)
	if err != nil {
		fail(err)
		return
	}
	folders, err := s.db.UIFolders(ctx)
	if err != nil {
		fail(err)
		return
	}
	feeds, err := s.db.UIFeeds(ctx, s.statusEnv())
	if err != nil {
		fail(err)
		return
	}
	unread, starred, err := s.db.Counts(ctx)
	if err != nil {
		fail(err)
		return
	}
	muted, err := s.db.MutedCount(ctx)
	if err != nil {
		fail(err)
		return
	}
	savedSearches, err := s.db.SavedSearches(ctx)
	if err != nil {
		fail(err)
		return
	}
	highlights, err := s.db.Highlights(ctx)
	if err != nil {
		fail(err)
		return
	}
	now := s.now()
	warnings := []warning{}
	if skew := s.db.IDs().Skew(); skew > clockWarnAfter {
		warnings = append(warnings, warning{"clock", fmt.Sprintf(
			"Item ids are ahead of the clock by %s. Reader sync windows and mark-all cutoffs are unreliable until then.", skew.Round(time.Minute))})
	}
	if snap := s.db.SnapshotStatus(ctx); snap.LastError != "" {
		warnings = append(warnings, warning{"snapshot", "The nightly database snapshot is failing: " + snap.LastError})
	} else if snap.LastAt > 0 && now.Sub(time.Unix(snap.LastAt, 0)) > snapshotStale {
		warnings = append(warnings, warning{"snapshot", "The last database snapshot is more than 48 hours old."})
	}
	if unread > unreadWarnAt {
		warnings = append(warnings, warning{"unread_cap", "Unread total is above 10,000: Reeder only syncs the newest 10,000 unread ids."})
	}
	schedRuns, _ := s.opt.Sched.Status()
	runs := make([]any, 0, len(schedRuns)+1)
	for _, run := range schedRuns {
		runs = append(runs, run)
	}
	if ar := s.applyStatus(); ar != nil {
		runs = append(runs, ar)
	}
	if ar := s.autoReadStatus(); ar != nil {
		runs = append(runs, ar)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":           map[string]any{"username": acct.Username, "api_enabled": acct.APIPasswordHash != ""},
		"settings":       settings,
		"device":         map[string]any{"id": dv.ID, "name": dv.Name, "profile": dview["profile"], "merged": dview["merged"]},
		"folders":        folders,
		"feeds":          feeds,
		"counts":         map[string]int64{"unread": unread, "starred": starred, "muted": muted},
		"highlights":     highlights,
		"runs":           runs,
		"warnings":       warnings,
		"saved_searches": savedSearches,
		"server_time":    now.Unix(),
		"version":        s.opt.Version,
	})
}
