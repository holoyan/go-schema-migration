package driver

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

type stubDriver struct{ name string }

func (s *stubDriver) Name() string { return s.name }
func (s *stubDriver) EnsureHistoryTable(ctx context.Context, db *sql.DB, table string) error {
	return nil
}
func (s *stubDriver) AppliedNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	return nil, nil
}
func (s *stubDriver) NextBatch(ctx context.Context, db *sql.DB, table string) (int, error) {
	return 1, nil
}
func (s *stubDriver) ApplyUp(ctx context.Context, db *sql.DB, table, name, sqlStmt string, batch int) error {
	return nil
}
func (s *stubDriver) ApplyDown(ctx context.Context, db *sql.DB, table, name, sqlStmt string) error {
	return nil
}
func (s *stubDriver) LastBatchMigrations(ctx context.Context, db *sql.DB, table string, batches int) ([]AppliedRow, error) {
	return nil, nil
}
func (s *stubDriver) AllMigrations(ctx context.Context, db *sql.DB, table string) ([]AppliedRow, error) {
	return nil, nil
}
func (s *stubDriver) RecordApplied(ctx context.Context, db *sql.DB, table, name string, batch int) error {
	return nil
}

func TestRegisterAndGet(t *testing.T) {
	t.Cleanup(resetRegistry)
	d := &stubDriver{name: "stub"}
	Register(d)
	got, err := Get("stub")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name() != "stub" {
		t.Fatalf("want stub, got %s", got.Name())
	}
}

func TestGetUnregistered(t *testing.T) {
	t.Cleanup(resetRegistry)
	_, err := Get("missing")
	if !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("want ErrNotRegistered, got %v", err)
	}
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	t.Cleanup(resetRegistry)
	Register(&stubDriver{name: "dup"})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate Register")
		}
	}()
	Register(&stubDriver{name: "dup"})
}

func TestAppliedRow(t *testing.T) {
	row := AppliedRow{Name: "x", Batch: 2, AppliedAt: time.Now()}
	if row.Name != "x" || row.Batch != 2 {
		t.Fatal("AppliedRow zero value wrong")
	}
}
