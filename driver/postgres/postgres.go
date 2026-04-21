// Package postgres implements the migrate.driver.DBDriver interface
// for PostgreSQL via github.com/jackc/pgx/v5/stdlib.
//
// Register by blank-importing:
//
//	import _ "github.com/holoyan/go-schema-migration/driver/postgres"
package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/holoyan/go-schema-migration/driver"
)

func init() { driver.Register(&pgDriver{}) }

type pgDriver struct{}

func (*pgDriver) Name() string { return "postgres" }

func (*pgDriver) EnsureHistoryTable(ctx context.Context, db *sql.DB, table string) error {
	q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id         BIGSERIAL PRIMARY KEY,
		name       VARCHAR(255) NOT NULL UNIQUE,
		batch      INTEGER NOT NULL,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`, quoteIdent(table))
	if _, err := db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("postgres: create %s: %w", table, err)
	}
	idx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_batch ON %s(batch)`, table, quoteIdent(table))
	if _, err := db.ExecContext(ctx, idx); err != nil {
		return fmt.Errorf("postgres: create batch index: %w", err)
	}
	return nil
}

func (*pgDriver) AppliedNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
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

func (*pgDriver) NextBatch(ctx context.Context, db *sql.DB, table string) (int, error) {
	q := fmt.Sprintf(`SELECT COALESCE(MAX(batch), 0) + 1 FROM %s`, quoteIdent(table))
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (*pgDriver) ApplyUp(ctx context.Context, db *sql.DB, table, name, sqlStmt string, batch int) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("postgres: exec migration %s: %w", name, err)
		}
		ins := fmt.Sprintf(`INSERT INTO %s (name, batch) VALUES ($1, $2)`, quoteIdent(table))
		if _, err := tx.ExecContext(ctx, ins, name, batch); err != nil {
			return fmt.Errorf("postgres: record history: %w", err)
		}
		return nil
	})
}

func (*pgDriver) ApplyDown(ctx context.Context, db *sql.DB, table, name, sqlStmt string) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("postgres: exec rollback %s: %w", name, err)
		}
		del := fmt.Sprintf(`DELETE FROM %s WHERE name = $1`, quoteIdent(table))
		if _, err := tx.ExecContext(ctx, del, name); err != nil {
			return fmt.Errorf("postgres: delete history row: %w", err)
		}
		return nil
	})
}

func (*pgDriver) LastBatchMigrations(ctx context.Context, db *sql.DB, table string, batches int) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf(`
		SELECT name, batch, applied_at FROM %s
		WHERE batch IN (SELECT batch FROM %s GROUP BY batch ORDER BY batch DESC LIMIT $1)
		ORDER BY id DESC
	`, quoteIdent(table), quoteIdent(table))
	return queryAppliedRows(ctx, db, q, batches)
}

func (*pgDriver) AllMigrations(ctx context.Context, db *sql.DB, table string) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf(`SELECT name, batch, applied_at FROM %s ORDER BY id ASC`, quoteIdent(table))
	return queryAppliedRows(ctx, db, q)
}

func (*pgDriver) RecordApplied(ctx context.Context, db *sql.DB, table, name string, batch int) error {
	q := fmt.Sprintf(`INSERT INTO %s (name, batch) VALUES ($1, $2)`, quoteIdent(table))
	if _, err := db.ExecContext(ctx, q, name, batch); err != nil {
		return fmt.Errorf("postgres: record applied: %w", err)
	}
	return nil
}

func queryAppliedRows(ctx context.Context, db *sql.DB, query string, args ...any) ([]driver.AppliedRow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []driver.AppliedRow
	for rows.Next() {
		var r driver.AppliedRow
		if err := rows.Scan(&r.Name, &r.Batch, &r.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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

// quoteIdent wraps an identifier in double quotes, doubling embedded quotes.
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
