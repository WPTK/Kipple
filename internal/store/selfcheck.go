package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// feature is one SQLite capability the schema and queries depend on.
type feature struct {
	name  string
	steps []string // run in order on one connection; the first failure names the feature
}

// requiredFeatures are probed at startup (design §13 item 2). A driver swap or
// a build without a module would otherwise fail on the first search or the
// first bulk mark, long after a deploy looked healthy. Everything runs in the
// temp schema, so the database file is never touched.
var requiredFeatures = []feature{
	{"json1 (json_valid, json_each)", []string{
		`SELECT json_valid('1') + (SELECT count(*) FROM json_each('[1,2]'))`,
	}},
	{"fts5 (unicode61, snippet, bm25)", []string{
		`CREATE VIRTUAL TABLE temp.kipple_selfcheck USING fts5(a, tokenize='unicode61 remove_diacritics 2')`,
		`INSERT INTO temp.kipple_selfcheck(rowid, a) VALUES (1, 'probe')`,
		`SELECT snippet(kipple_selfcheck, 0, '', '', '', 1), bm25(kipple_selfcheck), rank FROM temp.kipple_selfcheck WHERE kipple_selfcheck MATCH 'probe'`,
		`DROP TABLE temp.kipple_selfcheck`,
	}},
}

// selfCheck probes every required feature on one connection of db and returns
// an error naming all the missing ones.
func selfCheck(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: self-check: %w", err)
	}
	defer conn.Close()
	var missing []string
	for _, f := range requiredFeatures {
		if err := probe(ctx, conn, f); err != nil {
			missing = append(missing, fmt.Sprintf("%s: %v", f.name, err))
		}
	}
	if len(missing) > 0 {
		return errors.New("store: the SQLite driver lacks required features: " + strings.Join(missing, "; "))
	}
	return nil
}

func probe(ctx context.Context, conn *sql.Conn, f feature) error {
	for _, q := range f.steps {
		if strings.HasPrefix(q, "SELECT") {
			rows, err := conn.QueryContext(ctx, q)
			if err != nil {
				return err
			}
			for rows.Next() {
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			continue
		}
		if _, err := conn.ExecContext(ctx, q); err != nil {
			// Leave nothing behind on a half-run probe.
			_, _ = conn.ExecContext(ctx, `DROP TABLE IF EXISTS temp.kipple_selfcheck`)
			return err
		}
	}
	return nil
}
