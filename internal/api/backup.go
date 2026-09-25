package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/WPTK/kipple/internal/backup"
)

const (
	// downloadChunk is the copy unit of a backup download; each chunk gets its
	// own write deadline, so a stalled client is dropped after downloadStall
	// while a slow but moving one is not.
	downloadChunk = 256 << 10
	downloadStall = 60 * time.Second
	// downloadMax bounds one whole download.
	downloadMax = 30 * time.Minute
)

// backupCreate is POST /api/backup: build an export (a zip of a consistent
// snapshot, feeds.opml, settings.json, a manifest and RESTORE.txt) and answer
// with a single-use download token. The build can outlast the server's 60 s
// WriteTimeout, so the handler extends its own write deadline to the build limit.
func (s *Server) backupCreate(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(s.backups.BuildTimeout() + 30*time.Second))

	exp, err := s.backups.Create(r.Context())
	var noSpace *backup.NoSpaceError
	switch {
	case err == nil:
	case errors.Is(err, backup.ErrBusy):
		w.Header().Set("Retry-After", "10")
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "busy", "retry_after": 10,
			"message": "A backup or the nightly snapshot is already running. Try again in a few seconds.",
		})
		return
	case errors.As(err, &noSpace):
		writeErrorMsg(w, http.StatusInsufficientStorage, "no_space", noSpace.Error())
		return
	case errors.Is(err, backup.ErrTooLarge):
		writeErrorMsg(w, http.StatusRequestEntityTooLarge, "too_large",
			"The database is larger than the in-app export limit. Take the nightly snapshot from the server instead (docs/deploy.md).")
		return
	default:
		s.serverError(w, "backup export", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      exp.Token,
		"url":        "/api/backup/" + exp.Token,
		"filename":   exp.Filename,
		"bytes":      exp.Bytes,
		"expires_at": exp.ExpiresAt.Unix(),
		"expires_in": int(s.backups.TTL() / time.Second),
		"warning":    backup.Warning,
		"contents": map[string]any{
			"kipple_version": exp.Manifest.KippleVersion,
			"schema_version": exp.Manifest.SchemaVersion,
			"created_at":     exp.Manifest.CreatedAt,
			"feeds":          exp.Manifest.Feeds,
			"items":          exp.Manifest.Items,
			"starred":        exp.Manifest.Starred,
			"db_bytes":       exp.Manifest.DBBytes,
		},
	})
}

// backupDownload is GET /api/backup/{token}: the zip as an attachment. The
// route needs the web session and the same-origin download rule as well as the
// token, which is spent by this request whether or not the transfer completes
// (a failed one is retried with a new export). The file is deleted afterwards.
func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request) {
	d, err := s.backups.Take(r.PathValue("token"))
	if err != nil {
		if errors.Is(err, backup.ErrBadToken) {
			writeErrorMsg(w, http.StatusNotFound, "gone", "This download link has expired or was already used. Export again.")
			return
		}
		s.serverError(w, "backup download", err)
		return
	}
	defer d.Close()

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="`+d.Filename+`"`)
	h.Set("Content-Length", strconv.FormatInt(d.Bytes, 10))
	h.Set("Cache-Control", "private, no-store")
	h.Set("Accept-Ranges", "none")

	deadline := time.Now().Add(downloadMax)
	buf := make([]byte, downloadChunk)
	for {
		if time.Now().After(deadline) || r.Context().Err() != nil {
			return
		}
		n, rerr := d.File.Read(buf)
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(downloadStall))
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				s.log.Error("api: backup download read", "err", rerr)
			}
			return
		}
	}
}
