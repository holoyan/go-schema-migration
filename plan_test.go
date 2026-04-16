package migrate

import (
	"reflect"
	"testing"
)

func TestComputePending_EmptyApplied(t *testing.T) {
	onDisk := []sourceMigration{
		{Name: "20260101000000_a"},
		{Name: "20260102000000_b"},
	}
	got := computePending(onDisk, nil)
	want := []sourceMigration{
		{Name: "20260101000000_a"},
		{Name: "20260102000000_b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestComputePending_OutOfOrderMerge(t *testing.T) {
	// THE core regression: Alice's "20260416100000" already applied.
	// Bob's earlier-in-time "20260416090000" arrives later on disk.
	// It must appear in pending.
	onDisk := []sourceMigration{
		{Name: "20260416090000_bob"},
		{Name: "20260416100000_alice"},
	}
	applied := []string{"20260416100000_alice"}
	got := computePending(onDisk, applied)
	if len(got) != 1 || got[0].Name != "20260416090000_bob" {
		t.Fatalf("want [20260416090000_bob], got %+v", got)
	}
}

func TestComputePending_AllApplied(t *testing.T) {
	onDisk := []sourceMigration{{Name: "a"}, {Name: "b"}}
	applied := []string{"a", "b"}
	got := computePending(onDisk, applied)
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestComputePending_PreservesOrder(t *testing.T) {
	onDisk := []sourceMigration{
		{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"},
	}
	applied := []string{"b"}
	got := computePending(onDisk, applied)
	if len(got) != 3 || got[0].Name != "a" || got[1].Name != "c" || got[2].Name != "d" {
		t.Fatalf("want [a c d] preserving input order, got %+v", got)
	}
}

func TestBuildStatuses(t *testing.T) {
	onDisk := []sourceMigration{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	history := []historyRow{
		{Name: "a", Batch: 1},
		{Name: "b", Batch: 2},
	}
	got := buildStatuses(onDisk, history)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	if !got[0].Applied || got[0].Batch != 1 {
		t.Fatalf("a should be applied in batch 1, got %+v", got[0])
	}
	if !got[1].Applied || got[1].Batch != 2 {
		t.Fatalf("b should be applied in batch 2, got %+v", got[1])
	}
	if got[2].Applied {
		t.Fatalf("c should be pending, got %+v", got[2])
	}
}
