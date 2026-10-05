package api

import (
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/buildinfo"
	"github.com/WPTK/kipple/internal/reach"
	"github.com/WPTK/kipple/internal/store"
)

func TestAboutRequiresASession(t *testing.T) {
	h := newHarness(t)
	require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/about", "").Code)
}

func TestAboutReportsTheBuildAndNothingPrivate(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, func(o *Options) {
		o.Version = "v0.5.0-beta.1"
		o.Build = buildinfo.Info{Commit: "0123456789abcdef0123456789abcdef01234567", BuildDate: "2026-10-01T12:00:00Z"}
		o.WebBuild = "3fa9c01b2d"
		o.DataDir = dir
		o.Reach = reach.Fixed(reach.State{PublicURL: "https://reader.example.test"})
	})
	code, out, rec := h.api(h.login(), "GET", "/api/about", "")
	require.Equal(t, http.StatusOK, code)

	require.Equal(t, "v0.5.0-beta.1", out["version"])
	require.Equal(t, "0123456789abcdef0123456789abcdef01234567", out["commit"])
	require.Equal(t, "2026-10-01T12:00:00Z", out["build_date"])
	require.Contains(t, out["go_version"], "go")
	require.Contains(t, out["os_arch"], "/")
	require.EqualValues(t, store.LatestVersion(), out["schema_version"])
	require.EqualValues(t, store.LatestVersion(), out["schema_latest"])
	require.NotEmpty(t, out["sqlite_version"])
	require.NotEmpty(t, out["started_at"])
	require.GreaterOrEqual(t, out["uptime_s"], float64(0))
	require.Equal(t, true, out["data_dir_writable"])
	require.Equal(t, "password", out["auth_mode"])
	require.Equal(t, false, out["access_enabled"])
	require.Equal(t, true, out["public_url_set"])
	require.Equal(t, "3fa9c01b2d", out["web_build"])

	// Nothing a person would not paste into a public issue: no username, no address, no data path.
	for _, secret := range []string{testUser, "reader.example.test", dir} {
		require.NotContains(t, rec.Body.String(), secret)
	}
	// The write check leaves nothing behind.
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, ents)
}

func TestAboutDataDirNotWritable(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.DataDir = t.TempDir() + "/does/not/exist" })
	_, out, _ := h.api(h.login(), "GET", "/api/about", "")
	require.Equal(t, false, out["data_dir_writable"])
}

func TestAboutUnsetBuildFieldsAreUnknown(t *testing.T) {
	h := newHarness(t)
	_, out, _ := h.api(h.login(), "GET", "/api/about", "")
	require.Equal(t, "unknown", out["commit"])
	require.Equal(t, "unknown", out["build_date"])
}

func TestBootstrapCarriesTheWebBuild(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.WebBuild = "abc123def4" })
	_, out, _ := h.api(h.login(), "GET", "/api/bootstrap", "")
	require.Equal(t, "abc123def4", out["web_build"])
}
