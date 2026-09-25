package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/sanitize"
	"github.com/WPTK/kipple/internal/store"
)

// extractBudget is the whole synchronous extraction (design §7.1).
const extractBudget = 15 * time.Second

// ftCall is one in-flight extraction; concurrent requests for the same item
// share it instead of fetching the page twice.
type ftCall struct {
	done chan struct{}
	res  extract.Result
	err  error
}

type ftFlights struct {
	mu sync.Mutex
	m  map[int64]*ftCall
}

func (f *ftFlights) do(id int64, fn func() (extract.Result, error)) (extract.Result, error) {
	f.mu.Lock()
	if f.m == nil {
		f.m = map[int64]*ftCall{}
	}
	if c, ok := f.m[id]; ok {
		f.mu.Unlock()
		<-c.done
		return c.res, c.err
	}
	c := &ftCall{done: make(chan struct{})}
	f.m[id] = c
	f.mu.Unlock()
	finished := false
	defer func() {
		if !finished {
			c.err = errors.New("fulltext: extraction panicked")
		}
		// runs on panic too, so waiters are released and the id is not stuck
		f.mu.Lock()
		delete(f.m, id)
		f.mu.Unlock()
		close(c.done)
	}()
	c.res, c.err = fn()
	finished = true
	return c.res, c.err
}

type fulltextResponse struct {
	Mode        *int    `json:"mode"`
	Effective   int     `json:"effective"`
	Status      string  `json:"status"` // ok | error | skipped
	ContentHTML *string `json:"content_html"`
	WordCount   int64   `json:"word_count"`
	Error       *string `json:"error"`
}

// itemFulltext is POST /api/items/{id}/fulltext (design §7.1, §7.5).
//
// mode: 1 or 0 sets items.fulltext_mode, null clears it (follow the feed), an
// omitted field keeps it. When the effective mode is 1 and there is no stored
// extraction it extracts synchronously; ?refresh=1 forces a new attempt, which
// is also how a failed extraction is retried (a stored failure is reported as
// is, so opening the article does not refetch a page that already failed).
func (s *Server) itemFulltext(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var body struct {
		Mode json.RawMessage `json:"mode"`
	}
	if !decodeBody(w, r, &body, true) {
		return
	}
	setMode := false
	var mode *int
	switch strings.TrimSpace(string(body.Mode)) {
	case "":
	case "null":
		setMode = true
	case "1", "0":
		setMode = true
		m := int(strings.TrimSpace(string(body.Mode))[0] - '0')
		mode = &m
	default:
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	ctx := r.Context()

	if setMode {
		found, err := s.db.SetFulltextMode(ctx, id, mode)
		if err != nil {
			s.serverError(w, "fulltext mode", err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
	}
	it, found, err := s.db.GetFulltextItem(ctx, id)
	if err != nil {
		s.serverError(w, "fulltext", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found") // includes restore stubs: they have no live row to extract for
		return
	}

	resp := fulltextResponse{Mode: it.Mode, Effective: it.Effective, Status: "skipped"}
	hasGood := it.HasRow && it.HTML != ""
	var html string
	switch {
	case it.Effective != 1:
	case hasGood && !refresh:
		resp.Status, html, resp.WordCount = "ok", it.HTML, it.Words
	case it.HasRow && !hasGood && it.Error != "" && !refresh:
		resp.Status, resp.Error = "error", &it.Error
	default:
		res, err := s.ftFlights.do(id, func() (extract.Result, error) { return s.extractItem(ctx, it) })
		var msg string
		if err != nil {
			var ee *extract.Error
			if errors.As(err, &ee) {
				msg = ee.Msg
			} else {
				msg = "extraction failed"
				s.log.Error("api: fulltext", "err", err)
			}
		}
		save := store.FulltextSave{Error: msg}
		if err == nil {
			save = store.FulltextSave{HTML: res.HTML, Text: res.Text, WordCount: res.WordCount, ImageURL: res.ImageURL, SourceURL: res.SourceURL}
		}
		if serr := s.db.SaveFulltext(context.WithoutCancel(ctx), id, s.now().Unix(), save); serr != nil {
			s.serverError(w, "fulltext save", serr)
			return
		}
		if err == nil {
			resp.Status, html, resp.WordCount = "ok", res.HTML, int64(res.WordCount)
		} else {
			resp.Status, resp.Error = "error", &msg
			if hasGood { // a failed refresh keeps the earlier extraction
				html, resp.WordCount = it.HTML, it.Words
			}
		}
	}
	if html != "" {
		if rw := s.imageRewriters(ctx, []int64{it.FeedID}); rw != nil {
			html = sanitize.RewriteImages(html, rw(it.FeedID))
		}
		resp.ContentHTML = &html
	}
	writeJSON(w, http.StatusOK, resp)
}

// extractItem runs the extraction detached from the request (the result is
// stored even if this caller goes away) under the 15 s budget.
func (s *Server) extractItem(ctx context.Context, it store.FulltextItem) (extract.Result, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), extractBudget)
	defer cancel()
	return s.extractor.Extract(ctx, extract.Target{
		URL: it.URL, UserAgent: it.UserAgent,
		AllowPrivate: it.AllowPrivateNet, InsecureTLS: it.AllowInsecureTLS, NoHTTP2: it.NoHTTP2,
	})
}
