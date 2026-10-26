package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/sched"
)

// The two waits are variables so tests can shorten them.
var (
	addWait     = 8 * time.Second  // POST /api/feeds waits this long for the first fetch
	refreshWait = 15 * time.Second // POST /api/feeds/{id}/refresh
)

const (
	discoverWait  = 10 * time.Second
	maxTitleRunes = fetch.MaxTitleRunes
	maxFolderName = 100
	maxUAAuthLen  = 500
)

func writeErrorMsg(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, map[string]string{"error": kind, "message": msg})
}

// readObject decodes a JSON object body into raw fields and rejects any key not
// in allowed (400 unknown_field).
func readObject(w http.ResponseWriter, r *http.Request, allowed ...string) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if !decodeBody(w, r, &m, false) {
		return nil, false
	}
	if m == nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return nil, false
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for k := range m {
		if !ok[k] {
			writeErrorMsg(w, http.StatusBadRequest, "unknown_field", "unknown field "+k)
			return nil, false
		}
	}
	return m, true
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func rawBool(raw json.RawMessage) (bool, bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil || isNull(raw) {
		return false, false
	}
	return b, true
}

func rawInt(raw json.RawMessage) (int64, bool) {
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] == '"' { // a JSON string is not a number
		return 0, false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var num json.Number
	if err := d.Decode(&num); err != nil {
		return 0, false
	}
	n, err := num.Int64()
	return n, err == nil
}

func rawString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || isNull(raw) {
		return "", false
	}
	return s, true
}

// hasControl reports a control character: anything below 0x20 except tab, and
// DEL (the HTTP header-value rule). It is the one check for every user-supplied
// text that ends up in a header or a display name (titles, folder names, the
// per-feed User-Agent and HTTP auth, the custom User-Agent setting).
func hasControl(s string) bool {
	for i := 0; i < len(s); i++ { // bytes: a multi-byte rune never has a byte below 0x80
		if c := s[i]; c < 0x20 && c != '	' || c == 0x7f {
			return true
		}
	}
	return false
}

// titleInput checks a feed name the user typed and returns it cleaned by the one name rule
// (fetch.CleanName): refused for a real control character (hasControl, outside the surrounding
// whitespace) or for more than maxTitleRunes characters once invisible ones are gone, so a trailing
// zero-width character never turns a valid name into an error. "" means no name.
func titleInput(s string, ok bool) (string, bool) {
	if !ok || hasControl(strings.TrimSpace(s)) || fetch.NameRunes(s) > maxTitleRunes {
		return "", false
	}
	return fetch.CleanName(s), true
}

// awaitReply waits for a priority job's reply, the timer, scheduler shutdown or
// the client going away.
func (s *Server) awaitReply(r *http.Request, ch <-chan sched.Reply, d time.Duration) (rep sched.Reply, got bool, err error) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case rep = <-ch:
		return rep, true, nil
	case <-t.C:
		return rep, false, nil
	case <-s.opt.Sched.Shutdown():
		return rep, false, sched.ErrStopped
	case <-r.Context().Done():
		return rep, false, r.Context().Err()
	}
}

func (s *Server) publishFeedChanged(id int64) {
	if s.opt.Hub != nil {
		s.opt.Hub.Publish("feed.changed", map[string]any{"feed_id": idStr(id)})
	}
}

// publishFolderChanged announces a folder mutation (create, rename, delete, order,
// membership); id 0 means "several or unknown", sent without folder_id.
func (s *Server) publishFolderChanged(id int64) {
	if s.opt.Hub == nil {
		return
	}
	if id == 0 {
		s.opt.Hub.Publish("folder.changed", map[string]any{})
		return
	}
	s.opt.Hub.Publish("folder.changed", map[string]any{"folder_id": idStr(id)})
}

func idStr(id int64) string { return idStrings([]int64{id})[0] }

type fetchOutcome struct {
	Pending  bool   `json:"pending,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	NewItems int    `json:"new_items"`
	ErrClass string `json:"error_class,omitempty"`
	Error    string `json:"error,omitempty"`
}

func outcomeOf(rep sched.Reply) fetchOutcome {
	return fetchOutcome{Outcome: rep.Outcome, NewItems: rep.NewItems, ErrClass: rep.ErrClass, Error: rep.ErrMsg}
}
