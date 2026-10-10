package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// An integer setting that is not an exact in-range integer reads as the
// default, never as a truncated or overflowed conversion.
func TestSettingIntRejectsNonIntegralAndOutOfRange(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"42", 42},
		{"-3", -3},
		{"1e2", 100},
		{"7.0", 7},
		{"1.5", 99},
		{"-0.25", 99},
		{"1e300", 99},
		{"-1e300", 99},
		{"9223372036854775808", 99}, // 2^63
		{`"12"`, 99},
	} {
		e.exec(`INSERT INTO settings(key, value) VALUES ('test.int', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, tc.raw)
		require.Equal(t, tc.want, settingInt(e.ctx, e.db.Reader(), "test.int", 99), tc.raw)
		n, err := settingIntErr(e.ctx, e.db.Reader(), "test.int", 99)
		require.NoError(t, err)
		require.Equal(t, tc.want, n, tc.raw)
	}
}
