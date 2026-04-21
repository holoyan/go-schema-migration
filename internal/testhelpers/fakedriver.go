// Package testhelpers holds shared test fixtures. Importable from tests
// in this repo only.
package testhelpers

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"time"

	"github.com/holoyan/go-schema-migration/driver"
)

// FakeDriver is an in-memory DBDriver used by migrator_test.go. It
// records every call for later inspection and simulates a history
// table as a slice.
type FakeDriver struct {
	Mu          sync.Mutex
	History     []driver.AppliedRow
	nextID      int64
	EnsureCalls int
	UpCalls     []UpCall
	DownCalls   []DownCall
	FailOnApply string // if non-empty, ApplyUp returns error for this name
}

type UpCall struct {
	Name  string
	SQL   string
	Batch int
}

type DownCall struct {
	Name string
	SQL  string
}

func (f *FakeDriver) Name() string { return "fake" }

func (f *FakeDriver) EnsureHistoryTable(ctx context.Context, db *sql.DB, table string) error {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.EnsureCalls++
	return nil
}

func (f *FakeDriver) AppliedNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	out := make([]string, 0, len(f.History))
	for _, r := range f.History {
		out = append(out, r.Name)
	}
	return out, nil
}

func (f *FakeDriver) NextBatch(ctx context.Context, db *sql.DB, table string) (int, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	max := 0
	for _, r := range f.History {
		if r.Batch > max {
			max = r.Batch
		}
	}
	return max + 1, nil
}

func (f *FakeDriver) ApplyUp(ctx context.Context, db *sql.DB, table, name, sqlStmt string, batch int) error {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.FailOnApply == name {
		return errFakeApply
	}
	f.UpCalls = append(f.UpCalls, UpCall{Name: name, SQL: sqlStmt, Batch: batch})
	f.nextID++
	f.History = append(f.History, driver.AppliedRow{
		Name:      name,
		Batch:     batch,
		AppliedAt: time.Now(),
	})
	return nil
}

func (f *FakeDriver) ApplyDown(ctx context.Context, db *sql.DB, table, name, sqlStmt string) error {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.DownCalls = append(f.DownCalls, DownCall{Name: name, SQL: sqlStmt})
	for i, r := range f.History {
		if r.Name == name {
			f.History = append(f.History[:i], f.History[i+1:]...)
			return nil
		}
	}
	return nil
}

func (f *FakeDriver) LastBatchMigrations(ctx context.Context, db *sql.DB, table string, batches int) ([]driver.AppliedRow, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	seen := map[int]struct{}{}
	for _, r := range f.History {
		seen[r.Batch] = struct{}{}
	}
	nums := make([]int, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(nums)))
	if len(nums) > batches {
		nums = nums[:batches]
	}
	targetSet := map[int]struct{}{}
	for _, n := range nums {
		targetSet[n] = struct{}{}
	}
	out := []driver.AppliedRow{}
	for i := len(f.History) - 1; i >= 0; i-- {
		r := f.History[i]
		if _, ok := targetSet[r.Batch]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *FakeDriver) AllMigrations(ctx context.Context, db *sql.DB, table string) ([]driver.AppliedRow, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	out := make([]driver.AppliedRow, len(f.History))
	copy(out, f.History)
	return out, nil
}

var errFakeApply = fakeApplyErr{}

type fakeApplyErr struct{}

func (fakeApplyErr) Error() string { return "fake: ApplyUp forced failure" }
