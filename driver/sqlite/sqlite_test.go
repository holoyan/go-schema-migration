package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/holoyan/go-schema-migration/driver/sqlite" // register
	"github.com/holoyan/go-schema-migration/driver"
	"github.com/holoyan/go-schema-migration/internal/testhelpers"
	_ "modernc.org/sqlite"
)

func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSQLiteDriver_EnsureAndApply(t *testing.T) {
	d, err := driver.Get("sqlite")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	db := openSQLite(t)
	ctx := context.Background()

	if err := d.EnsureHistoryTable(ctx, db, "schema_migrations"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := d.EnsureHistoryTable(ctx, db, "schema_migrations"); err != nil {
		t.Fatalf("ensure 2nd call: %v", err)
	}

	batch, err := d.NextBatch(ctx, db, "schema_migrations")
	if err != nil || batch != 1 {
		t.Fatalf("first NextBatch want 1, got %d err %v", batch, err)
	}

	if err := d.ApplyUp(ctx, db, "schema_migrations", "20260101_a", "CREATE TABLE a(id INTEGER);", 1); err != nil {
		t.Fatalf("applyup: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='a'").Scan(&count); err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if count != 1 {
		t.Fatalf("table a not created")
	}

	names, err := d.AppliedNames(ctx, db, "schema_migrations")
	if err != nil {
		t.Fatalf("applied: %v", err)
	}
	if len(names) != 1 || names[0] != "20260101_a" {
		t.Fatalf("applied names wrong: %v", names)
	}

	if err := d.ApplyDown(ctx, db, "schema_migrations", "20260101_a", "DROP TABLE a;"); err != nil {
		t.Fatalf("applydown: %v", err)
	}
	names, _ = d.AppliedNames(ctx, db, "schema_migrations")
	if len(names) != 0 {
		t.Fatalf("post-rollback applied names should be empty: %v", names)
	}
}

func TestSQLiteDriver_FailureRollsBackHistory(t *testing.T) {
	d, _ := driver.Get("sqlite")
	db := openSQLite(t)
	ctx := context.Background()
	_ = d.EnsureHistoryTable(ctx, db, "schema_migrations")

	err := d.ApplyUp(ctx, db, "schema_migrations", "bad", "NOT VALID SQL;", 1)
	if err == nil {
		t.Fatal("expected SQL error")
	}
	names, _ := d.AppliedNames(ctx, db, "schema_migrations")
	if len(names) != 0 {
		t.Fatalf("failed migration must not leave a history row: %v", names)
	}
}

func TestSQLiteDriver_LastBatchMigrations(t *testing.T) {
	d, _ := driver.Get("sqlite")
	db := openSQLite(t)
	ctx := context.Background()
	_ = d.EnsureHistoryTable(ctx, db, "schema_migrations")

	_ = d.ApplyUp(ctx, db, "schema_migrations", "a", "CREATE TABLE t1(id INT);", 1)
	_ = d.ApplyUp(ctx, db, "schema_migrations", "b", "CREATE TABLE t2(id INT);", 1)
	_ = d.ApplyUp(ctx, db, "schema_migrations", "c", "CREATE TABLE t3(id INT);", 2)

	rows, err := d.LastBatchMigrations(ctx, db, "schema_migrations", 1)
	if err != nil {
		t.Fatalf("last: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "c" {
		t.Fatalf("last 1 batch should be [c], got %+v", rows)
	}

	rows, _ = d.LastBatchMigrations(ctx, db, "schema_migrations", 2)
	if len(rows) != 3 {
		t.Fatalf("last 2 batches should be 3 migrations, got %d", len(rows))
	}
	if rows[0].Name != "c" || rows[1].Name != "b" || rows[2].Name != "a" {
		t.Fatalf("wrong order newest-first: %+v", rows)
	}
}

func TestSQLiteDriver_Contract(t *testing.T) {
	d, _ := driver.Get("sqlite")
	db := openSQLite(t)
	testhelpers.RunContract(t, d, db)
}
