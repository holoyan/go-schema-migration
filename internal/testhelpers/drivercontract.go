// RunContract exercises a DBDriver against a live *sql.DB. Every
// real driver (postgres, mysql, sqlite) should invoke RunContract
// from its own integration test to prove it behaves identically.
package testhelpers

import (
	"context"
	"database/sql"
	"testing"

	"github.com/holoyan/go-schema-migration/driver"
)

// RunContract runs the standard DBDriver behavior suite against drv
// using db. db must be a clean database — RunContract assumes nothing
// else is writing to it. The caller is responsible for dropping any
// test tables (test_schema_migrations, contract_t1, contract_t2, c_a,
// c_b, c_c) after the test.
func RunContract(t *testing.T, drv driver.DBDriver, db *sql.DB) {
	ctx := context.Background()
	const table = "test_schema_migrations"

	t.Run("EnsureIsIdempotent", func(t *testing.T) {
		if err := drv.EnsureHistoryTable(ctx, db, table); err != nil {
			t.Fatal(err)
		}
		if err := drv.EnsureHistoryTable(ctx, db, table); err != nil {
			t.Fatalf("second call should succeed: %v", err)
		}
	})

	t.Run("NextBatchStartsAtOne", func(t *testing.T) {
		if _, err := db.Exec("DELETE FROM " + table); err != nil {
			t.Fatal(err)
		}
		got, err := drv.NextBatch(ctx, db, table)
		if err != nil {
			t.Fatal(err)
		}
		if got != 1 {
			t.Fatalf("want 1, got %d", got)
		}
	})

	t.Run("ApplyUpCreatesTableAndHistory", func(t *testing.T) {
		_, _ = db.Exec("DROP TABLE IF EXISTS contract_t1")
		_, _ = db.Exec("DELETE FROM " + table)
		if err := drv.ApplyUp(ctx, db, table, "m1", "CREATE TABLE contract_t1 (id INTEGER)", 1); err != nil {
			t.Fatal(err)
		}
		names, err := drv.AppliedNames(ctx, db, table)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 1 || names[0] != "m1" {
			t.Fatalf("applied names: %v", names)
		}
	})

	t.Run("ApplyUpFailureRollsBackHistory", func(t *testing.T) {
		_, _ = db.Exec("DELETE FROM " + table)
		err := drv.ApplyUp(ctx, db, table, "bad", "NOT VALID SQL HERE", 1)
		if err == nil {
			t.Fatal("want error")
		}
		names, _ := drv.AppliedNames(ctx, db, table)
		if len(names) != 0 {
			t.Fatalf("failed migration must leave no history row, got %v", names)
		}
	})

	t.Run("ApplyDownRemovesHistoryRow", func(t *testing.T) {
		_, _ = db.Exec("DROP TABLE IF EXISTS contract_t2")
		_, _ = db.Exec("DELETE FROM " + table)
		_ = drv.ApplyUp(ctx, db, table, "m2", "CREATE TABLE contract_t2 (id INTEGER)", 1)
		if err := drv.ApplyDown(ctx, db, table, "m2", "DROP TABLE contract_t2"); err != nil {
			t.Fatal(err)
		}
		names, _ := drv.AppliedNames(ctx, db, table)
		if len(names) != 0 {
			t.Fatalf("post-down should be empty: %v", names)
		}
	})

	t.Run("LastBatchMigrationsNewestFirst", func(t *testing.T) {
		_, _ = db.Exec("DELETE FROM " + table)
		_, _ = db.Exec("DROP TABLE IF EXISTS c_a")
		_, _ = db.Exec("DROP TABLE IF EXISTS c_b")
		_, _ = db.Exec("DROP TABLE IF EXISTS c_c")
		_ = drv.ApplyUp(ctx, db, table, "a", "CREATE TABLE c_a (id INTEGER)", 1)
		_ = drv.ApplyUp(ctx, db, table, "b", "CREATE TABLE c_b (id INTEGER)", 1)
		_ = drv.ApplyUp(ctx, db, table, "c", "CREATE TABLE c_c (id INTEGER)", 2)
		rows, err := drv.LastBatchMigrations(ctx, db, table, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Name != "c" {
			t.Fatalf("want [c], got %+v", rows)
		}
		rows, _ = drv.LastBatchMigrations(ctx, db, table, 2)
		if len(rows) != 3 || rows[0].Name != "c" || rows[1].Name != "b" || rows[2].Name != "a" {
			t.Fatalf("want [c b a] newest-first: %+v", rows)
		}
	})

	t.Run("RecordAppliedInsertsWithoutExecutingSQL", func(t *testing.T) {
		_, _ = db.Exec("DELETE FROM " + table)
		if err := drv.RecordApplied(ctx, db, table, "backfill_m1", 7); err != nil {
			t.Fatal(err)
		}
		names, err := drv.AppliedNames(ctx, db, table)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 1 || names[0] != "backfill_m1" {
			t.Fatalf("want [backfill_m1], got %v", names)
		}
		// Verify batch was stored correctly.
		rows, err := drv.AllMigrations(ctx, db, table)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Batch != 7 {
			t.Fatalf("want batch 7, got %+v", rows)
		}
	})
}
