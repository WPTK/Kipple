package store

import (
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

func TestOldestFirstOrder(t *testing.T) {
	at := func(h int) *time.Time { v := time.Unix(int64(h)*3600, 0); return &v }
	in := []fetch.Item{
		{UID: "a", Published: at(5)},
		{UID: "b"},
		{UID: "c", Published: at(5)},
		{UID: "d", Published: at(2)},
		{UID: "e"},
		{UID: "f", Published: at(9)},
	}
	var got []string
	for _, it := range oldestFirst(in) {
		got = append(got, it.UID)
	}
	// dated entries oldest first (ties in reverse document order), then undated ones in reverse document order
	// ahead of nothing dated... undated sort after every dated one.
	require.Equal(t, []string{"d", "c", "a", "f", "e", "b"}, got)
	require.Equal(t, "a", in[0].UID, "the input is not reordered")
}

// Sorting moves a permutation of indexes, not the items: the whole call allocates about one output copy.
func TestOldestFirstDoesNotCopyItemsForTheSort(t *testing.T) {
	in := make([]fetch.Item, 500)
	for i := range in {
		v := time.Unix(int64(1000-i), 0)
		in[i] = fetch.Item{UID: "u", Published: &v}
	}
	r := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			oldestFirst(in)
		}
	})
	outBytes := int64(len(in)) * int64(unsafeSizeofItem())
	require.Less(t, r.AllocedBytesPerOp(), outBytes*3/2, "oldestFirst allocated more than one output copy plus an index slice")
}

func unsafeSizeofItem() uintptr { return unsafe.Sizeof(fetch.Item{}) }
