//go:build integration
// +build integration

package mysql_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/holoyan/go-schema-migration/driver"
	_ "github.com/holoyan/go-schema-migration/driver/mysql"
	"github.com/holoyan/go-schema-migration/internal/testhelpers"
	_ "github.com/go-sql-driver/mysql"
)

// DSN in go-sql-driver/mysql's native format:
//
//	user:pass@tcp(host:port)/db?parseTime=true
//
// parseTime=true is REQUIRED so MySQL DATETIME/TIMESTAMP columns scan
// directly into time.Time.
const defaultMySQLDSN = "artak:secret@tcp(localhost:3306)/go-schema-migrate?parseTime=true&multiStatements=true"

func openMySQL(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MIGRATE_TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = defaultMySQLDSN
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("MySQL not reachable: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func cleanupTestTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, tbl := range []string{"test_schema_migrations", "contract_t1", "contract_t2", "c_a", "c_b", "c_c"} {
		_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl)
	}
}

func TestMySQLDriver_Contract(t *testing.T) {
	d, err := driver.Get("mysql")
	if err != nil {
		t.Fatal(err)
	}
	db := openMySQL(t)
	cleanupTestTables(t, db)
	t.Cleanup(func() { cleanupTestTables(t, db) })
	testhelpers.RunContract(t, d, db)
}
