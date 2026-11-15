package fetch

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// The kept slice is sized once for the document, and an empty document still gives nil.
func TestAssignUIDsSizesKeptOnce(t *testing.T) {
	items := make([]Item, 30)
	for i := range items {
		items[i] = Item{GUID: fmt.Sprintf("g%d", i), Title: "t", RawLink: fmt.Sprintf("https://example.com/%d", i)}
	}
	kept, _ := AssignUIDs(items, DedupAuto)
	require.Len(t, kept, 30)
	require.Equal(t, 30, cap(kept))
	kept, _ = AssignUIDs(nil, DedupAuto)
	require.Nil(t, kept)
}
