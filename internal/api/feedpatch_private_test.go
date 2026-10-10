package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A private address typed into the feed editor is refused with a message that says where the switch is.
func TestPatchFeedPrivateAddressNamesTheSwitch(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://a.example/feed")
	code, body, _ := h.api(c, "PATCH", "/api/feeds/"+sid(id), `{"url":"http://10.20.30.5/f"}`)
	require.Equal(t, 400, code)
	require.Equal(t, "invalid_url", body["error"])
	require.Contains(t, body["message"], "Unsafe options")
}
