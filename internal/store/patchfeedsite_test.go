package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A URL edit to another site resets the network exceptions, as a permanent
// redirect migration does and as the held-redirect note promises; an edit within
// the site keeps them, and a patch that sets them in the same request wins.
func TestURLEditToAnotherSiteResetsNetworkExceptions(t *testing.T) {
	flags := func(e *env, id int64) (int, int) {
		return e.count("SELECT allow_private_net FROM feeds WHERE id = ?", id), e.count("SELECT allow_insecure_tls FROM feeds WHERE id = ?", id)
	}
	patch := func(e *env, id int64, u string, cols map[string]any) error {
		_, err := e.db.PatchFeed(e.ctx, id, FeedPatch{URL: &u, Cols: cols})
		return err
	}

	t.Run("another site", func(t *testing.T) {
		e := newEnv(t)
		id := e.addFeed("http://nas.example.test/rss")
		e.exec("UPDATE feeds SET allow_private_net = 1, allow_insecure_tls = 1 WHERE id = ?", id)
		require.NoError(t, patch(e, id, "https://other.example.org/feed", map[string]any{}))
		p, i := flags(e, id)
		require.Zero(t, p)
		require.Zero(t, i)
	})
	t.Run("same site", func(t *testing.T) {
		e := newEnv(t)
		id := e.addFeed("http://nas.example.test/rss")
		e.exec("UPDATE feeds SET allow_private_net = 1, allow_insecure_tls = 1 WHERE id = ?", id)
		require.NoError(t, patch(e, id, "https://www.example.test/rss", map[string]any{}))
		p, i := flags(e, id)
		require.Equal(t, 1, p)
		require.Equal(t, 1, i)
	})
	t.Run("set in the same patch", func(t *testing.T) {
		e := newEnv(t)
		id := e.addFeed("http://nas.example.test/rss")
		e.exec("UPDATE feeds SET allow_private_net = 1, allow_insecure_tls = 1 WHERE id = ?", id)
		require.NoError(t, patch(e, id, "https://other.example.org/feed", map[string]any{"allow_private_net": true, "allow_insecure_tls": true}))
		p, i := flags(e, id)
		require.Equal(t, 1, p)
		require.Equal(t, 1, i)
	})
	t.Run("a private literal on another site needs the exception set again", func(t *testing.T) {
		e := newEnv(t)
		id := e.addFeed("http://nas.example.test/rss")
		e.exec("UPDATE feeds SET allow_private_net = 1 WHERE id = ?", id)
		require.Error(t, patch(e, id, "http://10.0.0.9/rss", map[string]any{}))
		require.NoError(t, patch(e, id, "http://10.0.0.9/rss", map[string]any{"allow_private_net": true}))
	})
}
