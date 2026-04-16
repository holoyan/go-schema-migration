// Package mysql implements migrate.driver.DBDriver for MySQL/MariaDB
// via github.com/go-sql-driver/mysql.
//
// MySQL caveat: most DDL statements auto-commit. If a single migration
// file contains multiple DDL statements and a later one fails, earlier
// DDL has already committed and the history row will NOT be written.
// Recovery: manually fix state or drop & recreate. Prefer one DDL per
// migration file to avoid this.
//
// Register by blank-importing:
//
//	import _ "github.com/artak/go-schema-migrate/driver/mysql"
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/artak/go-schema-migrate/driver"
	_ "github.com/go-sql-driver/mysql"
)

func init() { driver.Register(&myDriver{}) }

type myDriver struct{}

func (*myDriver) Name() string { return "mysql" }

func (*myDriver) EnsureHistoryTable(ctx context.Context, db *sql.DB, table string) error {
	q := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s ("+
		"id BIGINT AUTO_INCREMENT PRIMARY KEY,"+
		"name VARCHAR(255) NOT NULL UNIQUE,"+
		"batch INT NOT NULL,"+
		"applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP"+
		") ENGINE=InnoDB", quoteIdent(table))
	if _, err := db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("mysql: create %s: %w", table, err)
	}
	// MySQL has no IF NOT EXISTS on CREATE INDEX; tolerate "duplicate key name" (1061).
	idxName := "idx_" + strings.ReplaceAll(table, "`", "") + "_batch"
	idx := fmt.Sprintf("CREATE INDEX %s ON %s(batch)", quoteIdent(idxName), quoteIdent(table))
	if _, err := db.ExecContext(ctx, idx); err != nil && !isDuplicateIndexErr(err) {
		return fmt.Errorf("mysql: create batch index: %w", err)
	}
	return nil
}

func (*myDriver) AppliedNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	q := fmt.Sprintf("SELECT name FROM %s ORDER BY id ASC", quoteIdent(table))
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

func (*myDriver) NextBatch(ctx context.Context, db *sql.DB, table string) (int, error) {
	q := fmt.Sprintf("SELECT COALESCE(MAX(batch), 0) + 1 FROM %s", quoteIdent(table))
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (*myDriver) ApplyUp(ctx context.Context, db *sql.DB, table, name, sqlStmt string, batch int) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("mysql: exec migration %s: %w", name, err)
		}
		ins := fmt.Sprintf("INSERT INTO %s (name, batch) VALUES (?, ?)", quoteIdent(table))
		if _, err := tx.ExecContext(ctx, ins, name, batch); err != nil {
			return fmt.Errorf("mysql: record history: %w", err)
		}
		return nil
	})
}

func (*myDriver) ApplyDown(ctx context.Context, db *sql.DB, table, name, sqlStmt string) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("mysql: exec rollback %s: %w", name, err)
		}
		del := fmt.Sprintf("DELETE FROM %s WHERE name = ?", quoteIdent(table))
		if _, err := tx.ExecContext(ctx, del, name); err != nil {
			return fmt.Errorf("mysql: delete history row: %w", err)
		}
		return nil
	})
}

func (*myDriver) LastBatchMigrations(ctx context.Context, db *sql.DB, table string, batches int) ([]driver.AppliedRow, error) {
	// MySQL doesn't support LIMIT inside IN(SELECT ... LIMIT ...); wrap in another SELECT.
	q := fmt.Sprintf(
		"SELECT name, batch, applied_at FROM %s "+
			"WHERE batch IN (SELECT * FROM (SELECT batch FROM %s GROUP BY batch ORDER BY batch DESC LIMIT ?) AS t) "+
			"ORDER BY id DESC",
		quoteIdent(table), quoteIdent(table))
	return queryAppliedRows(ctx, db, q, batches)
}

func (*myDriver) AllMigrations(ctx context.Context, db *sql.DB, table string) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf("SELECT name, batch, applied_at FROM %s ORDER BY id ASC", quoteIdent(table))
	return queryAppliedRows(ctx, db, q)
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

// isDuplicateIndexErr detects MySQL error 1061 (Duplicate key name)
// so EnsureHistoryTable remains idempotent despite MySQL's lack of
// IF NOT EXISTS on CREATE INDEX.
func isDuplicateIndexErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "1061")
}

// quoteIdent wraps an identifier in backticks, doubling embedded backticks.
func quoteIdent(ident string) string {
	out := []byte{'`'}
	for i := 0; i < len(ident); i++ {
		c := ident[i]
		if c == '`' {
			out = append(out, '`')
		}
		out = append(out, c)
	}
	out = append(out, '`')
	return string(out)
}
