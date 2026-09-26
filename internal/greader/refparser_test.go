package greader

import (
	"net/url"
	"strings"
)

// refSplitPairs is the phase 1 parser (commit 3ae3678^, internal/greader/form.go
// splitPairsLimit), vendored verbatim as the reference the repairing parser must
// match on every input the repair does not concern.
func refSplitPairs(s string) (out []pair, ok bool) {
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
		out = append(out, pair{key: refUnescape(k), val: refUnescape(v), rawKey: k, rawVal: v})
	}
	return out, true
}

func refUnescape(s string) string {
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}
