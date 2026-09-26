package greader

import (
	"strconv"
	"strings"

	"github.com/WPTK/kipple/internal/store"
)

// stateName returns the name in user/<x>/state/com.google/<name>.
func stateName(id string) (string, bool) { return parseUserPath(id, "/state/com.google/") }

// resolveStream turns a stream id into a filter (design §6.4). Unknown streams,
// unknown labels and unknown feeds are an empty result, never an error.
func (c *call) resolveStream(id, raw string) (store.StreamFilter, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return store.StreamFilter{}, nil
	}
	if name, ok := stateName(id); ok {
		switch name {
		case "reading-list":
			return store.StreamFilter{}, nil
		case "starred":
			return store.StreamFilter{Starred: []int{1}}, nil
		case "read":
			return store.StreamFilter{Read: []int{1}}, nil
		case "unread", "kept-unread":
			return store.StreamFilter{Read: []int{0}}, nil
		case "broadcast", "like":
			return store.StreamFilter{Empty: true}, nil
		}
		c.warnStream(id)
		return store.StreamFilter{Empty: true}, nil
	}
	if strings.HasPrefix(id, "user/") {
		if _, ok := labelName(id); ok {
			fid, found, err := c.a.db.FindLabel(c.r.Context(), labelCandidates(id, raw))
			if err != nil {
				return store.StreamFilter{}, err
			}
			if !found {
				return store.StreamFilter{Empty: true}, nil
			}
			return store.StreamFilter{FolderID: fid}, nil
		}
	}
	if rest, ok := strings.CutPrefix(id, "feed/"); ok && rest != "" {
		if n, err := strconv.ParseInt(rest, 10, 64); err == nil && n > 0 {
			return store.StreamFilter{FeedID: n}, nil
		}
		// A feed URL in a request path had its "//" collapsed by the front handler.
		rest = restoreScheme(rest)
		fid, found, err := c.a.db.FindFeedID(c.r.Context(), rest)
		if err != nil {
			return store.StreamFilter{}, err
		}
		if !found {
			return store.StreamFilter{Empty: true}, nil
		}
		return store.StreamFilter{FeedID: fid}, nil
	}
	c.warnStream(id)
	return store.StreamFilter{Empty: true}, nil
}

func (c *call) warnStream(id string) {
	c.a.log.Warn("greader: unknown stream", "stream", id, "path", c.path, "ua", c.r.UserAgent())
}

// restoreScheme undoes the slash collapse in "http:/host/x".
func restoreScheme(s string) string {
	for _, sch := range []string{"http:/", "https:/"} {
		if rest, ok := strings.CutPrefix(s, sch); ok && !strings.HasPrefix(rest, "/") {
			return sch + "/" + rest
		}
	}
	return s
}

// applyStates ANDs the it (include only) and xt (exclude) state filters.
func applyStates(f store.StreamFilter, it, xt []string) store.StreamFilter {
	for _, v := range it {
		switch n, _ := stateName(v); n {
		case "read":
			f.Read = append(f.Read, 1)
		case "unread", "kept-unread":
			f.Read = append(f.Read, 0)
		case "starred":
			f.Starred = append(f.Starred, 1)
		}
	}
	for _, v := range xt {
		switch n, _ := stateName(v); n {
		case "read":
			f.Read = append(f.Read, 0)
		case "unread", "kept-unread":
			f.Read = append(f.Read, 1)
		case "starred":
			f.Starred = append(f.Starred, 0)
		}
	}
	return f
}

const (
	defaultN     = 20
	maxIDsN      = 100000
	maxContentsN = 1000
)

// pageParams parses n, r, c, ot and nt (design §3, §6.5). c must be all digits
// or it is ignored; n is clamped to [1, maxN], defaulting to 20.
func (c *call) pageParams(maxN int) store.IDPage {
	p := store.IDPage{N: defaultN}
	if n, err := strconv.Atoi(c.p.Get("n")); err == nil && n > 0 {
		p.N = n
	}
	if p.N > maxN {
		p.N = maxN
	}
	p.Asc = c.p.Get("r") == "o"
	if s := c.p.Get("c"); allDigits(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			p.Cont, p.HasCont = n, true
		}
	}
	// Seconds beyond year 2100 are clamped so the microsecond arithmetic cannot overflow.
	const maxSeconds = 4102444800
	if s := c.p.Get("ot"); allDigits(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil || len(s) > 10 {
			p.OT, p.HasOT = min(n, maxSeconds), true
			if err != nil {
				p.OT = maxSeconds
			}
		}
	}
	if s := c.p.Get("nt"); allDigits(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil || len(s) > 10 {
			p.NT, p.HasNT = min(n, maxSeconds), true
			if err != nil {
				p.NT = maxSeconds
			}
		}
	}
	return p
}
