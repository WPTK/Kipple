package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeSQL(t *testing.T) {
	// Comments and spacing do not matter; everything else does.
	require.Equal(t,
		normalizeSQL("CREATE TABLE t ( a TEXT UNIQUE, b INTEGER DEFAULT 0 )"),
		normalizeSQL("CREATE TABLE t ( -- the table\n  a TEXT   UNIQUE, /* two\nlines */\n\tb INTEGER DEFAULT 0\n)"))
	require.NotEqual(t, normalizeSQL("CREATE TABLE t (a TEXT UNIQUE)"), normalizeSQL("CREATE TABLE t (a TEXT)"))
	require.NotEqual(t, normalizeSQL("CREATE TABLE t (a CHECK (a > 0))"), normalizeSQL("CREATE TABLE t (a CHECK (0))"))
	// Quoted text is kept as written, including a comment marker and spaces.
	require.Equal(t, "a DEFAULT '-- x  y'", normalizeSQL("a   DEFAULT '-- x  y' -- note"))
	require.Equal(t, `"a  b" 'it''s'`, normalizeSQL(`"a  b"   'it''s'`))
}
