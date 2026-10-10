package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/WPTK/kipple/internal/opml"
	"github.com/WPTK/kipple/internal/sched"
)

const maxOPMLBody = opml.MaxFileBytes

// opmlImport is POST /api/opml: raw OPML or a multipart upload (first file
// part), optional ?mark_read_older_than_days=N (1-365) and ?move_existing=true
// (move feeds that already exist into the file's folders; default false). New
// feeds are inserted due now and followed by an import run.
func (s *Server) opmlImport(w http.ResponseWriter, r *http.Request) {
	var opts opml.ImportOptions
	if v := r.URL.Query().Get("mark_read_older_than_days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, http.StatusBadRequest, "bad_days")
			return
		}
		opts.MarkReadOlderThanDays = n
	}
	switch r.URL.Query().Get("move_existing") {
	case "", "false":
	case "true":
		opts.MoveExisting = true
	default:
		writeError(w, http.StatusBadRequest, "bad_move_existing")
		return
	}
	body, err := readOPMLBody(w, r)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	doc, err := opml.Parse(bytes.NewReader(body))
	if errors.Is(err, opml.ErrNotOPML) {
		writeError(w, http.StatusBadRequest, "not_opml")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_opml")
		return
	}
	if len(doc.Feeds) == 0 && len(doc.Folders) == 0 {
		writeErrorMsg(w, http.StatusBadRequest, "empty_opml", "that OPML file has no feeds or folders in it")
		return
	}
	res, err := opml.Import(r.Context(), s.db, doc, opts)
	if err != nil {
		s.serverError(w, "opml import", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		opml.Result
		RunID any `json:"run_id"`
	}{res, s.afterImport(res)})
}

// afterImport is what follows every import (OPML, recommended feeds): the
// folder event when folders were made, and an import run over the new feeds.
// It returns the run id, or nil when nothing was started.
func (s *Server) afterImport(res opml.Result) any {
	if res.FoldersCreated > 0 || len(res.FeedsMoved) > 0 {
		s.publishFolderChanged(0)
	}
	for _, m := range res.FeedsMoved {
		s.publishFeedChanged(m.FeedID)
	}
	if len(res.NewFeedIDs) == 0 {
		return nil
	}
	info, err := s.opt.Sched.StartImport(res.NewFeedIDs)
	switch {
	case err == nil:
		return fmt.Sprint(info.RunID)
	case errors.Is(err, sched.ErrStopped):
		// imported; the scheduler will not run again this process
	default:
		s.log.Error("api: import run", "err", err)
	}
	return nil
}

func readOPMLBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxOPMLBody)
	mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				return nil, fmt.Errorf("no file part: %w", err)
			}
			if p.FileName() != "" || p.FormName() == "file" || p.FormName() == "opml" {
				return io.ReadAll(p)
			}
		}
	}
	return io.ReadAll(r.Body)
}

// opmlExport is GET /api/opml (design §7.6): an attachment.
func (s *Server) opmlExport(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	if err := opml.Export(r.Context(), s.db, &buf); err != nil {
		s.serverError(w, "opml export", err)
		return
	}
	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="kipple.opml"`)
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(buf.Bytes())
}
