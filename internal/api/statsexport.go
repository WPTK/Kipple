package api

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

// statsWriteWindow is how long one page of an export may take to write: the server's 60 s
// WriteTimeout would otherwise cut a long export off, so each page extends it.
const statsWriteWindow = 60 * time.Second

// csvHeader is the raw export's CSV header, in statsColumns order.
func csvHeader() []string {
	h := make([]string, len(statsColumns))
	for i, c := range statsColumns {
		h[i] = c.Name
	}
	return h
}

// csvText guards a text cell against spreadsheet formula injection: a cell that starts with
// = + - @ tab or carriage return gets a single quote in front, so it is shown as text.
func csvText(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		s = "'" + s
	}
	return crNormalizer.Replace(s)
}

// crNormalizer turns CRLF and a lone CR inside a text cell into LF. encoding/csv drops a lone CR
// from a quoted field without a word and writes each LF as CRLF, so normalising first keeps every
// line break the title had.
var crNormalizer = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// csvRecord is one raw row as CSV cells.
func csvRecord(r *store.StatsExportRow, titles bool) []string {
	i64 := func(n int64) string { return strconv.FormatInt(n, 10) }
	rec := []string{
		i64(r.ID), i64(r.TS), r.LocalDate, strconv.Itoa(r.LocalHour), strconv.Itoa(r.LocalWeekday),
		csvText(r.Kind), csvText(r.Client), strconv.Itoa(r.Inferred), i64(r.ItemID), i64(r.FeedID), csvText(r.FeedTitle),
		"", csvText(r.FolderName.String), "", "", "", csvText(r.SessionKey.String),
	}
	if r.FolderID.Valid {
		rec[11] = i64(r.FolderID.Int64)
	}
	if titles {
		rec[13], rec[14] = csvText(r.ItemTitle.String), csvText(r.ItemURL.String)
	}
	if r.Value.Valid {
		rec[15] = i64(r.Value.Int64)
	}
	return rec
}

// exportEvent is one raw row as JSON. Ids are strings (design section 7); empty columns are null.
type exportEvent struct {
	ID           string  `json:"id"`
	TS           int64   `json:"ts"`
	LocalDate    string  `json:"local_date"`
	LocalHour    int     `json:"local_hour"`
	LocalWeekday int     `json:"local_weekday"`
	Kind         string  `json:"kind"`
	Client       string  `json:"client"`
	Inferred     int     `json:"inferred"`
	ItemID       string  `json:"item_id"`
	FeedID       string  `json:"feed_id"`
	FeedTitle    string  `json:"feed_title"`
	FolderID     *string `json:"folder_id"`
	FolderName   *string `json:"folder_name"`
	ItemTitle    *string `json:"item_title"`
	ItemURL      *string `json:"item_url"`
	Value        *int64  `json:"value"`
	SessionKey   *string `json:"session_key"`
}

func jsonEvent(r *store.StatsExportRow, titles bool) exportEvent {
	i64 := func(n int64) string { return strconv.FormatInt(n, 10) }
	e := exportEvent{ID: i64(r.ID), TS: r.TS, LocalDate: r.LocalDate, LocalHour: r.LocalHour, LocalWeekday: r.LocalWeekday,
		Kind: r.Kind, Client: r.Client, Inferred: r.Inferred, ItemID: i64(r.ItemID), FeedID: i64(r.FeedID), FeedTitle: r.FeedTitle}
	if r.FolderID.Valid {
		s := i64(r.FolderID.Int64)
		e.FolderID = &s
	}
	if r.FolderName.Valid {
		e.FolderName = &r.FolderName.String
	}
	if titles && r.ItemTitle.Valid {
		e.ItemTitle = &r.ItemTitle.String
	}
	if titles && r.ItemURL.Valid {
		e.ItemURL = &r.ItemURL.String
	}
	if r.Value.Valid {
		e.Value = &r.Value.Int64
	}
	if r.SessionKey.Valid {
		e.SessionKey = &r.SessionKey.String
	}
	return e
}

type exportRange struct {
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
	// LastEventDate is the newest local_date of any row, which can be after today (rows written
	// under another time zone): a raw "all" export is unbounded and includes such rows.
	LastEventDate *string `json:"last_event_date"`
}

// summaryExport is the summary envelope: the summary object as GET /api/stats/summary returns it,
// with the export fields beside it.
type summaryExport struct {
	Format          string `json:"format"`
	Version         int    `json:"version"`
	Content         string `json:"content"`
	ExportedAt      string `json:"exported_at"`
	TitlesIncluded  bool   `json:"titles_included"`
	IncludeInferred bool   `json:"include_inferred"`
	RecordingOn     bool   `json:"recording_enabled"`
	Dictionary      any    `json:"dictionary"`
	*store.StatsSummary
}

func badExport(w http.ResponseWriter, msg string) {
	writeErrorMsg(w, http.StatusBadRequest, "bad_request", msg)
}

// statsExport answers GET /api/stats/export (design section 8): a download of the raw events or of
// the summary. Reader pool only, one short query per page, nothing held across a write. It works
// with recording off, and content=summary then summarises the stored rows (recording_enabled says
// recording is off), unlike GET /api/stats/summary, which answers its empty off shape.
func (s *Server) statsExport(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	ctx := r.Context()
	format, content := qv.Get("format"), qv.Get("content")
	if content == "" {
		content = "raw"
	}
	if format == "" {
		format = "csv"
		if content == "summary" {
			format = "json"
		}
	}
	if format != "csv" && format != "json" && format != "jsonl" {
		badExport(w, "format must be csv, json or jsonl")
		return
	}
	if content != "raw" && content != "summary" {
		badExport(w, "content must be raw or summary")
		return
	}
	if content == "summary" && format != "json" {
		badExport(w, "content=summary is available as format=json only")
		return
	}
	titlesQ := qv.Get("titles")
	if titlesQ != "" && titlesQ != "0" && titlesQ != "1" {
		badExport(w, "titles must be 0 or 1")
		return
	}
	titles := titlesQ != "0"
	bomQ := qv.Get("bom")
	if bomQ != "" && bomQ != "0" && bomQ != "1" {
		badExport(w, "bom must be 0 or 1")
		return
	}
	bom := bomQ == "1" && format == "csv"
	// One export at a time: a download needs no origin proof, so another page can start
	// one, and each streams the whole table.
	if !s.exportBusy.CompareAndSwap(false, true) {
		w.Header().Set("Retry-After", "5")
		writeErrorMsg(w, http.StatusTooManyRequests, "busy", "A statistics export is already running. Try again in a moment.")
		return
	}
	defer s.exportBusy.Store(false)
	rd := s.db.Reader()
	recording, loc, tz, weekStart, err := store.StatsSettings(ctx, rd)
	if err != nil {
		s.serverError(w, "stats export", err)
		return
	}
	now := s.now()
	p, ok := statsRangeParams(qv, now, loc, weekStart, "all")
	if !ok {
		badExport(w, "range must be week, month, year or all, or from and to dates (YYYY-MM-DD, to not before from, at most 3660 days)")
		return
	}
	p.WhenOff = true // a data control: the stored rows are summarised whether or not recording is on
	stamp := now.In(loc).Format("20060102")
	if !titles {
		stamp += "-no-titles"
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	// Metadata for a reader of the CSV or JSONL, which has no envelope.
	h.Set("X-Kipple-Titles-Included", map[bool]string{true: "1", false: "0"}[titles])
	h.Set("X-Kipple-TZ", tz)
	h.Set("X-Kipple-Include-Inferred", map[bool]string{true: "1", false: "0"}[p.IncludeInferred])

	if content == "summary" {
		s.exportSummary(w, r, p, titles, stamp, recording)
		return
	}

	// The range echoed in the envelope. "all" runs from the first date present through today (the
	// server's local date, which the web takes as "today"); the raw "all" export itself is
	// unbounded, so rows dated after today are in it and range.last_event_date names the newest.
	rng := exportRange{Key: p.Key, From: p.From, To: p.To}
	from, to := p.From, p.To
	last, err := store.StatsLastEventDate(ctx, rd)
	if err != nil {
		s.serverError(w, "stats export", err)
		return
	}
	if last != "" {
		rng.LastEventDate = &last
	}
	if p.Key == "all" {
		from, to = "", ""
		first, err := store.StatsFirstEventDate(ctx, rd)
		if err != nil {
			s.serverError(w, "stats export", err)
			return
		}
		today := now.In(loc).Format("2006-01-02")
		if first == "" || first > today {
			first = today
		}
		rng.From, rng.To = first, today
	}
	maxID, err := store.StatsMaxID(ctx, rd)
	if err != nil {
		s.serverError(w, "stats export", err)
		return
	}
	total := 0
	if r.Method != http.MethodHead { // the count costs a quarter of a second per million rows; a HEAD sends no body
		var err error
		if total, err = store.StatsCountUpTo(ctx, rd, from, to, p.IncludeInferred, maxID); err != nil {
			if ctx.Err() == nil {
				s.serverError(w, "stats export", err)
			}
			return
		}
	}
	// X-Kipple-Rows is the number of RECORDS the export holds at the start (the same max id bounds
	// the pages), not lines: a CSV title may contain line breaks, so count CSV records with a CSV
	// parser; JSONL has one record per line; the JSON export's event_count is authoritative. Fewer
	// records than this means a cut-off download, or a delete that ran meanwhile (the rows it
	// removed are gone from later pages). X-Kipple-Rows-Sent is a trailer set after the last page,
	// the count actually written: best effort, since a proxy may drop trailers. It is absent on a
	// download that stopped early.
	if r.Method != http.MethodHead {
		h.Set("X-Kipple-Rows", strconv.Itoa(total))
	}
	page, err := store.StatsExportPage(ctx, rd, from, to, p.IncludeInferred, 0, maxID, store.StatsExportPageSize)
	if err != nil {
		if ctx.Err() == nil {
			s.serverError(w, "stats export", err)
		}
		return
	}

	switch format {
	case "csv":
		h.Set("Content-Type", "text/csv; charset=utf-8")
	case "json":
		h.Set("Content-Type", "application/json; charset=utf-8")
	default:
		h.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	}
	h.Set("Content-Disposition", `attachment; filename="kipple-stats-`+stamp+`.`+format+`"`)
	if r.Method == http.MethodHead {
		return
	}
	h.Set("Trailer", "X-Kipple-Rows-Sent")
	rc := http.NewResponseController(w)
	extend := func() { _ = rc.SetWriteDeadline(time.Now().Add(statsWriteWindow)) }
	extend()

	var cw *csv.Writer
	switch format {
	case "csv":
		if bom {
			if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
				return
			}
		}
		cw = csv.NewWriter(w)
		cw.UseCRLF = true
		if err := cw.Write(csvHeader()); err != nil {
			return
		}
	case "json":
		env := orderedObject{}
		env.add("format", "kipple-stats-export")
		env.add("version", 1)
		env.add("content", "raw")
		env.add("exported_at", now.UTC().Format(time.RFC3339))
		env.add("tz", tz)
		env.add("range", rng)
		env.add("titles_included", titles)
		env.add("include_inferred", p.IncludeInferred)
		env.add("recording_enabled", recording)
		env.add("dictionary", fullDictionary())
		b, err := json.Marshal(env)
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		if _, err := w.Write(append(b[:len(b)-1], []byte(`,"events":[`)...)); err != nil {
			return
		}
	}

	var buf bytes.Buffer
	first := true
	written := 0
	for {
		buf.Reset()
		for i := range page {
			row := &page[i]
			written++
			switch format {
			case "csv":
				if err := cw.Write(csvRecord(row, titles)); err != nil {
					return
				}
			case "json":
				if !first {
					buf.WriteByte(',')
				}
				first = false
				b, _ := json.Marshal(jsonEvent(row, titles))
				buf.WriteString("\n")
				buf.Write(b)
			default:
				b, _ := json.Marshal(jsonEvent(row, titles))
				buf.Write(b)
				buf.WriteByte('\n')
			}
		}
		if cw != nil {
			cw.Flush()
			if cw.Error() != nil {
				return
			}
		} else if _, err := w.Write(buf.Bytes()); err != nil {
			return
		}
		_ = rc.Flush()
		if len(page) < store.StatsExportPageSize {
			break
		}
		after := page[len(page)-1].ID
		if ctx.Err() != nil {
			return
		}
		extend()
		page, err = store.StatsExportPage(ctx, rd, from, to, p.IncludeInferred, after, maxID, store.StatsExportPageSize)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("api: stats export page", "err", err)
				panic(http.ErrAbortHandler) // headers are sent: cut the connection so the truncation is visible
			}
			return
		}
	}
	if format == "json" {
		_, _ = fmt.Fprintf(w, "\n],\"event_count\":%d}\n", written)
	}
	h.Set("X-Kipple-Rows-Sent", strconv.Itoa(written))
}

// exportSummary writes content=summary: the summary of the range as one JSON document.
func (s *Server) exportSummary(w http.ResponseWriter, r *http.Request, p store.StatsSummaryParams, titles bool, stamp string, recording bool) {
	ctx := r.Context()
	select {
	case s.statsGate <- struct{}{}:
		defer func() { <-s.statsGate }()
	case <-ctx.Done():
		return
	}
	sum, err := store.StatsSummaryFor(ctx, s.db.Reader(), p)
	if err != nil {
		if ctx.Err() == nil {
			s.serverError(w, "stats export", err)
		}
		return
	}
	if !titles && sum.Behavior.LongestRead != nil {
		sum.Behavior.LongestRead.Title = ""
	}
	out := summaryExport{Format: "kipple-stats-export", Version: 1, Content: "summary", ExportedAt: p.Now.UTC().Format(time.RFC3339),
		TitlesIncluded: titles, IncludeInferred: p.IncludeInferred, RecordingOn: recording, Dictionary: fullDictionary(), StatsSummary: sum}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="kipple-stats-summary-`+stamp+`.json"`)
	if r.Method == http.MethodHead {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// statsDictionary answers GET /api/stats/dictionary: the data dictionary, JSON or (format=md)
// Markdown text as an attachment.
func (s *Server) statsDictionary(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("format") {
	case "", "json":
		writeJSON(w, http.StatusOK, fullDictionary())
	case "md":
		h := w.Header()
		h.Set("Content-Type", "text/markdown; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="kipple-stats-dictionary.md"`)
		h.Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(dictionaryMarkdown()))
	default:
		badExport(w, "format must be json or md")
	}
}

type statsDeleteBody struct {
	From    string `json:"from"`
	To      string `json:"to"`
	DryRun  bool   `json:"dry_run"`
	All     bool   `json:"all"`
	Confirm string `json:"confirm"`
}

const statsDeleteConfirm = "DELETE ALL"

// statsDelete answers POST /api/stats/delete: it deletes stats_events rows by local date range (or
// all of them with the confirm phrase), or only counts them with dry_run. It touches no other
// table and works with recording off.
func (s *Server) statsDelete(w http.ResponseWriter, r *http.Request) {
	var b statsDeleteBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		badExport(w, "the body must be a JSON object with from and to, or all and confirm")
		return
	}
	ctx := r.Context()
	var from, to string
	if b.All {
		if b.From != "" || b.To != "" {
			badExport(w, "all cannot be combined with from or to")
			return
		}
		if !b.DryRun && b.Confirm != statsDeleteConfirm {
			badExport(w, `deleting everything needs "confirm":"`+statsDeleteConfirm+`"`)
			return
		}
	} else {
		f, e1 := time.Parse("2006-01-02", b.From)
		t, e2 := time.Parse("2006-01-02", b.To)
		if e1 != nil || e2 != nil {
			badExport(w, "from and to must be dates (YYYY-MM-DD)")
			return
		}
		if t.Before(f) {
			badExport(w, "to must not be before from")
			return
		}
		from, to = b.From, b.To
	}
	if b.DryRun {
		n, err := store.StatsCount(ctx, s.db.Reader(), from, to)
		if err != nil {
			s.serverError(w, "stats delete", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"count": n, "deleted": 0})
		return
	}
	rc := http.NewResponseController(w)
	// Each window extends the write deadline, so the server's 60 s WriteTimeout does not cut a long
	// delete. A proxy in front does give up (Cloudflare answers 524 after about 100 s) while the
	// server keeps deleting; the client then sees an error and must re-check with a dry run. A rerun
	// finishes the job.
	n, err := store.StatsDelete(ctx, s.db, from, to, func() { _ = rc.SetWriteDeadline(time.Now().Add(statsWriteWindow)) })
	if err != nil {
		if ctx.Err() != nil {
			s.log.Info("api: stats delete cancelled", "deleted", n, "err", err)
			return
		}
		// Earlier windows may have committed: say how many, and that it is not complete.
		s.log.Error("api: stats delete incomplete", "deleted", n, "all", b.All, "from", from, "to", to, "err", err)
		status, kind := http.StatusInternalServerError, "internal"
		if errors.Is(err, store.ErrMaintenance) {
			status, kind = http.StatusServiceUnavailable, "maintenance"
			w.Header().Set("Retry-After", strconv.Itoa(int(store.MaintenanceRetryAfter/time.Second)))
		}
		writeJSON(w, status, map[string]any{"error": kind, "complete": false, "deleted": n,
			"message": fmt.Sprintf("the delete stopped after removing %d rows; run it again to finish", n)})
		return
	}
	s.log.Info("api: stats delete", "deleted", n, "all", b.All, "from", from, "to", to)
	writeJSON(w, http.StatusOK, map[string]int{"count": n, "deleted": n})
}
