package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// tailnetLocal is this machine's Tailscale address in the setup harness.
var tailnetLocal = netip.MustParseAddr("100.100.100.1")

// arrivedOn records the local address the connection arrived on, as net/http
// does for a served request.
func arrivedOn(local string) func(*http.Request) {
	return func(r *http.Request) {
		ap := netip.MustParseAddrPort(local)
		*r = *r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, net.TCPAddrFromAddrPort(ap)))
	}
}

// openAccount finishes setup in open mode from this computer and returns the session.
func (h *setupHarness) openAccount(extra map[string]any, mod ...func(*http.Request)) *http.Cookie {
	h.t.Helper()
	body := map[string]any{"username": "reader", "passwordless": "open", "acknowledge_open": true}
	for k, v := range extra {
		body[k] = v
	}
	rec := h.req("POST", "/api/setup/account", accountBody(body), mod...)
	require.Equal(h.t, http.StatusCreated, rec.Code, rec.Body.String())
	c := cookieNamed(rec, cookieName)
	require.NotNil(h.t, c)
	return c
}

// #128: a remote client behind a same-machine proxy that passes its Host
// through (nginx with proxy_set_header Host $host and X-Forwarded-For /
// X-Forwarded-Proto, not in KIPPLE_TRUSTED_PROXY_IPS) chose Host anything.ts.net
// and was taken for Tailscale Serve: a session, the bootstrap and a lasting
// Reader API password. Now the forwarded headers give it away.
func TestOpenGateTSNetHostThroughAProxyIsRefused(t *testing.T) {
	h := newSetupHarness(t)
	sess := h.openAccount(nil)
	nginx := func(hostName string) []func(*http.Request) {
		return []func(*http.Request){
			host(hostName), hdr("Origin", "http://"+hostName),
			hdr("X-Forwarded-For", "203.0.113.9"), hdr("X-Forwarded-Proto", "http"),
		}
	}
	for _, hv := range []string{"anything.ts.net", "127.0.0.1:1919"} { // the report's request, and its control
		rec := h.req("POST", "/api/auth/open", "", nginx(hv)...)
		require.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", hv, rec.Body.String())
		require.Equal(t, "forwarded", decode(t, rec)["reason"], hv)
		require.Nil(t, cookieNamed(rec, cookieName), hv)

		rec = h.req("GET", "/api/bootstrap", "", append(nginx(hv), withCookies(sess))...)
		require.Equal(t, http.StatusForbidden, rec.Code, hv)
		rec = h.req("POST", "/api/account/api-password", `{"generate":true}`, append(nginx(hv), withCookies(sess))...)
		require.Equal(t, http.StatusForbidden, rec.Code, hv)
		require.Equal(t, "open_refused", decode(t, rec)["error"], hv)
	}
	// Spoofing a tailnet address in front of the proxy's own is still a list.
	rec := h.req("POST", "/api/auth/open", "", host("anything.ts.net"), hdr("Origin", "http://anything.ts.net"),
		hdr("X-Forwarded-For", "100.101.102.103, 203.0.113.9"))
	require.Equal(t, "forwarded", decode(t, rec)["reason"])

	// What tailscaled itself sends for a tailnet request still works.
	serve := []func(*http.Request){
		host("box.tail1234.ts.net"), hdr("Origin", "https://box.tail1234.ts.net"),
		hdr("X-Forwarded-For", "100.101.102.103"), hdr("X-Forwarded-Host", "box.tail1234.ts.net"), hdr("X-Forwarded-Proto", "https"),
		hdr("Tailscale-User-Login", "someone@example.com"),
	}
	rec = h.req("POST", "/api/auth/open", "", serve...)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NotNil(t, cookieNamed(rec, cookieName))
}

// lanNames are names any LAN device can answer (mDNS, LLMNR or NetBIOS, a
// router's DHCP names) and so rebind to this computer.
var lanNames = []string{"nas:1919", "evil.local:1919", "box.lan", "box.home.arpa", "svc.internal"}

// requireRefusedInOpenMode checks that every route of an open-mode instance
// refuses Host hv (421), with and without a session.
func requireRefusedInOpenMode(t *testing.T, h *setupHarness, sess *http.Cookie, hv string) {
	t.Helper()
	for _, path := range []string{"/api/instance", "/api/bootstrap", "/", "/healthz"} {
		rec := h.req("GET", path, "", host(hv), withCookies(sess))
		require.Equal(t, http.StatusMisdirectedRequest, rec.Code, "%s %s", hv, path)
	}
	rec := h.req("POST", "/api/auth/open", "", host(hv), hdr("Origin", "http://"+hv))
	require.Equal(t, http.StatusMisdirectedRequest, rec.Code, hv)
	require.Contains(t, rec.Body.String(), "Allowed host names", hv)
	require.Contains(t, rec.Body.String(), "KIPPLE_ALLOWED_HOSTS", hv)
}

// The default install publishes the port on 127.0.0.1 only, so no LAN device
// can reach it. A LAN device can still answer a .local or single-label name
// with 127.0.0.1 and drive the owner's browser into an open-mode instance
// (DNS rebinding), so open mode answers none of those unless listed. Set up
// from 127.0.0.1, nothing is listed, and every LAN-answered name is refused.
func TestOpenModeLoopbackSetupRefusesLANAnsweredNames(t *testing.T) {
	h := newSetupHarness(t)
	for _, hv := range lanNames {
		require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host(hv)).Code, "setup mode: %s", hv)
	}
	sess := h.openAccount(nil) // from 127.0.0.1:1919
	sec, err := h.db.SecuritySettings(context.Background())
	require.NoError(t, err)
	require.Empty(t, sec.AllowedHosts, "an IP literal needs no listing")
	for _, hv := range lanNames {
		requireRefusedInOpenMode(t, h, sess, hv)
	}
	for _, hv := range []string{"127.0.0.1:1919", "[::1]:1919", "localhost:1919", "app.localhost:1919", "box.tail1234.ts.net"} {
		require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host(hv)).Code, hv)
	}
}

// Choosing open mode in the wizard under a name open mode would not answer
// unlisted (nas.local, nas) lists that name with the account, so the owner
// keeps working there; every other LAN-answered name is still refused.
func TestOpenModeRemembersTheWizardName(t *testing.T) {
	for _, tc := range []struct{ hv, remembered string }{
		{"nas.local:1919", "nas.local"},
		{"NAS:1919", "nas"},
		{"box.home.arpa.", "box.home.arpa"},
	} {
		t.Run(tc.remembered, func(t *testing.T) {
			h := newSetupHarness(t)
			st := decode(t, h.req("GET", "/api/instance", "", host(tc.hv)))
			require.Equal(t, map[string]any{"reason": nil}, st["open"], "the wizard offers open mode here")
			sess := h.openAccount(nil, host(tc.hv), hdr("Origin", "http://"+tc.hv))
			sec, err := h.db.SecuritySettings(context.Background())
			require.NoError(t, err)
			require.Equal(t, []string{tc.remembered}, sec.AllowedHosts)

			for _, path := range []string{"/api/instance", "/api/bootstrap", "/", "/healthz"} {
				require.Equal(t, http.StatusOK, h.req("GET", path, "", host(tc.hv), withCookies(sess)).Code, path)
			}
			rec := h.req("POST", "/api/auth/open", "", host(tc.hv), hdr("Origin", "http://"+tc.hv))
			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			for _, hv := range append([]string{"evil.local:1919", "evil", "nas.lan"}, lanNames...) {
				if n, _, _ := strings.Cut(strings.ToLower(hv), ":"); strings.TrimSuffix(n, ".") == tc.remembered {
					continue
				}
				requireRefusedInOpenMode(t, h, sess, hv)
			}
		})
	}
}

// A public name is not remembered (setup mode refuses it), and neither is a
// name open mode already answers.
func TestOpenModeRemembersNothingOpenModeAnswers(t *testing.T) {
	for _, hv := range []string{"localhost:1919", "app.localhost", "192.168.1.20:1919", "box.tail1234.ts.net"} {
		h := newSetupHarness(t)
		h.openAccount(nil, host(hv), hdr("Origin", "http://"+hv))
		sec, err := h.db.SecuritySettings(context.Background())
		require.NoError(t, err)
		require.Empty(t, sec.AllowedHosts, hv)
	}
	h := newSetupHarness(t, func(o *Options) { o.AllowedHosts = []string{"nas.local"} })
	h.openAccount(nil, host("nas.local"), hdr("Origin", "http://nas.local"))
	sec, err := h.db.SecuritySettings(context.Background())
	require.NoError(t, err)
	require.Empty(t, sec.AllowedHosts, "already listed in KIPPLE_ALLOWED_HOSTS")
}

// Listed names pass in open mode; the owner may list LAN names explicitly.
func TestOpenModeHostGateListedNames(t *testing.T) {
	h := newSetupHarness(t)
	sess := h.openAccount(nil)
	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"security.allowed_hosts":["nas","*.local"]}`, withCookies(sess)).Code)
	require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host("nas:1919")).Code)
	require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host("evil.local:1919")).Code)
	require.Equal(t, http.StatusMisdirectedRequest, h.req("GET", "/api/instance", "", host("box.lan")).Code)
}

// A peer in Tailscale's range counts as the tailnet only when it arrived on
// this machine's own Tailscale address or a private one (the LAN, a container
// bridge), not on a public or unknown address.
func TestOpenGateTailnetPeerNeedsTheTailscaleInterface(t *testing.T) {
	h := newSetupHarness(t)
	h.openAccount(nil)
	ts := peer("100.101.102.103:5000")
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/auth/open", "", ts, arrivedOn("100.100.100.1:1919")).Code)
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/auth/open", "", ts, arrivedOn("192.168.1.10:1919")).Code)
	rec := h.req("POST", "/api/auth/open", "", ts, arrivedOn("203.0.113.5:1919"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "peer", decode(t, rec)["reason"])
	rec = h.req("POST", "/api/auth/open", "", ts)
	require.Equal(t, "peer", decode(t, rec)["reason"], "no local address: fail closed")
}

// #131: About names open mode "open" (not Cloudflare Access) and shows the zone
// in force now, not the one the process started with.
func TestAboutReportsOpenModeAndTheLiveZone(t *testing.T) {
	h := newSetupHarness(t)
	sess := h.openAccount(nil)
	out := decode(t, h.req("GET", "/api/about", "", withCookies(sess)))
	require.Equal(t, "open", out["auth_mode"])
	require.Equal(t, "UTC", out["tz"], "a new install")

	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"tz":"Europe/Paris"}`, withCookies(sess)).Code)
	out = decode(t, h.req("GET", "/api/about", "", withCookies(sess)))
	require.Equal(t, "Europe/Paris", out["tz"], "no restart needed")
}

func TestAboutReportsAccessAndPasswordModes(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	_, out, _ := h.api(c, "GET", "/api/about", "")
	require.Equal(t, "password", out["auth_mode"])
	require.NoError(t, h.db.SetPasswordHash(context.Background(), "", store.AuthStandard, sessionID(c.Value)))
	_, out, _ = h.api(c, "GET", "/api/about", "")
	require.Equal(t, "access", out["auth_mode"])
}

// streamEnds reads br until the stream closes, failing after timeout.
func streamEnds(t *testing.T, br *bufio.Reader, timeout time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, br)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "closed") {
			require.NoError(t, err)
		}
	case <-time.After(timeout):
		t.Fatalf("the stream was still open after %v", timeout)
	}
}

// An /api/events stream is closed as soon as the open gate stops passing it,
// without waiting for a heartbeat: here the owner removes the host name the
// stream was opened under from security.allowed_hosts.
func TestEventStreamClosesWhenTheOpenGateStopsPassing(t *testing.T) {
	h := newSetupHarness(t, func(o *Options) { o.Heartbeat = time.Hour })
	sess := h.openAccount(nil)
	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"security.allowed_hosts":["nas"]}`, withCookies(sess)).Code)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "192.168.1.20:5000" // a LAN device, let in as a local peer
		h.root.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	req.Host = "nas"
	req.AddCookie(&http.Cookie{Name: sess.Name, Value: sess.Value})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	br := bufio.NewReader(resp.Body)
	require.Equal(t, "retry: 3000\n", readUntil(t, br, "retry:", 2*time.Second))

	rec := h.req("PATCH", "/api/settings", `{"security.allowed_hosts":[]}`, withCookies(sess))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	streamEnds(t, br, 3*time.Second)
}
