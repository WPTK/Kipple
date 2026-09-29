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
	openLAN bool
	loaded  time.Time
	failed  bool // the read failed and nothing was known before (not cached): enforce
}

type modeCache struct {
	mu    sync.Mutex
	gen   uint64
	snap  *modeSnapshot
	stale bool // invalidated: re-read before use, but keep snap for a failed read
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
	snap := &modeSnapshot{openLAN: sec.OpenLAN, loaded: now, allowed: s.allowedWith(sec.AllowedHosts)}
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

// noteMode records a mode, account or security-setting change made here: the
// next request re-reads the snapshot, and the fallback kept for a read that
// fails already carries the change (apply edits a copy; nil changes nothing),
// so a failed re-read can neither forget open mode nor miss a switch to it.
func (s *Server) noteMode(apply func(*modeSnapshot)) {
	s.mode.mu.Lock()
	defer s.mode.mu.Unlock()
	s.mode.gen++
	s.mode.stale = true
	if s.mode.snap != nil && apply != nil {
		c := *s.mode.snap
		c.allowed = append([]string(nil), c.allowed...)
		apply(&c)
		s.mode.snap = &c
	}
}

// enforceHosts reports whether the Host gate refuses (rather than only logs)
// unlisted names: in setup mode (known in memory) and in open mode (design
// 5.2), and fail-closed when the mode is unknown: New reads it at start and a
// later failed read keeps the last one, so that takes a database that could
// not be read even once.
func (s *Server) enforceHosts(snap *modeSnapshot) bool {
	return s.opt.Setup.Pending() || snap.failed || snap.mode == store.AuthOpen
}

// hostAllowed normalizes r's Host and judges it.
func (s *Server) hostAllowed(r *http.Request, snap *modeSnapshot) (host string, ok bool) {
	host, valid := setup.NormalizeHost(r.Host)
	return host, valid && setup.HostAllowed(host, snap.allowed)
}

// hostRefusedText is the 421 body: what happened and the settings that fix it.
const hostRefusedText = "Kipple refused this request because of the address it was sent to.\n\n" +
	"While Kipple is being set up, and while it runs without a password (open mode), it only answers requests\n" +
	"addressed to an IP address, localhost, a single-word name, or a .local, .lan, .home.arpa, .internal or\n" +
	".ts.net name. This protects it against DNS rebinding from other web sites.\n\n" +
	"To use another name, add it to KIPPLE_ALLOWED_HOSTS (comma-separated, e.g. rss.example.com or *.example.com)\n" +
	"and restart, or add it under Settings (security.allowed_hosts). Opening Kipple by its IP address always works.\n"

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
// part (Host gate, not forwarded, a local peer; design 5.4) with the given
// security.open_lan, and with signIn also the browser part (an Origin naming
// the host the request was sent to) that granting open-mode access needs: a
// session, the switch to open mode, a Reader API password. In open mode every
// signed-in request passes the network part (authed), so a session never
// outlives the network position or the setting that admitted it.
func (s *Server) gateRefusal(r *http.Request, snap *modeSnapshot, openLAN, signIn bool) string {
	host, ok := s.hostAllowed(r, snap)
	if signIn {
		return s.opt.Gate.SignInRefusal(r, host, ok, openLAN)
	}
	return s.opt.Gate.OpenRefusal(r, host, ok, openLAN)
}

// signInRefusal is gateRefusal for granting access, with the stored open_lan.
func (s *Server) signInRefusal(r *http.Request) string {
	snap := s.snapshot(r.Context())
	return s.gateRefusal(r, snap, snap.openLAN, true)
}

// writeOpenRefused answers a request that failed the open gate.
func writeOpenRefused(w http.ResponseWriter, reason string) {
	msg := "open mode (no password) only works from this computer or over Tailscale"
	switch reason {
	case setup.RefuseHost:
		msg = "open mode refuses this address: open Kipple by its IP address, localhost or an allowed host name"
	case setup.RefuseForwarded:
		msg = "open mode refuses requests through a proxy or tunnel: reach Kipple directly (localhost or Tailscale), or set a password"
	case setup.RefusePeer:
		msg = "open mode only accepts this computer and Tailscale devices; allow the local network under Settings (security.open_lan), or set a password"
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "open_refused", "reason": reason, "message": msg})
}
