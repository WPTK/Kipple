package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

// modeTTL bounds how stale the cached auth mode and allowed-host list may be.
// Changes made through this API invalidate the cache at once; the TTL only
// covers `kipple password` from another process, which can only move open mode
// back to standard (so a stale value enforces the Host gate a little longer).
const modeTTL = 5 * time.Second

// modeSnapshot is what the Host gate needs on every request.
type modeSnapshot struct {
	mode    string   // store.AuthStandard or store.AuthOpen ("" without an account)
	allowed []string // security.allowed_hosts plus Options.AllowedHosts
	loaded  time.Time
	failed  bool // the read failed and nothing was known before (not cached): enforce
}

type modeCache struct {
	mu    sync.Mutex
	gen   uint64
	snap  *modeSnapshot
	stale bool // invalidated: re-read before use, but keep snap for a failed read
	// changed is closed (and forgotten) by noteMode once the new snapshot is in
	// place, so a long-lived request (an /api/events stream) re-checks the open
	// gate at once rather than at its next heartbeat.
	changed chan struct{}
}

// modeChanged is closed at the next mode, account or security-setting change
// made through this API.
func (s *Server) modeChanged() <-chan struct{} {
	s.mode.mu.Lock()
	defer s.mode.mu.Unlock()
	if s.mode.changed == nil {
		s.mode.changed = make(chan struct{})
	}
	return s.mode.changed
}

// snapshot returns the cached mode, re-reading it when older than modeTTL. A
// failed read keeps the previous snapshot; with none it reports failed, which
// the gate treats as "enforce".
func (s *Server) snapshot(ctx context.Context) *modeSnapshot {
	now := s.now()
	s.mode.mu.Lock()
	cur, gen, stale := s.mode.snap, s.mode.gen, s.mode.stale
	s.mode.mu.Unlock()
	if cur != nil && !stale && now.Sub(cur.loaded) < modeTTL && !now.Before(cur.loaded) {
		return cur
	}
	acct, ok, err := s.db.Account(ctx)
	var sec store.Security
	if err == nil {
		sec, err = s.db.SecuritySettings(ctx)
	}
	if err != nil {
		s.log.Warn("api: reading the auth mode for the Host gate", "err", err)
		if cur != nil {
			return cur // the last known mode (New primes one at start)
		}
		return &modeSnapshot{failed: true}
	}
	snap := &modeSnapshot{loaded: now, allowed: s.allowedWith(sec.AllowedHosts)}
	if ok {
		snap.mode = acct.AuthMode
	}
	s.mode.mu.Lock()
	if s.mode.gen == gen {
		s.mode.snap, s.mode.stale = snap, false
	}
	s.mode.mu.Unlock()
	return snap
}

// allowedWith is the configured names plus the valid entries of a stored
// security.allowed_hosts list (the PATCH validator already normalized them; a
// hand-edited row is filtered).
func (s *Server) allowedWith(stored []string) []string {
	out := append([]string(nil), s.opt.AllowedHosts...)
	for _, e := range stored {
		if n, err := setup.CheckHostEntry(e); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// noteMode records a mode, account or security-setting change made here and
// re-reads the snapshot at once from what was just committed. apply (nil: no
// edit) puts the change into a copy of the old snapshot first, so if that
// re-read fails the fallback still carries it and cannot forget open mode or
// miss a switch to it.
func (s *Server) noteMode(ctx context.Context, apply func(*modeSnapshot)) {
	s.mode.mu.Lock()
	s.mode.gen++
	s.mode.stale = true
	if s.mode.snap != nil && apply != nil {
		c := *s.mode.snap
		c.allowed = append([]string(nil), c.allowed...)
		apply(&c)
		s.mode.snap = &c
	}
	s.mode.mu.Unlock()
	s.snapshot(context.WithoutCancel(ctx))
	s.mode.mu.Lock()
	if s.mode.changed != nil {
		close(s.mode.changed)
		s.mode.changed = nil
	}
	s.mode.mu.Unlock()
}

// enforceHosts reports whether the Host gate refuses (rather than only logs)
// unlisted names: in setup mode (known in memory) and in open mode (design
// 5.2), and fail-closed when the mode is unknown: New reads it at start and a
// later failed read keeps the last one, so that takes a database that could
// not be read even once.
func (s *Server) enforceHosts(snap *modeSnapshot) bool {
	return s.opt.Setup.Pending() || snap.failed || snap.mode == store.AuthOpen
}

// hostAllowed normalizes r's Host and judges it: by open mode's narrower
// list (setup.OpenHostAllowed) when open, and when the mode could not be read
// (fail closed), else by the setup-mode list (setup.HostAllowed).
func (s *Server) hostAllowed(r *http.Request, snap *modeSnapshot) (host string, ok bool) {
	if s.openHosts(snap) {
		return s.openHostAllowed(r, snap)
	}
	host, valid := setup.NormalizeHost(r.Host)
	return host, valid && setup.HostAllowed(host, snap.allowed)
}

// openHostAllowed judges r's Host by open mode's list, whatever the mode:
// the open gate always uses it, including for the switch to open mode.
func (s *Server) openHostAllowed(r *http.Request, snap *modeSnapshot) (host string, ok bool) {
	host, valid := setup.NormalizeHost(r.Host)
	return host, valid && setup.OpenHostAllowed(host, snap.allowed)
}

// openHosts reports whether the Host gate uses open mode's list: an open-mode
// account outside setup mode, or a mode that could not be read.
func (s *Server) openHosts(snap *modeSnapshot) bool {
	return snap.failed || (snap.mode == store.AuthOpen && !s.opt.Setup.Pending())
}

// hostRefusedText is the 421 body: what happened and the settings that fix it.
const hostRefusedText = "Kipple refused this request because of the address it was sent to.\n\n" +
	"While Kipple is being set up, it only answers requests addressed to an IP address, localhost, a single-word\n" +
	"name, or a .localhost, .local, .lan, .home.arpa, .internal or .ts.net name. While it runs without a password\n" +
	"(open mode), it answers an IP address, localhost, a .localhost or .ts.net name, the name it was set up under,\n" +
	"and the names you listed: other devices on the local network can answer single-word, .local and similar\n" +
	"names, so each one you use has to be listed. This protects it against DNS rebinding.\n\n" +
	"To use another name, add it under Settings > Allowed host names (security.allowed_hosts), or to\n" +
	"KIPPLE_ALLOWED_HOSTS (comma-separated, e.g. nas.local, rss.example.com or *.example.com) and restart.\n" +
	"Opening Kipple by its IP address always works.\n"

// HostGate is the Host-header check (design 5.2), installed ahead of every
// handler. In setup and open mode a request whose Host is not an allowed name
// is refused with 421 Misdirected Request; otherwise it is only logged, at most
// once an hour: there the session cookie is bound to the real origin, so a
// rebinding page reads nothing, and refusing would break existing deployments
// whose name was never configured.
func (s *Server) HostGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.snapshot(r.Context())
		if _, ok := s.hostAllowed(r, snap); !ok {
			if s.enforceHosts(snap) {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Cache-Control", "private, no-store")
				w.WriteHeader(http.StatusMisdirectedRequest)
				_, _ = w.Write([]byte(hostRefusedText))
				return
			}
			s.warnHost()
		}
		next.ServeHTTP(w, r)
	})
}

// hostWarnEvery is the minimum gap between "unlisted host" warnings.
const hostWarnEvery = time.Hour

func (s *Server) warnHost() {
	s.hostWarnMu.Lock()
	t := s.now()
	warn := s.hostWarnAt.IsZero() || t.Sub(s.hostWarnAt) >= hostWarnEvery
	if warn {
		s.hostWarnAt = t
	}
	s.hostWarnMu.Unlock()
	if warn {
		s.log.Warn("a request named a host that is not in the allowed list; it is served because Kipple has a password, but setup and open mode would refuse it: add the name to KIPPLE_ALLOWED_HOSTS or security.allowed_hosts")
	}
}

// gateRefusal is the open gate for r against snap ("" = passes): the network
// part (Host gate, not forwarded, a local peer; design 5.4), and with signIn
// also the browser part (an Origin naming the host the request was sent to)
// that granting open-mode access needs: a session, the switch to open mode, a
// Reader API password. In open mode every signed-in request passes the network
// part (authed), so a session never outlives the network position that
// admitted it.
func (s *Server) gateRefusal(r *http.Request, snap *modeSnapshot, signIn bool) string {
	host, ok := s.openHostAllowed(r, snap)
	return s.openGate(r, host, ok, signIn)
}

// wizardOpenRefusal is gateRefusal for choosing open mode in the setup wizard
// (signIn: the account POST; otherwise the instance's preview of it), judging
// the Host by setup mode's list: the name the wizard was opened under (nas,
// nas.local) may become open mode's name, and remember is that name when open
// mode would not answer it unlisted (setup.OpenHostToRemember; "" otherwise),
// which setupAccount lists with the new account. This gives a rebinding page
// nothing setup mode does not already: whoever creates the account owns the
// instance, with or without a password.
func (s *Server) wizardOpenRefusal(r *http.Request, snap *modeSnapshot, signIn bool) (reason, remember string) {
	host, ok := s.hostAllowed(r, snap) // setup mode's list while setup is pending
	if reason = s.openGate(r, host, ok, signIn); reason == "" {
		remember = setup.OpenHostToRemember(host, snap.allowed)
	}
	return reason, remember
}

// openGate is the open gate for an already judged Host.
func (s *Server) openGate(r *http.Request, host string, hostOK, signIn bool) string {
	if signIn {
		return s.opt.Gate.SignInRefusal(r, host, hostOK)
	}
	return s.opt.Gate.OpenRefusal(r, host, hostOK)
}

// signInRefusal is gateRefusal for granting access.
func (s *Server) signInRefusal(r *http.Request) string {
	return s.gateRefusal(r, s.snapshot(r.Context()), true)
}

// writeOpenRefused answers a request that failed the open gate.
func writeOpenRefused(w http.ResponseWriter, reason string) {
	msg := "open mode (no password) only works from this computer, your local network or Tailscale"
	switch reason {
	case setup.RefuseHost:
		msg = "open mode does not answer this address: open Kipple by its IP address, localhost or the name you set it up under, or add this name under Settings > Allowed host names (security.allowed_hosts) or to KIPPLE_ALLOWED_HOSTS"
	case setup.RefuseForwarded:
		msg = "open mode refuses requests through a proxy or tunnel: reach Kipple directly (localhost, your local network or Tailscale), or set a password"
	case setup.RefusePeer:
		msg = "open mode only accepts this computer, devices on your local network and Tailscale devices; set a password to use Kipple from anywhere else"
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "open_refused", "reason": reason, "message": msg})
}
