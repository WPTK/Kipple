package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SchemaObject is one row of sqlite_master: a table, index, trigger or view.
type SchemaObject struct {
	Type  string
	Name  string
	Table string // tbl_name: the table an index or trigger belongs to
	SQL   string // "" for the indexes SQLite makes itself
}

// SchemaAt builds a fresh database at schema version (1 to LatestVersion) in
// memory and returns its schema objects: what a Kipple database at that version
// holds and nothing else. A restore compares an uploaded database against it.
func SchemaAt(ctx context.Context, version int) ([]SchemaObject, error) {
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	if version < 1 || version > len(ms) {
		return nil, fmt.Errorf("store: no schema %d", version)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // one connection: one in-memory database
	for _, m := range ms[:version] {
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			return nil, fmt.Errorf("store: build schema %d: %s: %w", version, m.name, err)
		}
	}
	return ReadSchema(ctx, db)
}

// ReadSchema lists the schema objects of the main database of q.
func ReadSchema(ctx context.Context, q Querier) ([]SchemaObject, error) {
	rows, err := q.QueryContext(ctx, "SELECT type, name, tbl_name, ifnull(sql, '') FROM main.sqlite_master ORDER BY type, name")
	if err != nil {
		return nil, fmt.Errorf("store: read schema: %w", err)
	}
	defer rows.Close()
	var out []SchemaObject
	for rows.Next() {
		var o SchemaObject
		if err := rows.Scan(&o.Type, &o.Name, &o.Table, &o.SQL); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
