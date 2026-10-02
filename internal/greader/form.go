package greader

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// maxBody caps a form body read (design §6.2). A 1000-id NNW edit-tag is ~60 KB.
const maxBody = 4 << 20

// maxPairs caps the parsed key=value pairs per part of a request; beyond it the
// request is a 400 (a 4 MiB "a&a&a..." body would otherwise allocate megabytes).
const maxPairs = 20000

// maxLoginBody caps a ClientLogin body and any body read before the caller has
// authenticated by header.
const maxLoginBody = 64 << 10

// pair is one key=value from a body or query string, decoded and raw.
type pair struct {
	key, val       string // QueryUnescape'd (raw text kept on an unescape error)
	rawKey, rawVal string // exactly as sent
}

// Params is a Reader API request's parameters: the body pairs and the query
// pairs, both parsed by splitting on '&' only and on the first '=' only, so the
// unencoded ';', '=', ',' and '$' NetNewsWire sends survive (design §6.2).
// r.ParseForm is never used.
type Params struct {
	body    []pair
	query   []pair
	rawBody string
	// tooMany is set when a query string or body exceeded maxPairs.
	tooMany bool
	// truncated is set when the body was longer than the read cap. What was
	// parsed is then incomplete (a T= or i= past the cap is missing), so callers
	// answer 413 instead of acting on it.
	truncated bool
}

// capReader reads at most left bytes and records whether the source held more.
type capReader struct {
	r    io.Reader
	left int64
	hit  bool
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var b [1]byte
		if n, _ := c.r.Read(b[:]); n > 0 {
			c.hit = true
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// splitPairsLimit implements steps 2-3 of §6.2 and refuses (ok false, before
// allocating the parts) an input with more than maxPairs pairs. With repair
// false it is exactly the phase 1 parser. With repair true (the POST body of disable-tag, the one
// endpoint NNW sends a raw folder id to) the run of parts that follows a
// label value and is really the tail of its name is glued back onto it.
func splitPairsLimit(s string, repair bool) (out []pair, ok bool) {
	if s == "" {
		return nil, true
	}
	work(len(s))
	if strings.Count(s, "&") >= maxPairs {
		return nil, false
	}
	work(len(s))
	parts := strings.Split(s, "&")
	out = make([]pair, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			continue
		}
		work(len(part))
		k, v, _ := strings.Cut(part, "=")
		e := pair{key: unescape(k), val: unescape(v), rawKey: k, rawVal: v}
		if repair && labelKeys[e.key] {
			if hasLabelPrefix(e.val) {
				// A value with a valid %XX escape was form-encoded by its client, so an empty part after it is
				// a stray '&' (or the end of the body), not the tail of the name; raw names keep "R&" and "A&&B".
				encoded := hasEscape(v)
				// Collect the whole run of tail parts, then join and decode once.
				j := i + 1
				for j < len(parts) && j-i <= maxGlueParts && isNameTail(parts[j]) && !(encoded && parts[j] == "") {
					work(len(parts[j]))
					j++
				}
				if j > i+1 {
					e.rawVal = v + "&" + strings.Join(parts[i+1:j], "&")
					work(len(e.rawVal))
					e.val = lenientUnescape(e.rawVal)
					i = j - 1
				}
			}
		}
		out = append(out, e)
	}
	return out, true
}

// hasEscape reports whether s holds a valid %XX escape.
func hasEscape(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == '%' && isHex(s[i+1]) && isHex(s[i+2]) {
			return true
		}
	}
	return false
}

// maxGlueParts caps how many '&'-split parts one label name may absorb.
const maxGlueParts = 64

var identKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// hasLabelPrefix reports whether v starts user/<x>/label/, whatever follows (the
// name may be empty or blank before its first '&': "user/-/label/&Co").
func hasLabelPrefix(v string) bool {
	rest, ok := strings.CutPrefix(v, "user/")
	if !ok {
		return false
	}
	_, _, ok = strings.Cut(rest, "/label/")
	return ok
}

// isParamKey reports whether a key (as sent) is a request parameter rather than
// part of a folder name: an identifier, one whose percent-decoded form is an
// identifier ("%54" is T), or a vendor style key with a dash or dot and no space
// ("x-client", "client.id").
func isParamKey(k string) bool {
	if k == "" {
		return false
	}
	dec := unescape(k)
	if identKey.MatchString(k) || identKey.MatchString(dec) {
		return true
	}
	if strings.ContainsAny(k, " +") || strings.Contains(dec, " ") {
		return false
	}
	return strings.ContainsAny(k, "-.") || strings.ContainsAny(dec, "-.")
}

// isNameTail reports whether a part following a label value is the tail of that
// value's name rather than a new parameter (design §6.2). A real parameter
// always carries '=' with a parameter key; anything else is glued: a part with no
// '=' (the "T" of "AT&T", the empty parts of "R&" and "A&&B") or one whose key is
// not a parameter key (" Politics+", "a b=c").
func isNameTail(part string) bool {
	k, _, hasEq := strings.Cut(part, "=")
	return !hasEq || !isParamKey(k)
}

// lenientUnescape decodes every valid %XX and leaves an invalid escape literal,
// so one bad escape never blocks the rest. '+' stays '+'.
func lenientUnescape(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

// labelKeys are the parameters that carry a user/-/label/<name> id.
var labelKeys = map[string]bool{"s": true, "a": true, "r": true, "dest": true}

func unescape(s string) string {
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}

// readParams parses the query string and, for a POST, the body. Non-multipart
// bodies are always treated as urlencoded, whatever the media type. When raw is
// true the body is kept undecoded and unparsed (subscription/import reads OPML).
func readParams(r *http.Request, raw bool) *Params { return readParamsLimit(r, raw, maxBody, false) }

// readParamsLimit is readParams with a body size cap of limit bytes. repair
// enables the raw folder name repair for the body (never the query).
func readParamsLimit(r *http.Request, raw bool, limit int64, repair bool) *Params {
	p := &Params{}
	var ok bool
	if p.query, ok = splitPairsLimit(r.URL.RawQuery, false); !ok {
		p.tooMany = true
		return p
	}
	if r.Method != http.MethodPost || r.Body == nil {
		return p
	}
	mt, mparams, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		mt = ""
	}
	body := &capReader{r: r.Body, left: limit}
	defer func() { p.truncated = p.truncated || body.hit }()
	if raw {
		b, _ := io.ReadAll(body)
		p.rawBody = string(b)
		return p
	}
	if mt == "multipart/form-data" {
		if boundary := mparams["boundary"]; boundary != "" {
			p.readMultipart(multipart.NewReader(body, boundary))
			return p
		}
	}
	b, _ := io.ReadAll(body)
	p.rawBody = string(b)
	if p.body, ok = splitPairsLimit(p.rawBody, repair); !ok {
		p.tooMany = true
	}
	return p
}

// maxPartValue caps one multipart form value; a longer one marks the parse truncated.
const maxPartValue = 1 << 20

func (p *Params) readMultipart(mr *multipart.Reader) {
	for {
		part, err := mr.NextPart()
		if err != nil {
			return
		}
		name := part.FormName()
		if name == "" || part.FileName() != "" {
			_ = part.Close()
			continue
		}
		v, _ := io.ReadAll(io.LimitReader(part, maxPartValue+1))
		_ = part.Close()
		if len(v) > maxPartValue {
			// Never act on a cut value (rejectParams answers 413).
			p.truncated = true
			return
		}
		if len(p.body) >= maxPairs {
			p.tooMany = true
			return
		}
		p.body = append(p.body, pair{key: name, val: string(v), rawKey: name, rawVal: string(v)})
	}
}

// Get returns the first value of k, checking the body before the query.
func (p *Params) Get(k string) string {
	for _, e := range p.body {
		if e.key == k {
			return e.val
		}
	}
	for _, e := range p.query {
		if e.key == k {
			return e.val
		}
	}
	return ""
}

// Has reports whether k is present at all (even with an empty value).
func (p *Params) Has(k string) bool {
	for _, e := range p.body {
		if e.key == k {
			return true
		}
	}
	for _, e := range p.query {
		if e.key == k {
			return true
		}
	}
	return false
}

// All returns every value of k, body values first, in order.
func (p *Params) All(k string) []string {
	var out []string
	for _, e := range p.body {
		if e.key == k {
			out = append(out, e.val)
		}
	}
	for _, e := range p.query {
		if e.key == k {
			out = append(out, e.val)
		}
	}
	return out
}

// AllRaw is All without unescaping (label lookup candidates, §6.2).
func (p *Params) AllRaw(k string) []string {
	var out []string
	for _, e := range p.body {
		if e.key == k || e.rawKey == k {
			out = append(out, e.rawVal)
		}
	}
	for _, e := range p.query {
		if e.key == k || e.rawKey == k {
			out = append(out, e.rawVal)
		}
	}
	return out
}

// RawBody returns the undecoded body.
func (p *Params) RawBody() string { return p.rawBody }

// BodyEmpty reports whether the request carried no body pairs and no raw body.
func (p *Params) BodyEmpty() bool { return len(p.body) == 0 && p.rawBody == "" }

// keys returns the distinct keys, sorted, for logging (never values).
func (p *Params) keys(pairs []pair) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range pairs {
		if !seen[e.key] {
			seen[e.key] = true
			out = append(out, e.key)
		}
	}
	sort.Strings(out)
	return out
}

// bodyKeys and queryKeys list the parameter names present.
func (p *Params) bodyKeys() []string  { return p.keys(p.body) }
func (p *Params) queryKeys() []string { return p.keys(p.query) }

// workHook is a test seam: when set, the splitter reports each scan, copy or join
// it does as a byte count, so a test can prove its work is linear in the input
// without a stopwatch. Nil in production: each call is one predictable branch.
var workHook func(n int)

func work(n int) {
	if workHook != nil {
		workHook(n)
	}
}
