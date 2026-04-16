package migrate

import (
	"testing"
	"time"
)

func TestAppliedMigrationZero(t *testing.T) {
	m := AppliedMigration{Name: "a", Batch: 1, AppliedAt: time.Now()}
	if m.Name != "a" || m.Batch != 1 {
		t.Fatal("zero-value roundtrip failed")
	}
}

func TestPlannedMigrationZero(t *testing.T) {
	p := PlannedMigration{Name: "a", Path: "/x/a.up.sql", SQL: "SELECT 1", Batch: 3}
	if p.Batch != 3 {
		t.Fatal("planned fields wrong")
	}
}

func TestMigrationStatusPendingHasZeroTime(t *testing.T) {
	s := MigrationStatus{Name: "x", Applied: false}
	if !s.AppliedAt.IsZero() {
		t.Fatal("pending migration must have zero AppliedAt")
	}
}
