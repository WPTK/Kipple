package greader

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestRepairIsLinearAndCapped(t *testing.T) {
	tail := strings.Repeat("x", 200)
	body := "s=user/-/label/A" + strings.Repeat("&"+tail, 19000) // ~3.8 MB
	start := time.Now()
	p := repairParams(body)
	require.Less(t, time.Since(start), 500*time.Millisecond)
	require.False(t, p.tooMany)
	require.LessOrEqual(t, len(p.All("s")[0]), len("user/-/label/A")+(maxGlueParts+1)*201)
	// A 1 MB query is never repaired and parses at phase 1 cost.
	qs := strings.Repeat("a&", 19000) + strings.Repeat("b", 1<<20)
	r := httptest.NewRequest(http.MethodGet, "/x?"+qs, nil)
	start = time.Now()
	readParamsLimit(r, false, maxBody, true)
	require.Less(t, time.Since(start), 500*time.Millisecond)
	// Many separate runs in one body stay linear.
	body = strings.Repeat("s=user/-/label/A&"+tail+"&", 9000)
	start = time.Now()
	repairParams(body)
	require.Less(t, time.Since(start), 500*time.Millisecond)
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
				if _, ok := labelName(e.val); ok {
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
