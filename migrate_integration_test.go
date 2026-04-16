package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	migrate "github.com/artak/go-schema-migrate"
	_ "github.com/artak/go-schema-migrate/driver/sqlite"
	_ "modernc.org/sqlite"
)

func writeMigrations(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newInMemoryMigrator(t *testing.T, dir string) *migrate.Migrator {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	m, err := migrate.New(migrate.Config{
		Source:     "file://" + dir,
		DriverName: "sqlite",
		DB:         db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func TestE2E_UpDownPlanStatus(t *testing.T) {
	dir := t.TempDir()
	writeMigrations(t, dir, map[string]string{
		"20260101000000_create_users.up.sql":    `CREATE TABLE users (id INTEGER PRIMARY KEY);`,
		"20260101000000_create_users.down.sql":  `DROP TABLE users;`,
		"20260102000000_create_orders.up.sql":   `CREATE TABLE orders (id INTEGER PRIMARY KEY);`,
		"20260102000000_create_orders.down.sql": `DROP TABLE orders;`,
	})
	m := newInMemoryMigrator(t, dir)

	plan, err := m.Plan(context.Background())
	if err != nil || len(plan) != 2 {
		t.Fatalf("Plan: want 2, got %d err %v", len(plan), err)
	}

	applied, err := m.Up(context.Background())
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(applied) != 2 {
		t.Fatalf("Up applied %d, want 2", len(applied))
	}

	status, _ := m.Status(context.Background())
	for _, s := range status {
		if !s.Applied {
			t.Fatalf("%s should be applied", s.Name)
		}
	}

	rolled, err := m.Down(context.Background(), 1)
	if err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(rolled) != 2 {
		t.Fatalf("Down rolled %d, want 2 (one batch)", len(rolled))
	}

	status, _ = m.Status(context.Background())
	for _, s := range status {
		if s.Applied {
			t.Fatalf("%s should be pending after rollback", s.Name)
		}
	}
}

func TestE2E_OutOfOrderMerge_AppliesBothMigrations(t *testing.T) {
	dir := t.TempDir()
	writeMigrations(t, dir, map[string]string{
		"20260416100000_alice.up.sql":   `CREATE TABLE alice (id INTEGER);`,
		"20260416100000_alice.down.sql": `DROP TABLE alice;`,
	})
	m := newInMemoryMigrator(t, dir)
	if _, err := m.Up(context.Background()); err != nil {
		t.Fatalf("initial Up: %v", err)
	}

	// Simulate Bob's branch merging in later.
	writeMigrations(t, dir, map[string]string{
		"20260416090000_bob.up.sql":   `CREATE TABLE bob (id INTEGER);`,
		"20260416090000_bob.down.sql": `DROP TABLE bob;`,
	})
	// Open a fresh Migrator so source is re-read, but SAME DB handle.
	m2 := newInMemoryMigratorSharingDB(t, dir, m)

	applied, err := m2.Up(context.Background())
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if len(applied) != 1 || applied[0].Name != "20260416090000_bob" {
		t.Fatalf("want bob applied in a new batch, got %+v", applied)
	}
	if applied[0].Batch != 2 {
		t.Fatalf("bob must land in batch 2, got %d", applied[0].Batch)
	}
}

func TestE2E_PlanDown_ReturnsRollbackPlan(t *testing.T) {
	dir := t.TempDir()
	writeMigrations(t, dir, map[string]string{
		"20260101000000_create_users.up.sql":    `CREATE TABLE users (id INTEGER PRIMARY KEY);`,
		"20260101000000_create_users.down.sql":  `DROP TABLE users;`,
		"20260102000000_create_orders.up.sql":   `CREATE TABLE orders (id INTEGER PRIMARY KEY);`,
		"20260102000000_create_orders.down.sql": `DROP TABLE orders;`,
	})
	m := newInMemoryMigrator(t, dir)

	if _, err := m.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}

	plan, err := m.PlanDown(context.Background(), 1)
	if err != nil {
		t.Fatalf("PlanDown: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("PlanDown: want 2, got %d", len(plan))
	}

	// Verify PlanDown does NOT modify the DB.
	status, _ := m.Status(context.Background())
	for _, s := range status {
		if !s.Applied {
			t.Fatalf("PlanDown must not change applied state; %s not applied", s.Name)
		}
	}

	// Close should be a no-op.
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func newInMemoryMigratorSharingDB(t *testing.T, dir string, orig *migrate.Migrator) *migrate.Migrator {
	t.Helper()
	db := orig.DBForTest()
	m, err := migrate.New(migrate.Config{
		Source:     "file://" + dir,
		DriverName: "sqlite",
		DB:         db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}
