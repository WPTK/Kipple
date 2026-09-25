package greader

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		rest string
		ok   bool
	}{
		{"/api/greader.php", "", true},
		{"/api/greader.php/", "/", true},
		{"/api/greader.php/accounts/ClientLogin", "/accounts/ClientLogin", true},
		{"/api/greader.php//reader/api/0/edit-tag", "/reader/api/0/edit-tag", true},
		{"/api/greader.php/api/greader.php/reader/api/0/token", "/reader/api/0/token", true},
		{"//api//greader.php///reader/api/0/token", "/reader/api/0/token", true},
		{"/api/greader.php/check/compatibility", "/check/compatibility", true},
		{"/api/greader.php/icon/12-abc", "/icon/12-abc", true},
		{"/reader/api/0/token", "/reader/api/0/token", true},
		{"/accounts/ClientLogin", "/accounts/ClientLogin", true},
		{"/api/greader.phpx/reader/api/0/token", "", false},
		{"/api/greader.php/other", "", false},
		{"/", "", false},
		{"/check/compatibility", "", false},
		{"/api/feeds", "", false},
		{"/healthz", "", false},
	}
	for _, c := range cases {
		rest, ok := classify(c.in)
		require.Equal(t, c.ok, ok, c.in)
		if c.ok {
			require.Equal(t, c.rest, rest, c.in)
		}
	}
}

func TestFrontProbesAndFallthrough(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{base, base + "/"} {
		w := h.do(http.MethodGet, p, "", map[string]string{"Authorization": ""})
		require.Equal(t, 200, w.Code, p)
		require.Equal(t, "OK", w.Body.String())
		require.Equal(t, "private, no-cache", w.Header().Get("Cache-Control"))
	}
	w := h.do(http.MethodGet, base+"/check/compatibility", "", nil)
	require.Equal(t, "PASS", w.Body.String())

	w = h.do(http.MethodGet, "/healthz", "", nil)
	require.Equal(t, http.StatusTeapot, w.Code)
	w = h.do(http.MethodGet, "/api/feeds", "", nil)
	require.Equal(t, http.StatusTeapot, w.Code)
}

func TestFrontNoRedirectsAndBodiesArrive(t *testing.T) {
	h := newHarness(t)
	var got string
	h.api.routes["echo-write"] = route{post: true, h: func(c *call) { got = c.p.Get("i"); c.ok() }}
	for _, path := range []string{
		base + "//reader/api/0/echo-write",
		base + "/reader/api/0/echo-write",
		base + "/api/greader.php/reader/api/0/echo-write",
		"/reader/api/0/echo-write",
		"//reader//api/0/echo-write",
	} {
		got = ""
		w := h.do(http.MethodPost, path, "T="+h.tok+"&i=42", nil)
		require.Equal(t, 200, w.Code, path)
		require.Equal(t, "OK", w.Body.String(), path)
		require.Equal(t, "42", got, path)
	}
}

func TestUnknownEndpointIsEmptyJSONArrayAfterAuth(t *testing.T) {
	h := newHarness(t)
	w := h.get(rd + "no-such-thing")
	require.Equal(t, 200, w.Code)
	require.JSONEq(t, "[]", w.Body.String())
	w = h.do(http.MethodGet, base+rd+"no-such-thing", "", map[string]string{"Authorization": ""})
	require.Equal(t, 401, w.Code)
}

func TestWriteEndpointRejectsGET(t *testing.T) {
	h := newHarness(t)
	h.api.routes["echo-write"] = route{post: true, h: func(c *call) { c.ok() }}
	w := h.get(rd + "echo-write")
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}
