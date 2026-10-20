package api

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WPTK/kipple/internal/reach"
)

// FuzzSignInChecks: the browser-facing checks of POST /api/auth/login against an oracle written from the
// rules, not the code. A request signs in (204 and a session cookie) exactly when the Origin or fetch-metadata
// rule passes, X-Kipple-Client is web or pwa, the user name matches (trimmed, case-insensitive) and the
// password is right. Nothing else sets a session cookie, and no cookie value the sender makes up opens a
// session before anyone has signed in.
func FuzzSignInChecks(f *testing.F) {
	type seed struct {
		origin, host, sfs, cookie, user, pass, xfp, client string
		viaProxy                                           bool
	}
	for _, s := range []seed{
		{"http://kipple.example", "kipple.example", "", "", testUser, testPass, "", "web", false},
		{"https://kipple.example", "kipple.example", "", "", testUser, testPass, "https", "web", true},
		{"https://kipple.example", "kipple.example", "", "", testUser, testPass, "https", "web", false},
		{"", "kipple.example", "same-origin", "", " OWNER ", testPass, "", "pwa", false},
		{"http://evil.example", "kipple.example", "same-site", "", testUser, testPass, "", "web", false},
		{"null", "kipple.example", "", "x", testUser, testPass, "", "web", false},
		{"http://kipple.example", "kipple.example", "", "kipple_session=a", testUser, testPass, "", "other", false},
		{"http://kipple.example:1919", "kipple.example:1919", "", "", testUser, "", "", "web", false},
		{"http://kipple.example", "kipple.example", "", "", testUser, testPass + "x", "", "web", false},
		{"https://kipple.example", "kipple.example", "", "", testUser, testPass, "HTTPS, http", "web", true},
		{"http://kipple.example", "KIPPLE.example", "", "", testUser, testPass, "", "web", false},
	} {
		f.Add(s.origin, s.host, s.sfs, s.cookie, s.user, s.pass, s.xfp, s.client, s.viaProxy)
	}
	h := newHarness(f, func(o *Options) {
		o.Reach = reach.Fixed(reach.State{Trusted: []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32")}})
	})
	f.Fuzz(func(t *testing.T, origin, host, sfs, cookie, user, pass, xfp, client string, viaProxy bool) {
		if len(origin)+len(host)+len(sfs)+len(cookie)+len(user)+len(pass)+len(xfp)+len(client) > 1500 ||
			!utf8.ValidString(user) || !utf8.ValidString(pass) {
			return
		}
		for _, v := range []string{origin, host, sfs, cookie, xfp, client} {
			if strings.ContainsAny(v, "\r\n\x00") { // not representable as one header value
				return
			}
		}
		mod := func(r *http.Request) {
			r.Host = host
			r.Header.Del("Sec-Fetch-Site")
			r.Header.Del("X-Kipple-Client")
			if sfs != "" {
				r.Header.Set("Sec-Fetch-Site", sfs)
			}
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
			if client != "" {
				r.Header.Set("X-Kipple-Client", client)
			}
			if xfp != "" {
				r.Header.Set("X-Forwarded-Proto", xfp)
			}
			if cookie != "" {
				r.Header.Set("Cookie", cookie)
			}
			if viaProxy {
				r.RemoteAddr = "192.0.2.20:4000"
			}
		}

		// A made-up cookie opens nothing.
		if rec := h.do("GET", "/api/status", "", mod); rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET /api/status with Cookie %q before any sign-in = %d", cookie, rec.Code)
		}

		body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
		rec := h.do("POST", "/api/auth/login", string(body), mod)

		scheme := "http"
		if viaProxy && strings.EqualFold(strings.TrimSpace(strings.Split(xfp, ",")[0]), "https") {
			scheme = "https"
		}
		var originOK bool
		if sfs != "" {
			originOK = sfs == "same-origin"
		} else {
			originOK = origin != "" && origin == scheme+"://"+host
		}
		want := originOK && (client == "web" || client == "pwa") &&
			strings.EqualFold(strings.TrimSpace(user), testUser) && pass == testPass

		var session *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if (c.Name == cookieName || c.Name == hostCookieName) && c.MaxAge >= 0 && c.Value != "" {
				session = c
			}
		}
		switch {
		case want && (rec.Code != http.StatusNoContent || session == nil):
			t.Fatalf("a valid sign-in was refused: %d %s (origin %q host %q sfs %q client %q)", rec.Code, rec.Body.String(), origin, host, sfs, client)
		case !want && (rec.Code == http.StatusNoContent || session != nil):
			t.Fatalf("a sign-in that breaks a rule succeeded: %d (origin %q host %q sfs %q client %q user %q)", rec.Code, origin, host, sfs, client, user)
		case rec.Code >= 500:
			t.Fatalf("sign-in answered %d: %s", rec.Code, rec.Body.String())
		}
		if session != nil {
			c2 := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: session.Name, Value: session.Value}) }
			if rec := h.do("GET", "/api/status", "", mod, c2); rec.Code != http.StatusOK {
				t.Fatalf("the new session does not work: %d", rec.Code)
			}
			if session.Secure != (scheme == "https") {
				t.Fatalf("Secure=%v for a %s sign-in", session.Secure, scheme)
			}
			if !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
				t.Fatalf("session cookie attributes: HttpOnly=%v SameSite=%v", session.HttpOnly, session.SameSite)
			}
		}
	})
}
