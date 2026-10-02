package greader

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func repairParams(body string) *Params {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return readParamsLimit(r, false, maxBody, true)
}

func TestMarkAllAsReadLabelWithTS(t *testing.T) {
	h := newHarness(t)
	fa := h.addFeed("https://a.example/a", "A", "Comics")
	fb := h.addFeed("https://a.example/b", "B", "Other")
	a := h.addItem(fa, itemSeed{})
	b := h.addItem(fb, itemSeed{})
	ts := strconv.FormatInt(time.Now().Add(time.Hour).UnixMicro(), 10)
	// Reeder: T, s, ts.
	h.post(rd+"mark-all-as-read", "T=x&s=user/-/label/Comics&ts="+ts)
	require.True(t, isRead(h, a), "folder marked read")
	require.False(t, isRead(h, b))
	h.post(rd+"mark-all-as-read", "T=x&ts="+ts+"&s="+url.QueryEscape("user/-/label/Other"))
	require.True(t, isRead(h, b))
}

func TestLabelStreamWithClientExtraKeys(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "Tech")
	it := h.addItem(f, itemSeed{})
	for _, extra := range []string{"includeAllDirectStreamIds=false", "merge=true", "likes=", "comments=", "types=1", "_=123"} {
		w := h.get(rd + "stream/items/ids?output=json&n=10&s=user/-/label/Tech&" + extra)
		require.Contains(t, w.Body.String(), strconv.FormatInt(it, 10), extra)
	}
	f2 := h.addFeed("https://a.example/g", "G", "My Folder")
	it2 := h.addItem(f2, itemSeed{})
	w := h.get(rd + "stream/items/ids?output=json&s=user%2F-%2Flabel%2FMy+Folder&includeAllDirectStreamIds=false")
	require.Contains(t, w.Body.String(), strconv.FormatInt(it2, 10), "'+' stays a space when properly encoded")
	f3 := h.addFeed("https://a.example/h", "H", "News & Co")
	it3 := h.addItem(f3, itemSeed{})
	w = h.get(rd + "stream/items/ids?output=json&s=" + url.QueryEscape("user/-/label/News & Co"))
	require.Contains(t, w.Body.String(), strconv.FormatInt(it3, 10))
}

func TestRawFolderNamesRecoveredInBody(t *testing.T) {
	for _, name := range []string{"News & Politics+", "R&D", "A+B", "Tom & Jerry", "Café & Thé", "日本&ニュース", "R&", "A&&B", "AT&T"} {
		id := "user/-/label/" + name
		for _, key := range []string{"s", "a", "r", "dest"} {
			for _, body := range []string{"T=tok&" + key + "=" + id, key + "=" + id + "&T=tok", "T=tok&" + key + "=" + id + "&i=1"} {
				p := repairParams(body)
				if strings.HasSuffix(name, "A+B") {
					continue // form-decoded when no '&' is present, covered below
				}
				require.Equal(t, []string{id}, p.All(key), body)
				require.Equal(t, "tok", p.Get("T"), body)
			}
		}
	}
	require.Equal(t, "user/-/label/A B", repairParams("s=user/-/label/A+B&T=x").Get("s"), "no '&': form-decoded as before")
}

func TestDisableTagAmpersandLookAlike(t *testing.T) {
	h := newHarness(t)
	fa := h.addFeed("https://a.example/a", "A", "AT&T")
	fb := h.addFeed("https://a.example/b", "B", "AT")
	h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/AT&T")
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = 'AT&T'"))
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM folders WHERE name = 'AT'"))
	require.Equal(t, []string{"Uncategorized"}, labelsOf(findSub(subsOf(t, h), feedID(fa))))
	require.Equal(t, []string{"AT"}, labelsOf(findSub(subsOf(t, h), feedID(fb))))
}

func TestDisableTagTrailingAndDoubleAmpersand(t *testing.T) {
	for _, name := range []string{"R&", "A&&B"} {
		h := newHarness(t)
		h.addFolder("R")
		h.addFolder("A")
		h.addFeed("https://a.example/a", "A", name)
		h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/"+name)
		require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = ?", name), name)
		require.Equal(t, 2, q[int](h, "SELECT count(*) FROM folders WHERE name IN ('R','A')"), name)
	}
}

// allocated reports the least number of bytes f allocates over a few runs. Bytes
// allocated follow the work the parser does (every copy, join and split is
// counted) but not the speed of the machine, so a bound on them holds under
// -race and on a loaded runner where a wall-clock bound does not.
func allocated(f func()) uint64 {
	best := ^uint64(0)
	for i := 0; i < 3; i++ {
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		f()
		runtime.ReadMemStats(&b)
		if d := b.TotalAlloc - a.TotalAlloc; d < best {
			best = d
		}
	}
	return best
}

// steps reports the bytes the splitter scanned, copied and joined during f, as
// counted by the workHook seam in form.go. Unlike a stopwatch it does not depend
// on the machine, so it is exact under -race and on a loaded runner, and it sees
// CPU-only work that allocates nothing (a rescan per part, say).
func steps(f func()) uint64 {
	var n uint64
	workHook = func(b int) { n += uint64(b) }
	defer func() { workHook = nil }()
	f()
	return n
}

// requireLinear proves a parse is linear in its input by comparing the work at n
// and 2n units, on two measures. Counted steps (exact) and bytes allocated both
// double for linear work and quadruple for quadratic work, so the bound allows
// 2.6x. Each is also capped per input byte, so a constant-factor blowup fails
// too. Allocation covers copies the step counter does not see; steps cover
// CPU-only work that allocates nothing. The wall-clock check is only a backstop
// for work that is neither counted nor allocated: it is far above any honest run
// (a few tens of ms, about a second under -race) and catches catastrophic
// regressions only.
func requireLinear(t *testing.T, name string, perByte uint64, n int, build func(n int) (input int, run func())) {
	t.Helper()
	in1, run1 := build(n)
	in2, run2 := build(2 * n)
	s1, s2 := steps(run1), steps(run2)
	w1, w2 := allocated(run1), allocated(run2)
	t.Logf("%s: %d bytes of input: %d steps, %d allocated; %d bytes: %d steps, %d allocated", name, in1, s1, w1, in2, s2, w2)
	require.LessOrEqual(t, s1, 8*uint64(in1), "%s: steps per input byte", name)
	require.LessOrEqual(t, s2, 8*uint64(in2), "%s: steps per input byte", name)
	require.LessOrEqual(t, float64(s2), 2.6*float64(s1), "%s: doubling the input must not more than double the steps", name)
	require.LessOrEqual(t, w1, perByte*uint64(in1), "%s: allocation per input byte", name)
	require.LessOrEqual(t, w2, perByte*uint64(in2), "%s: allocation per input byte", name)
	require.LessOrEqual(t, float64(w2), 2.6*float64(w1), "%s: doubling the input must not more than double the allocation", name)
	start := time.Now()
	run2()
	require.Less(t, time.Since(start), time.Second, "%s: backstop against uncounted CPU-only blowups (catastrophic regressions only)", name)
}

func TestRepairIsLinearAndCapped(t *testing.T) {
	tail := strings.Repeat("x", 200)
	// One long run of tail parts after a label: the glue is capped, the rest is linear.
	requireLinear(t, "one long run", 16, 9500, func(n int) (int, func()) {
		body := "s=user/-/label/A" + strings.Repeat("&"+tail, n) // ~3.8 MB at 19000
		return len(body), func() {
			p := repairParams(body)
			require.False(t, p.tooMany)
			require.LessOrEqual(t, len(p.All("s")[0]), len("user/-/label/A")+(maxGlueParts+1)*201)
		}
	})
	// A 1 MB query is never repaired and parses at phase 1 cost.
	requireLinear(t, "plain query", 16, 9000, func(n int) (int, func()) {
		qs := strings.Repeat("a&", n) + strings.Repeat("b", 58*n) // 1 MB at 18000
		return len(qs), func() {
			r := httptest.NewRequest(http.MethodGet, "/x?"+qs, nil)
			readParamsLimit(r, false, maxBody, true)
		}
	})
	// Many separate runs in one body stay linear.
	requireLinear(t, "many runs", 16, 4500, func(n int) (int, func()) {
		body := strings.Repeat("s=user/-/label/A&"+tail+"&", n)
		return len(body), func() { repairParams(body) }
	})
}

var genKeys = []string{"T", "s", "a", "r", "t", "ac", "dest", "i", "n", "xt", "output", "ts", "includeAllDirectStreamIds", "_", "merge", "x1"}

func genValue(rng *rand.Rand) string {
	pool := []string{"user/-/label/News", "user/-/label/My%20Folder", "user/-/label/A+B", "feed/https://a.example/f?x=1;y=2", "1", "%2B", "a+b", "", "%zz", "日本", "user/-/state/com.google/read", "x=y", "a,b;c$d"}
	return pool[rng.Intn(len(pool))]
}

// genIdentBody builds a body of only identifier-style key=value pairs.
func genIdentBody(rng *rand.Rand) string {
	var parts []string
	for n := rng.Intn(12); n >= 0; n-- {
		parts = append(parts, genKeys[rng.Intn(len(genKeys))]+"="+genValue(rng))
	}
	return strings.Join(parts, "&")
}

func TestRepairMatchesPhase1OnIdentifierPairs(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		s := genIdentBody(rng)
		want, wok := refSplitPairs(s)
		for _, repair := range []bool{false, true} {
			got, gok := splitPairsLimit(s, repair)
			require.Equal(t, wok, gok, s)
			require.Equal(t, want, got, s)
		}
	}
}

func FuzzSplitPairs(f *testing.F) {
	for _, s := range []string{
		"", "T=x&s=user/-/label/Comics&ts=1", "T=x&s=user/-/label/AT&T", "s=user/-/label/R&", "s=user/-/label/A&&B",
		"T=x&s=user/-/label/News & Politics+", "s=user%2F-%2Flabel%2FMy+Folder&_=1", "a&b&&=c&%zz=%zz", "s=user/-/label/A&a b=c&i=1",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want, wok := refSplitPairs(s)
		got, gok := splitPairsLimit(s, false)
		require.Equal(t, wok, gok)
		require.Equal(t, want, got)
		rep, rok := splitPairsLimit(s, true)
		require.Equal(t, wok, rok)
		hasLabel := false
		for _, e := range want {
			if labelKeys[e.key] {
				if hasLabelPrefix(e.val) {
					hasLabel = true
				}
			}
		}
		for _, e := range rep {
			require.LessOrEqual(t, len(e.rawVal), len(s))
		}
		if !hasLabel {
			require.Equal(t, want, rep)
		}
	})
}

func TestRepairKeepsParamsAndGluesNames(t *testing.T) {
	cases := []struct {
		body   string
		s      string
		others map[string]string // params that must survive as their own pairs
	}{
		{"s=user/-/label/A&a b=c&i=1", "user/-/label/A&a b=c", map[string]string{"i": "1"}},
		{"s=user/-/label/A&x=1", "user/-/label/A", map[string]string{"x": "1"}},
		{"s=user/-/label/A&x-client=1", "user/-/label/A", map[string]string{"x-client": "1"}},
		{"s=user/-/label/A&client.id=1", "user/-/label/A", map[string]string{"client.id": "1"}},
		{"s=user/-/label/A&%54=tok", "user/-/label/A", map[string]string{"T": "tok"}},
		{"s=user/-/label/A&%61=user/-/label/Y", "user/-/label/A", map[string]string{"a": "user/-/label/Y"}},
		{"s=user/-/label/R&", "user/-/label/R&", nil},
		{"s=user/-/label/A&&B", "user/-/label/A&&B", nil},
		{"s=user/-/label/&Co", "user/-/label/&Co", nil},
		{"s=user/-/label/ &Co&T=tok", "user/-/label/ &Co", map[string]string{"T": "tok"}},
		{"s=user/-/label/My%20News&100%&T=tok", "user/-/label/My News&100%", map[string]string{"T": "tok"}},
	}
	for _, c := range cases {
		p := repairParams(c.body)
		require.Equal(t, c.s, p.Get("s"), c.body)
		for k, v := range c.others {
			require.Equal(t, v, p.Get(k), c.body+" "+k)
		}
	}
}

func TestLenientUnescape(t *testing.T) {
	require.Equal(t, "a b&100%", lenientUnescape("a%20b&100%"))
	require.Equal(t, "%zz%4", lenientUnescape("%zz%4"))
	require.Equal(t, "+é", lenientUnescape("+%C3%A9"))
}

// subscription/edit, rename-tag and every non-disable-tag endpoint parse with
// the exact phase 1 parser: the repair is off for them.
func TestNoRepairOutsideDisableTag(t *testing.T) {
	bodies := []string{
		"a=user/-/label/News&", "a=user/-/label/News&&T=tok", "s=user/-/label/A&x-client=1", "s=user/-/label/A&%54=tok",
		"s=user/-/label/&Co", "s=user/-/label/My%20News&100%", "T=tok&s=user/-/label/AT&T", "T=tok&a=user/-/label/News & Politics+",
		"s=user/-/label/A&a b=c&i=1", "s=user/-/label/A&client.id=1&dest=user/-/label/B&",
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 5000; i++ {
		parts := []string{"s=user/-/label/A", "&", "T", "x-y=1", "%54=z", " b=c", "&", "a=user/-/label/", "R", "", "dest=user/-/label/Q&"}
		var sb strings.Builder
		for n := rng.Intn(8); n >= 0; n-- {
			sb.WriteString(parts[rng.Intn(len(parts))] + "&")
		}
		bodies = append(bodies, sb.String())
	}
	for _, b := range bodies {
		want, wok := refSplitPairs(b)
		got, gok := splitPairsLimit(b, false)
		require.Equal(t, wok, gok, b)
		require.Equal(t, want, got, b)
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(b))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		require.Equal(t, want, readParamsLimit(r, false, maxBody, false).body, b)
	}
	require.False(t, (&route{}).repair)
}

func TestSubscriptionEditAndRenameTagAreNotRepaired(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/a", "A", "Tech")
	h.addFolder("News")
	// Phase 1: name "News" (the stray '&' is its own empty pair); the feed goes to the
	// existing folder, no garbage folder is created.
	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+feedID(f)+"&a=user/-/label/News&")
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name LIKE '%&%'"))
	require.Equal(t, []string{"News"}, labelsOf(findSub(subsOf(t, h), feedID(f))))
	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+feedID(f)+"&a=user/-/label/News&&T="+h.tok)
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name LIKE '%&%'"))
	h.post(rd+"rename-tag", "T="+h.tok+"&s=user/-/label/News&&dest=user/-/label/Fresh&")
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM folders WHERE name = 'Fresh'"))
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name LIKE '%&%'"))
}

func TestDisableTagStrayAmpersandAndVendorKeys(t *testing.T) {
	h := newHarness(t)
	h.addFeed("https://a.example/a", "A", "News")
	h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/News&")
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = 'News'"))
	h.addFeed("https://a.example/b", "B", "Sports")
	h.post(rd+"disable-tag", "s=user/-/label/Sports&x-client=1&client.id=2&T="+h.tok)
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = 'Sports'"))
	h.addFeed("https://a.example/c", "C", "R&Co")
	h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/R&Co")
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = 'R&Co'"))
	// A missing "AT&T" never deletes the folder "AT".
	h.addFeed("https://a.example/d", "D", "AT")
	h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/AT&T")
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM folders WHERE name = 'AT'"))
	// Mixed escapes in a merged run still find the folder.
	h.addFeed("https://a.example/e", "E", "My News&100%")
	h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/My%20News&100%")
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = 'My News&100%'"))
}
