package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/setup"
)

// Reset (docs/design.md §2.6): Settings can return Kipple to setup mode. It is
// the swap of `kipple restore` with nothing to install: the next start moves
// the database into backup/pre-restore-<ts>/ and begins empty. The routes exist
// in normal mode only (setup mode has nothing to reset).

// resetPhrase is what the person types to confirm.
const resetPhrase = "reset kipple"

// resetInfo is GET /api/reset: whether KIPPLE_USERNAME and KIPPLE_PASSWORD are
// set for this process, in which case the next start would create that account
// again and skip setup mode.
func (s *Server) resetInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"env_account": s.opt.EnvAccount})
}

// resetKipple is POST /api/reset {"password", "phrase", "ignore_env_account"}:
// the current password is proved exactly as the account password change does
// (an account without one proves itself the same way it does there), the phrase
// must be typed, then the marker is written, the answer is 202 and Kipple stops
// so the next start applies it.
func (s *Server) resetKipple(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password         string `json:"password"`
		Phrase           string `json:"phrase"`
		IgnoreEnvAccount bool   `json:"ignore_env_account"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(body.Phrase), resetPhrase) {
		writeErrorMsg(w, http.StatusBadRequest, "bad_phrase", `Type "`+resetPhrase+`" to confirm.`)
		return
	}
	if backup.MarkerPending(s.opt.DataDir) {
		s.writeRestoreError(w, "reset", backup.ErrRestorePending)
		return
	}
	if _, ok := s.checkCurrent(w, r, body.Password, false); !ok {
		return
	}
	acct, exists, err := s.db.Account(r.Context())
	if err != nil || !exists {
		if err == nil {
			err = errors.New("no account row")
		}
		s.serverError(w, "reset: load account", err)
		return
	}
	// The environment request first: if the marker then fails, an account still
	// exists and the next start removes the file.
	if err := setup.SetIgnoreEnvAccount(s.opt.DataDir, s.opt.EnvAccount && body.IgnoreEnvAccount); err != nil {
		s.serverError(w, "reset: environment account", err)
		return
	}
	if err := backup.WriteResetMarker(s.opt.DataDir, s.opt.Version, acct.Username, s.now()); err != nil {
		s.writeRestoreError(w, "reset", err)
		return
	}
	s.log.Info("reset confirmed; restarting to apply it", "username", acct.Username,
		"ignore_env_account", s.opt.EnvAccount && body.IgnoreEnvAccount, "client", s.clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "estimate_seconds": resetEstimateSeconds})
	_ = http.NewResponseController(w).Flush()
	if s.opt.Restart != nil {
		s.opt.Restart()
	}
}

// resetEstimateSeconds is how long the restart takes: the shutdown and a move
// of files within one volume. The same floor as a restore's estimate.
const resetEstimateSeconds = 30
