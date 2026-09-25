package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestoreDaysClamped(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"365", MaxRestoreDays},
		{"181", MaxRestoreDays},
		{"180", 180},
		{"90", 90},
		{"0", 0},
		{"-5", 0},
	} {
		e.exec(`INSERT INTO settings(key, value) VALUES ('retention.restore_days', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, tc.raw)
		got := LoadFetchSettings(e.ctx, e.db.writer).RestoreDays
		require.Equal(t, tc.want, got, tc.raw)
	}
}
