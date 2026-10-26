package greader

import (
	"net/http"
	"strconv"
	"testing"
)

// BenchmarkStreamItemIDs measures stream/items/ids through the full handler on a
// 200 item library, with and without ot.
func BenchmarkStreamItemIDs(b *testing.B) {
	h := newHarness(b)
	feed := h.addFeed("https://bench.example/feed", "Bench", "")
	for k := range 200 {
		h.addItem(feed, itemSeed{ID: baseID - int64(k+1)*confMin, Title: "bench " + strconv.Itoa(k)})
	}
	rl := q1("s", stateReadingList)
	ot := strconv.FormatInt(baseID/1_000_000-3600, 10)
	for name, query := range map[string]string{"no ot": "", "ot": "&ot=" + ot} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if w := h.get(rd + "stream/items/ids?n=20&" + rl + query); w.Code != http.StatusOK {
					b.Fatal(w.Code, w.Body.String())
				}
			}
		})
	}
}
