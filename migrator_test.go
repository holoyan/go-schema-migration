package migrate

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"testing/fstest"

	"github.com/holoyan/go-schema-migration/driver"
	"github.com/holoyan/go-schema-migration/internal/testhelpers"
)

func newTestMigrator(t *testing.T, files fstest.MapFS) (*Migrator, *testhelpers.FakeDriver) {
	t.Helper()
	drv := &testhelpers.FakeDriver{}
	driver.Register(drv)
	t.Cleanup(func() { driver.UnregisterForTest("fake") })

	sources, err := loadFromFS(files)
	if err != nil {
		t.Fatalf("loadFromFS: %v", err)
	}
	m := &Migrator{
		cfg: Config{DriverName: "fake", HistoryTable: "schema_migrations"},
		drv: drv,
		src: sources,
		log: noopLogger{},
	}
	return m, drv
}

func TestUp_AppliesAllPendingInOneBatch(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
		"20260102000000_b.up.sql":   {Data: []byte("CREATE TABLE b();")},
		"20260102000000_b.down.sql": {Data: []byte("DROP TABLE b;")},
	}
	m, drv := newTestMigrator(t, fs)
	got, err := m.Up(context.Background())
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 applied, got %d", len(got))
	}
	if len(drv.UpCalls) != 2 || drv.UpCalls[0].Batch != 1 || drv.UpCalls[1].Batch != 1 {
		t.Fatalf("both migrations must share batch 1, got %+v", drv.UpCalls)
	}
}

func TestUp_NothingToDoWhenAllApplied(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
	}
	m, drv := newTestMigrator(t, fs)
	drv.History = []driver.AppliedRow{{Name: "20260101000000_a", Batch: 1}}

	got, err := m.Up(context.Background())
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 applied, got %d", len(got))
	}
	if len(drv.UpCalls) != 0 {
		t.Fatalf("should have made no ApplyUp calls")
	}
}

func TestUp_OutOfOrderRegression(t *testing.T) {
	fs := fstest.MapFS{
		"20260416090000_bob.up.sql":     {Data: []byte("CREATE TABLE bob();")},
		"20260416090000_bob.down.sql":   {Data: []byte("DROP TABLE bob;")},
		"20260416100000_alice.up.sql":   {Data: []byte("CREATE TABLE alice();")},
		"20260416100000_alice.down.sql": {Data: []byte("DROP TABLE alice;")},
	}
	m, drv := newTestMigrator(t, fs)
	drv.History = []driver.AppliedRow{{Name: "20260416100000_alice", Batch: 1}}

	got, err := m.Up(context.Background())
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(got) != 1 || got[0].Name != "20260416090000_bob" {
		t.Fatalf("expected bob applied, got %+v", got)
	}
	if drv.UpCalls[0].Batch != 2 {
		t.Fatalf("expected batch 2, got %d", drv.UpCalls[0].Batch)
	}
}

func TestDown_InvalidStepsReturnsError(t *testing.T) {
	fs := fstest.MapFS{"20260101000000_a.up.sql": {Data: []byte("")}}
	m, _ := newTestMigrator(t, fs)
	_, err := m.Down(context.Background(), 0)
	if !errors.Is(err, ErrInvalidSteps) {
		t.Fatalf("want ErrInvalidSteps, got %v", err)
	}
	_, err = m.Down(context.Background(), -1)
	if !errors.Is(err, ErrInvalidSteps) {
		t.Fatalf("want ErrInvalidSteps, got %v", err)
	}
}

func TestDown_RollsBackLastBatchNewestFirst(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
		"20260102000000_b.up.sql":   {Data: []byte("")},
		"20260102000000_b.down.sql": {Data: []byte("DROP TABLE b;")},
	}
	m, drv := newTestMigrator(t, fs)
	drv.History = []driver.AppliedRow{
		{Name: "20260101000000_a", Batch: 1},
		{Name: "20260102000000_b", Batch: 1},
	}
	_, err := m.Down(context.Background(), 1)
	if err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(drv.DownCalls) != 2 {
		t.Fatalf("want 2 down calls, got %d", len(drv.DownCalls))
	}
	if drv.DownCalls[0].Name != "20260102000000_b" {
		t.Fatalf("newest must roll back first, got %s", drv.DownCalls[0].Name)
	}
}

func TestDown_OrphanDownReturnsError(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql": {Data: []byte("")},
	}
	m, drv := newTestMigrator(t, fs)
	drv.History = []driver.AppliedRow{{Name: "20260101000000_a", Batch: 1}}

	_, err := m.Down(context.Background(), 1)
	if !errors.Is(err, ErrNoRollback) {
		t.Fatalf("want ErrNoRollback, got %v", err)
	}
	if len(drv.DownCalls) != 0 {
		t.Fatal("no down SQL should execute when rollback missing")
	}
}

func TestDown_StepsCapsAtHistorySize(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("")},
		"20260101000000_a.down.sql": {Data: []byte("")},
	}
	m, drv := newTestMigrator(t, fs)
	drv.History = []driver.AppliedRow{{Name: "20260101000000_a", Batch: 1}}

	got, err := m.Down(context.Background(), 999)
	if err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 rolled back, got %d", len(got))
	}
}

func TestPlan_DoesNotCallApply(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("")},
	}
	m, drv := newTestMigrator(t, fs)

	got, err := m.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(got) != 1 || got[0].SQL != "CREATE TABLE a();" || got[0].Batch != 1 {
		t.Fatalf("Plan returned wrong data: %+v", got)
	}
	if len(drv.UpCalls) != 0 {
		t.Fatal("Plan must not call ApplyUp")
	}
}

func TestBackfill_DefaultBatchPerMigration(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
		"20260102000000_b.up.sql":   {Data: []byte("CREATE TABLE b();")},
		"20260102000000_b.down.sql": {Data: []byte("DROP TABLE b;")},
	}
	m, drv := newTestMigrator(t, fs)

	got, err := m.Backfill(context.Background(), BackfillOptions{})
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 recorded, got %d", len(got))
	}
	// Each migration gets its own incrementing batch.
	if got[0].Name != "20260101000000_a" || got[0].Batch != 1 {
		t.Fatalf("first: want (20260101000000_a, batch 1), got %+v", got[0])
	}
	if got[1].Name != "20260102000000_b" || got[1].Batch != 2 {
		t.Fatalf("second: want (20260102000000_b, batch 2), got %+v", got[1])
	}
	// History must have been written.
	if len(drv.History) != 2 {
		t.Fatalf("want 2 history rows, got %d", len(drv.History))
	}
}

func TestBackfill_UserSuppliedBatch(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
		"20260102000000_b.up.sql":   {Data: []byte("CREATE TABLE b();")},
		"20260102000000_b.down.sql": {Data: []byte("DROP TABLE b;")},
	}
	m, drv := newTestMigrator(t, fs)

	got, err := m.Backfill(context.Background(), BackfillOptions{Batch: 5})
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 recorded, got %d", len(got))
	}
	for _, a := range got {
		if a.Batch != 5 {
			t.Fatalf("all migrations must share batch 5, got %+v", a)
		}
	}
	if len(drv.History) != 2 {
		t.Fatalf("want 2 history rows, got %d", len(drv.History))
	}
}

func TestBackfill_Idempotent(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
	}
	m, drv := newTestMigrator(t, fs)

	first, err := m.Backfill(context.Background(), BackfillOptions{})
	if err != nil {
		t.Fatalf("first Backfill: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("want 1 recorded, got %d", len(first))
	}

	second, err := m.Backfill(context.Background(), BackfillOptions{})
	if err != nil {
		t.Fatalf("second Backfill: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second run must record nothing, got %d", len(second))
	}
	// History still has exactly 1 row.
	if len(drv.History) != 1 {
		t.Fatalf("want 1 history row after second run, got %d", len(drv.History))
	}
}

func TestBackfill_CustomRegex(t *testing.T) {
	// golang-migrate-style filenames: 000001_init.up.sql
	customRE := regexp.MustCompile(`^(\d+_[a-zA-Z0-9_]+)\.(up|down)\.sql$`)
	customFS := fstest.MapFS{
		"000001_create_users.up.sql":   {Data: []byte("CREATE TABLE users (id INTEGER);")},
		"000001_create_users.down.sql": {Data: []byte("DROP TABLE users;")},
		"000002_add_orders.up.sql":     {Data: []byte("CREATE TABLE orders (id INTEGER);")},
		"000002_add_orders.down.sql":   {Data: []byte("DROP TABLE orders;")},
	}
	// Build the migrator directly using the custom regex so source loading succeeds.
	drv := &testhelpers.FakeDriver{}
	driver.Register(drv)
	t.Cleanup(func() { driver.UnregisterForTest("fake") })

	src, err := loadFromFSWithRegex(customFS, customRE)
	if err != nil {
		t.Fatalf("loadFromFSWithRegex: %v", err)
	}
	m := &Migrator{
		cfg: Config{DriverName: "fake", HistoryTable: "schema_migrations"},
		drv: drv,
		src: src,
		log: noopLogger{},
	}

	got, err := m.Backfill(context.Background(), BackfillOptions{})
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 recorded, got %d", len(got))
	}
	if got[0].Name != "000001_create_users" {
		t.Fatalf("wrong first migration: %+v", got[0])
	}
	if len(drv.History) != 2 {
		t.Fatalf("want 2 history rows, got %d", len(drv.History))
	}
}

func TestBackfill_NeverExecutesSQL(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
	}
	m, drv := newTestMigrator(t, fs)

	_, err := m.Backfill(context.Background(), BackfillOptions{})
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if len(drv.UpCalls) != 0 {
		t.Fatalf("backfill must never call ApplyUp, got %d calls", len(drv.UpCalls))
	}
	if len(drv.History) != 1 {
		t.Fatalf("want 1 history row, got %d", len(drv.History))
	}
}

func TestBackfill_DryRunWritesNothing(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
		"20260102000000_b.up.sql":   {Data: []byte("CREATE TABLE b();")},
		"20260102000000_b.down.sql": {Data: []byte("DROP TABLE b;")},
	}
	m, drv := newTestMigrator(t, fs)

	got, err := m.Backfill(context.Background(), BackfillOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Backfill dry-run: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("dry-run must return the plan (2 items), got %d", len(got))
	}
	if len(drv.History) != 0 {
		t.Fatalf("dry-run must write nothing to history, got %d rows", len(drv.History))
	}
}

func TestBackfill_NothingToDoReturnsNil(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
	}
	m, drv := newTestMigrator(t, fs)
	drv.History = []driver.AppliedRow{{Name: "20260101000000_a", Batch: 1}}

	got, err := m.Backfill(context.Background(), BackfillOptions{})
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if got != nil {
		t.Fatalf("want nil when nothing to backfill, got %v", got)
	}
}

func TestStatus_ShowsAppliedAndPending(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("")},
		"20260101000000_a.down.sql": {Data: []byte("")},
		"20260102000000_b.up.sql":   {Data: []byte("")},
	}
	m, drv := newTestMigrator(t, fs)
	drv.History = []driver.AppliedRow{{Name: "20260101000000_a", Batch: 1}}

	got, err := m.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if !got[0].Applied || got[1].Applied {
		t.Fatalf("applied flags wrong: %+v", got)
	}
}
