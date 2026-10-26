package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 0021 folds the Inbox thumbnails and the Digest's Images into one list-pictures key, in device profiles
// and in the defaults for new devices: hidden in either becomes list_images false, shown in both leaves
// the default, and every other key stays.
func TestMigration0021ListImages(t *testing.T) {
	t.Parallel()
	const (
		inboxOff   = "0123456789abcdef"
		digestOff  = "fedcba9876543210"
		bothShown  = "aaaaaaaaaaaaaaaa"
		bothHidden = "bbbbbbbbbbbbbbbb"
		plain      = "cccccccccccccccc"
	)
	db := reopen(t, schema19WithDevices(t,
		map[string]string{
			"ui.device_defaults": `{"client.inbox_thumbs":"off","client.order":"oldest"}`,
			"tz":                 `"UTC"`,
		},
		map[string]string{
			inboxOff:   `{"ui.theme":"fountain","client.inbox_thumbs":"off","client.digest_images":true}`,
			digestOff:  `{"client.digest_images":false,"client.layout":"digest"}`,
			bothShown:  `{"client.inbox_thumbs":"auto","client.digest_images":true,"client.order":"oldest"}`,
			bothHidden: `{"client.inbox_thumbs":"off","client.digest_images":false}`,
			plain:      `{"ui.theme":"paper"}`,
		}))
	get := func(id string) string {
		return scalar[string](t, db.Reader(), "SELECT settings FROM devices WHERE id = ?", id)
	}
	require.JSONEq(t, `{"ui.theme":"fountain","client.list_images":false}`, get(inboxOff))
	require.JSONEq(t, `{"client.layout":"digest","client.list_images":false}`, get(digestOff))
	require.JSONEq(t, `{"client.order":"oldest"}`, get(bothShown))
	require.JSONEq(t, `{"client.list_images":false}`, get(bothHidden))
	require.JSONEq(t, `{"ui.theme":"paper"}`, get(plain))
	require.JSONEq(t, `{"client.order":"oldest","client.list_images":false}`,
		scalar[string](t, db.Reader(), "SELECT value FROM settings WHERE key = 'ui.device_defaults'"))
	require.Equal(t, `"UTC"`, scalar[string](t, db.Reader(), "SELECT value FROM settings WHERE key = 'tz'"))
	requireCleanIntegrity(t, db.Reader())
}
