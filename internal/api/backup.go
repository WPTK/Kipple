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

// backupCreate is POST /api/backup. The export runs as a background job that
// outlives the request (a build near the limit outlasts a tunnel's ~100 s origin
// timeout, and a client that disconnects must not cancel it). When it finishes
// within the sync window (5 s by default) the answer is the ready payload, 200,
// as before; otherwise 202 {job_id, status:"building"} and the client polls
// GET /api/backup/jobs/{id}.
func (s *Server) backupCreate(w http.ResponseWriter, r *http.Request) {
	job, err := s.backups.Start()
	if errors.Is(err, backup.ErrBusy) {
		w.Header().Set("Retry-After", "10")
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "busy", "retry_after": 10,
			"message": "A backup or the nightly snapshot is already running. Try again in a few seconds.",
		})
		return
	}
	if err != nil {
		s.serverError(w, "backup export", err)
		return
	}
	timer := time.NewTimer(s.backups.SyncWait())
	defer timer.Stop()
	select {
	case <-job.Done():
	case <-timer.C:
	case <-r.Context().Done():
	}
	st := job.State()
	switch st.Status {
	case backup.JobReady:
		writeJSON(w, http.StatusOK, s.backupPayload(st.Export))
	case backup.JobFailed:
		s.backupFailure(w, st.Err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": backup.JobBuilding})
	}
}

// backupFailure maps a failed build to its status.
func (s *Server) backupFailure(w http.ResponseWriter, err error) {
	var noSpace *backup.NoSpaceError
	switch {
	case errors.As(err, &noSpace):
		writeErrorMsg(w, http.StatusInsufficientStorage, "no_space", noSpace.Error())
	case errors.Is(err, backup.ErrTooLarge):
		writeErrorMsg(w, http.StatusRequestEntityTooLarge, "too_large",
			"The database is larger than the in-app export limit. Take the nightly snapshot from the server instead (docs/deploy.md).")
	default:
		s.serverError(w, "backup export", err)
	}
}

// backupPayload is the ready answer, shared by POST and the job poll.
func (s *Server) backupPayload(exp backup.Export) map[string]any {
	return map[string]any{
		"status":     backup.JobReady,
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
	}
}

// backupJob is GET /api/backup/jobs/{id}: the state of a background export.
// building: {status}. ready: the payload of POST. failed: {status, error,
// message}. 404 gone once a finished export was downloaded, replaced or expired.
func (s *Server) backupJob(w http.ResponseWriter, r *http.Request) {
	st, err := s.backups.Job(r.PathValue("id"))
	if err != nil {
		writeErrorMsg(w, http.StatusNotFound, "gone", "No such export. Export again.")
		return
	}
	switch st.Status {
	case backup.JobReady:
		writeJSON(w, http.StatusOK, s.backupPayload(st.Export))
	case backup.JobFailed:
		code, msg := "internal", "The export failed. See the server log."
		var noSpace *backup.NoSpaceError
		switch {
		case errors.As(st.Err, &noSpace):
			code, msg = "no_space", noSpace.Error()
		case errors.Is(st.Err, backup.ErrTooLarge):
			code, msg = "too_large", "The database is larger than the in-app export limit. Take the nightly snapshot from the server instead (docs/deploy.md)."
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": backup.JobFailed, "error": code, "message": msg})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"status": backup.JobBuilding})
	}
}

// backupDownload is GET /api/backup/{token}: the zip as an attachment. The
// route needs the web session as well as the
// token, which is spent by this request whether or not the transfer completes
// (a failed one is retried with a new export). The file is deleted afterwards.
func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// HEAD reaches this handler through the GET pattern; answering it would
		// spend the single-use token without sending the file.
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method")
		return
	}
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
