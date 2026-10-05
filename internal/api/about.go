package api

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/WPTK/kipple/internal/buildinfo"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

// about is GET /api/about: what this server is, for the About screen and its "Copy debug info" text. It carries
// no username, no hostname, no address and no path (only whether the data directory is writable): the block is
// meant to be pasted into a public issue. Nothing here contacts anything.
func (s *Server) about(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acct, _, err := s.db.Account(ctx)
	if err != nil {
		s.serverError(w, "about", err)
		return
	}
	schema, err := s.db.Version(ctx)
	if err != nil {
		s.serverError(w, "about", err)
		return
	}
	b := s.opt.Build
	b.Version = s.opt.Version
	b = b.Normalized()
	// The mode as every other endpoint names it: "open" is its own mode, not an
	// Access account that happens to have no password.
	authMode := setup.DisplayMode(acct)
	now := s.now()
	uptime := int64(now.Sub(s.started).Seconds())
	if uptime < 0 {
		uptime = 0
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"version":           b.Version,
		"commit":            b.Commit,
		"build_date":        b.BuildDate,
		"go_version":        buildinfo.GoVersion(),
		"os_arch":           buildinfo.OSArch(),
		"schema_version":    schema,
		"schema_latest":     store.LatestVersion(),
		"sqlite_version":    s.db.SQLiteVersion(ctx),
		"started_at":        s.started.UTC().Format(time.RFC3339),
		"uptime_s":          uptime,
		"data_dir_writable": dataDirWritable(s.opt.DataDir),
		"tz":                store.Zone(ctx, s.db.Reader()).String(), // the zone in force now (the tz setting), as statistics use it
		"auth_mode":         authMode,
		"access_enabled":    s.reach.Access() != nil,
		"public_url_set":    s.reach.PublicURL() != "",
		"web_build":         s.opt.WebBuild,
		"last_version":      s.db.LastVersion(ctx),
	})
}

// dataDirWritable creates and removes a temporary file in dir, on every call. An empty dir (tests) reports false.
func dataDirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	f, err := os.CreateTemp(dir, ".kipple-write-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(filepath.Clean(name)) == nil
}
