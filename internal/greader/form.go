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
// false it is exactly the phase 1 parser. With repair true (POST bodies of the
// endpoints NNW sends raw folder names to) the run of parts that follows a
// label value and is really the tail of its name is glued back onto it.
func splitPairsLimit(s string, repair bool) (out []pair, ok bool) {
	if s == "" {
		return nil, true
	}
	if strings.Count(s, "&") >= maxPairs {
		return nil, false
	}
	parts := strings.Split(s, "&")
	out = make([]pair, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		e := pair{key: unescape(k), val: unescape(v), rawKey: k, rawVal: v}
		if repair && labelKeys[e.key] {
			if _, isLabel := labelName(e.val); isLabel {
				// Collect the whole run of tail parts, then join and decode once.
				j := i + 1
				for j < len(parts) && j-i <= maxGlueParts && isNameTail(parts[j]) {
					j++
				}
				if j > i+1 {
					e.rawVal = v + "&" + strings.Join(parts[i+1:j], "&")
					e.val = pathUnescape(e.rawVal)
					i = j - 1
				}
			}
		}
		out = append(out, e)
	}
	return out, true
}

// maxGlueParts caps how many '&'-split parts one label name may absorb.
const maxGlueParts = 64

var identKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// isNameTail reports whether a part following a label value is the tail of that
// value's name rather than a new parameter (design §6.2). A real parameter
// always carries '=' with an identifier key; anything else is glued: a part with
// no '=' (the "T" of "AT&T", the empty parts of "R&" and "A&&B") or one whose
// key holds a space or other non-identifier character (" Politics+", "a b=c").
func isNameTail(part string) bool {
	k, _, hasEq := strings.Cut(part, "=")
	return !hasEq || !identKey.MatchString(k)
}

func pathUnescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
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
	defer func() { p.truncated = body.hit }()
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
		v, _ := io.ReadAll(io.LimitReader(part, 1<<20))
		_ = part.Close()
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
