package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SchemaObject is one row of sqlite_master: a table, index, trigger or view.
type SchemaObject struct {
	Type  string
	Name  string
	Table string // tbl_name: the table an index or trigger belongs to
	SQL   string // "" for the indexes SQLite makes itself
	Shape string // a table's or index's structure as SQLite reads it (see shape); "" for the rest
}

// SchemaAt builds a fresh database at schema version (1 to LatestVersion) in
// memory and returns its schema objects: what a Kipple database at that version
// holds and nothing else. A restore compares an uploaded database against it.
func SchemaAt(ctx context.Context, version int) ([]SchemaObject, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // one connection: one in-memory database
	if err := BuildSchema(ctx, db, version); err != nil {
		return nil, err
	}
	return ReadSchema(ctx, db)
}

// BuildSchema applies the migrations up to version to the empty database db
// (one connection) and sets its user_version, as a Kipple of that version
// would have left it.
func BuildSchema(ctx context.Context, db *sql.DB, version int) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	if version < 1 || version > len(ms) {
		return fmt.Errorf("store: no schema %d", version)
	}
	for _, m := range ms[:version] {
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("store: build schema %d: %s: %w", version, m.name, err)
		}
	}
	_, err = db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version))
	return err
}

// ReadSchema lists the schema objects of the main database of q, each table
// and index with its Shape.
func ReadSchema(ctx context.Context, q Querier) ([]SchemaObject, error) {
	rows, err := q.QueryContext(ctx, "SELECT type, name, tbl_name, ifnull(sql, '') FROM main.sqlite_master ORDER BY type, name")
	if err != nil {
		return nil, fmt.Errorf("store: read schema: %w", err)
	}
	var out []SchemaObject
	for rows.Next() {
		var o SchemaObject
		if err := rows.Scan(&o.Type, &o.Name, &o.Table, &o.SQL); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, o)
	}
	rows.Close() // before the next queries: SchemaAt's database has one connection
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Shape, err = shape(ctx, q, out[i]); err != nil {
			return nil, fmt.Errorf("store: read the %s %q: %w", out[i].Type, out[i].Name, err)
		}
	}
	return out, nil
}

// shape describes what a table or index is, from SQLite's own reading of it
// rather than its text: for a table its kind (ordinary, virtual or shadow),
// WITHOUT ROWID and STRICT, and each column's name, declared type, NOT NULL,
// primary key place and hidden or generated flag; a virtual table adds its
// definition (module and arguments). For an index, UNIQUE, partial, and each
// key column's name (or expression), order and collation. "" for the rest.
// The text of a table can differ between databases of the same schema (a
// migration's comments were edited after it shipped); its shape cannot.
func shape(ctx context.Context, q Querier, o SchemaObject) (string, error) {
	var b strings.Builder
	switch o.Type {
	case "table":
		var kind string
		var wr, strict int
		if err := q.QueryRowContext(ctx, `SELECT type, wr, strict FROM pragma_table_list WHERE schema = 'main' AND name = ?`, o.Name).Scan(&kind, &wr, &strict); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s wr=%d strict=%d", kind, wr, strict)
		if kind == "virtual" {
			b.WriteString(" " + strings.Join(strings.Fields(o.SQL), " "))
		}
		rows, err := q.QueryContext(ctx, `SELECT name, upper(type), "notnull", pk, hidden FROM pragma_table_xinfo(?) ORDER BY cid`, o.Name)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		for rows.Next() {
			var name, typ string
			var notNull, pk, hidden int
			if err := rows.Scan(&name, &typ, &notNull, &pk, &hidden); err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "; %s %s notnull=%d pk=%d hidden=%d", name, typ, notNull, pk, hidden)
		}
		return b.String(), rows.Err()
	case "index":
		var unique, partial int
		if err := q.QueryRowContext(ctx, `SELECT "unique", partial FROM pragma_index_list(?) WHERE name = ?`, o.Table, o.Name).Scan(&unique, &partial); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "unique=%d partial=%d", unique, partial)
		rows, err := q.QueryContext(ctx, `SELECT ifnull(name, '(expression)'), "desc", ifnull(coll, '') FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, o.Name)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		for rows.Next() {
			var name, coll string
			var desc int
			if err := rows.Scan(&name, &desc, &coll); err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "; %s desc=%d %s", name, desc, coll)
		}
		return b.String(), rows.Err()
	}
	return "", nil
}
