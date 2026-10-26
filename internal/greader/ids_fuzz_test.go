package greader

import (
	"math/big"
	"strings"
	"testing"
)

// FuzzGreaderIDs: the Reader API's id and path parsers never panic and either round-trip or refuse.
// ParseItemID gives back the id every emitted form names (long, decimal, 0x hex); parseUserPath finds exactly
// the name after user/<one segment><suffix>; classify agrees with a plain restatement of the mount rules; and
// normalizeTS accepts exactly the digit strings that fit in 63 bits and are not zero microseconds.
func FuzzGreaderIDs(f *testing.F) {
	for _, s := range []struct {
		raw, seg, name string
		id             int64
	}{
		{"tag:google.com,2005:reader/item/00000000000003e8", "-", "News", 1000},
		{"1000", "-", "x/state/com.google/read", -1},
		{"0xdeadbeef", "1234", " ", 1 << 62},
		{"-9223372036854775808", "a/b", "", -1 << 63},
		{"ffffffffffffffff", "", "Ünï", 0},
		{"/api/greader.php//api/greader.php/reader/api/0/token", "-", "a", 7},
		{"//reader/api/0/stream/contents", "-", "a", 7},
		{"/accounts/ClientLogin", "-", "a", 7},
		{"/api/greader.php/icon/x", "-", "a", 7},
		{"1700000000", "-", "a", 7},
		{"1700000000000", "-", "a", 7},
		{"1700000000000000", "-", "a", 7},
		{"1700000000000000000", "-", "a", 7},
		{"99999999999999999999", "-", "a", 7},
		{"0", "-", "a", 7},
		{"00012", "-", "a", 7},
	} {
		f.Add(s.raw, s.seg, s.name, s.id)
	}
	f.Fuzz(func(t *testing.T, raw, seg, name string, id int64) {
		// Item ids: every emitted form parses back to the id.
		for _, form := range []string{FormatLongID(id), FormatDecimal(id), "0x" + FormatHex16(id)} {
			if got, ok := ParseItemID(form); !ok || got != id {
				t.Fatalf("ParseItemID(%q) = %d, %v, want %d", form, got, ok, id)
			}
		}
		if got, ok := ParseItemID(raw); ok {
			if again, ok2 := ParseItemID(FormatLongID(got)); !ok2 || again != got {
				t.Fatalf("ParseItemID(%q) = %d does not round-trip through the long form", raw, got)
			}
		}

		// User paths: a built path yields the name; a raw one that yields a name is built that way.
		for _, suffix := range []string{"/label/", "/state/com.google/"} {
			if !strings.Contains(seg, "/") && strings.TrimSpace(name) != "" {
				if got, ok := parseUserPath("user/"+seg+suffix+name, suffix); !ok || got != name {
					t.Fatalf("parseUserPath(user/%s%s%s) = %q, %v", seg, suffix, name, got, ok)
				}
			}
			if got, ok := parseUserPath(raw, suffix); ok {
				rest, _ := strings.CutPrefix(raw, "user/")
				if strings.TrimSpace(got) == "" || !strings.HasSuffix(raw, got) ||
					rest[strings.IndexByte(rest, '/'):] != suffix+got {
					t.Fatalf("parseUserPath(%q, %q) = %q is not the name after one segment", raw, suffix, got)
				}
			}
		}

		// classify: the Reader mount rules, restated.
		p := raw
		for strings.Contains(p, "//") {
			p = strings.ReplaceAll(p, "//", "/")
		}
		prefixed := false
		for p == apiPrefix || strings.HasPrefix(p, apiPrefix+"/") {
			p = p[len(apiPrefix):]
			prefixed = true
		}
		wantOK := false
		switch {
		case p == "" || p == "/" || p == "/check/compatibility" || strings.HasPrefix(p, "/icon/"):
			wantOK = prefixed
		case p == "/accounts/ClientLogin" || strings.HasPrefix(p, "/reader/api/0/"):
			wantOK = true
		}
		rest, ok := classify(raw)
		if ok != wantOK || (ok && rest != p) || (!ok && rest != "" && rest != p) {
			t.Fatalf("classify(%q) = %q, %v; want %q, %v", raw, rest, ok, p, wantOK)
		}
		if ok && (strings.Contains(rest, "//") || (rest != "" && rest[0] != '/')) {
			t.Fatalf("classify(%q) left a malformed rest %q", raw, rest)
		}
		if !ok && wantOK {
			t.Fatalf("classify(%q) refused a path the rules accept", raw)
		}

		// normalizeTS: accepted exactly when digits only, non-zero and within int64; the unit follows the digit count.
		us, ok := normalizeTS(raw)
		n, isNum := new(big.Int).SetString(raw, 10)
		digits := raw != "" && strings.Trim(raw, "0123456789") == ""
		wantTS := digits && isNum && n.IsInt64() && n.Sign() != 0 && (len(raw) < 17 || n.Int64() >= 1000) // nanoseconds under 1000 round to zero
		if ok != wantTS {
			t.Fatalf("normalizeTS(%q) ok = %v, want %v", raw, ok, wantTS)
		}
		if ok {
			if us <= 0 {
				t.Fatalf("normalizeTS(%q) = %d, not positive", raw, us)
			}
			switch l := len(raw); {
			case l <= 12 && us%1_000_000 != 0, l >= 13 && l <= 15 && us%1000 != 0:
				t.Fatalf("normalizeTS(%q) = %d, not a whole number of the input's unit", raw, us)
			}
		}
	})
}
