package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubDriver answers every statement with "no such module / function", like a
// SQLite build compiled without json1 and fts5.
type stubDriver struct{}
type stubConn struct{}

func (stubDriver) Open(string) (driver.Conn, error) { return stubConn{}, nil }
func (stubConn) Prepare(q string) (driver.Stmt, error) {
	if strings.Contains(q, "fts5") || strings.Contains(q, "json") || strings.Contains(q, "kipple_selfcheck") {
		return nil, errors.New("no such module or function")
	}
	return nil, errors.New("unsupported")
}
func (stubConn) Close() error              { return nil }
func (stubConn) Begin() (driver.Tx, error) { return nil, errors.New("unsupported") }

func init() { sql.Register("kipple-stub-nofeatures", stubDriver{}) }

func TestSelfCheckPassesOnTheRealDriver(t *testing.T) {
	db, err := sql.Open("sqlite", buildDSN(filepath.Join(t.TempDir(), "x.db"), "writer"))
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, selfCheck(context.Background(), db))
	// Nothing lingers on the connection afterwards.
	require.NoError(t, selfCheck(context.Background(), db))
}

func TestSelfCheckNamesEveryMissingFeature(t *testing.T) {
	db, err := sql.Open("kipple-stub-nofeatures", "")
	require.NoError(t, err)
	defer db.Close()
	err = selfCheck(context.Background(), db)
	require.Error(t, err)
	require.Contains(t, err.Error(), "json1")
	require.Contains(t, err.Error(), "fts5")
	require.Contains(t, err.Error(), "lacks required features")
}
