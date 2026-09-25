package greader

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
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
}

// splitPairs implements steps 2-3 of §6.2.
func splitPairs(s string) []pair {
	out, _ := splitPairsLimit(s)
	return out
}

// splitPairsLimit is splitPairs that refuses (ok false, before allocating the
// parts) an input with more than maxPairs pairs.
func splitPairsLimit(s string) (out []pair, ok bool) {
	if s == "" {
		return nil, true
	}
	if strings.Count(s, "&") >= maxPairs {
		return nil, false
	}
	parts := strings.Split(s, "&")
	out = make([]pair, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		out = append(out, pair{key: unescape(k), val: unescape(v), rawKey: k, rawVal: v})
	}
	return out, true
}

func unescape(s string) string {
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}

// readParams parses the query string and, for a POST, the body. Non-multipart
// bodies are always treated as urlencoded, whatever the media type. When raw is
// true the body is kept undecoded and unparsed (subscription/import reads OPML).
func readParams(r *http.Request, raw bool) *Params { return readParamsLimit(r, raw, maxBody) }

// readParamsLimit is readParams with a body size cap of limit bytes.
func readParamsLimit(r *http.Request, raw bool, limit int64) *Params {
	p := &Params{}
	var ok bool
	if p.query, ok = splitPairsLimit(r.URL.RawQuery); !ok {
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
	if raw {
		b, _ := io.ReadAll(io.LimitReader(r.Body, limit))
		p.rawBody = string(b)
		return p
	}
	if mt == "multipart/form-data" {
		if boundary := mparams["boundary"]; boundary != "" {
			p.readMultipart(multipart.NewReader(io.LimitReader(r.Body, limit), boundary))
			return p
		}
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, limit))
	p.rawBody = string(b)
	if p.body, ok = splitPairsLimit(p.rawBody); !ok {
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
