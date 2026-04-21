// Package sqlite implements the migrate.driver.DBDriver interface for
// SQLite via modernc.org/sqlite (pure-Go, no CGo).
//
// Register by blank-importing the package:
//
//	import _ "github.com/holoyan/go-schema-migration/driver/sqlite"
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/holoyan/go-schema-migration/driver"
	_ "modernc.org/sqlite"
)

func init() { driver.Register(&sqliteDriver{}) }

type sqliteDriver struct{}

func (*sqliteDriver) Name() string { return "sqlite" }

func (*sqliteDriver) EnsureHistoryTable(ctx context.Context, db *sql.DB, table string) error {
	q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL UNIQUE,
		batch      INTEGER NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`, quoteIdent(table))
	if _, err := db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("sqlite: create %s: %w", table, err)
	}
	idx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_batch ON %s(batch)`, table, quoteIdent(table))
	if _, err := db.ExecContext(ctx, idx); err != nil {
		return fmt.Errorf("sqlite: create batch index: %w", err)
	}
	return nil
}

func (*sqliteDriver) AppliedNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	q := fmt.Sprintf(`SELECT name FROM %s ORDER BY id ASC`, quoteIdent(table))
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (*sqliteDriver) NextBatch(ctx context.Context, db *sql.DB, table string) (int, error) {
	q := fmt.Sprintf(`SELECT COALESCE(MAX(batch), 0) + 1 FROM %s`, quoteIdent(table))
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (*sqliteDriver) ApplyUp(ctx context.Context, db *sql.DB, table, name, sqlStmt string, batch int) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("sqlite: exec migration %s: %w", name, err)
		}
		ins := fmt.Sprintf(`INSERT INTO %s (name, batch) VALUES (?, ?)`, quoteIdent(table))
		if _, err := tx.ExecContext(ctx, ins, name, batch); err != nil {
			return fmt.Errorf("sqlite: record history: %w", err)
		}
		return nil
	})
}

func (*sqliteDriver) ApplyDown(ctx context.Context, db *sql.DB, table, name, sqlStmt string) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("sqlite: exec rollback %s: %w", name, err)
		}
		del := fmt.Sprintf(`DELETE FROM %s WHERE name = ?`, quoteIdent(table))
		if _, err := tx.ExecContext(ctx, del, name); err != nil {
			return fmt.Errorf("sqlite: delete history row: %w", err)
		}
		return nil
	})
}

func (*sqliteDriver) LastBatchMigrations(ctx context.Context, db *sql.DB, table string, batches int) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf(`
		SELECT name, batch, applied_at FROM %s
		WHERE batch IN (
			SELECT batch FROM %s GROUP BY batch ORDER BY batch DESC LIMIT ?
		)
		ORDER BY id DESC
	`, quoteIdent(table), quoteIdent(table))
	return queryAppliedRows(ctx, db, q, batches)
}

func (*sqliteDriver) AllMigrations(ctx context.Context, db *sql.DB, table string) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf(`SELECT name, batch, applied_at FROM %s ORDER BY id ASC`, quoteIdent(table))
	return queryAppliedRows(ctx, db, q)
}

func (*sqliteDriver) RecordApplied(ctx context.Context, db *sql.DB, table, name string, batch int) error {
	q := fmt.Sprintf(`INSERT INTO %s (name, batch) VALUES (?, ?)`, quoteIdent(table))
	if _, err := db.ExecContext(ctx, q, name, batch); err != nil {
		return fmt.Errorf("sqlite: record applied: %w", err)
	}
	return nil
}

// queryAppliedRows executes a query and scans results into []driver.AppliedRow.
// The applied_at column is stored as TEXT in SQLite and parsed manually.
func queryAppliedRows(ctx context.Context, db *sql.DB, query string, args ...any) ([]driver.AppliedRow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []driver.AppliedRow
	for rows.Next() {
		var r driver.AppliedRow
		var appliedAt string
		if err := rows.Scan(&r.Name, &r.Batch, &appliedAt); err != nil {
			return nil, err
		}
		r.AppliedAt = parseTimestamp(appliedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// parseTimestamp attempts to parse the SQLite TEXT timestamp into time.Time.
// SQLite's datetime() returns "YYYY-MM-DD HH:MM:SS" format (UTC).
func parseTimestamp(s string) time.Time {
	formats := []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		"2006-01-02T15:04:05Z",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// quoteIdent wraps an identifier in double quotes, doubling any embedded quotes.
func quoteIdent(ident string) string {
	out := []byte{'"'}
	for i := 0; i < len(ident); i++ {
		c := ident[i]
		if c == '"' {
			out = append(out, '"')
		}
		out = append(out, c)
	}
	out = append(out, '"')
	return string(out)
}
