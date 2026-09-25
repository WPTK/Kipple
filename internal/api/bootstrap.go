package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/WPTK/kipple/internal/sched"
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
	runs, _ := s.opt.Sched.Status()
	if runs == nil {
		runs = []sched.RunStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":        map[string]any{"username": acct.Username, "api_enabled": acct.APIPasswordHash != ""},
		"settings":    settings,
		"folders":     folders,
		"feeds":       feeds,
		"counts":      map[string]int64{"unread": unread, "starred": starred},
		"runs":        runs,
		"warnings":    warnings,
		"server_time": now.Unix(),
		"version":     s.opt.Version,
	})
}
