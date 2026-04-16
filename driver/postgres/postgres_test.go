//go:build integration
// +build integration

package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/artak/go-schema-migrate/driver"
	_ "github.com/artak/go-schema-migrate/driver/postgres"
	"github.com/artak/go-schema-migrate/internal/testhelpers"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const defaultPGDSN = "postgres://artak:secret@localhost:5432/go-schema-migrate?sslmode=disable"

func openPG(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MIGRATE_TEST_PG_DSN")
	if dsn == "" {
		dsn = defaultPGDSN
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("Postgres not reachable at %s: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// cleanupTestTables drops the tables RunContract touches.
func cleanupTestTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, tbl := range []string{"test_schema_migrations", "contract_t1", "contract_t2", "c_a", "c_b", "c_c"} {
		_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl)
	}
}

func TestPostgresDriver_Contract(t *testing.T) {
	d, err := driver.Get("postgres")
	if err != nil {
		t.Fatal(err)
	}
	db := openPG(t)
	cleanupTestTables(t, db)
	t.Cleanup(func() { cleanupTestTables(t, db) })
	testhelpers.RunContract(t, d, db)
}

// Silence unused-import warning for fmt if the conversion function is removed.
var _ = fmt.Sprintf
