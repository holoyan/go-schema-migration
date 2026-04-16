# go-schema-migrate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go migration library + CLI that keeps `golang-migrate`'s file-source ecosystem but replaces single-version tracking with a Laravel-style history table, so concurrent-developer migrations apply exactly once per environment regardless of merge order.

**Architecture:** Single Go module. Public library API (`Migrator` + `Up/Down/Plan/Status`) orchestrates a pluggable `driver.DBDriver` against golang-migrate `source.Driver`-loaded files. Drivers (Postgres, MySQL, SQLite) self-register via `database/sql`-style blank imports. CLI under `cmd/migrate/` is a thin wrapper over the library.

**Tech Stack:** Go 1.23, `github.com/golang-migrate/migrate/v4/source`, `modernc.org/sqlite` (pure-Go SQLite), `github.com/jackc/pgx/v5/stdlib` (Postgres), `github.com/go-sql-driver/mysql` (MySQL), `github.com/stretchr/testify` (assertions), `github.com/testcontainers/testcontainers-go` (integration tests).

**Spec:** [2026-04-16-go-schema-migrate-design.md](../specs/2026-04-16-go-schema-migrate-design.md)

---

## Phase Overview

| Phase | Tasks | Result |
|---|---|---|
| 1. Core engine + SQLite | 1–11 | Library usable against SQLite, including regression test for out-of-order merges |
| 2. Postgres driver | 12–13 | Library usable against Postgres |
| 3. MySQL driver | 14–15 | Library usable against MySQL |
| 4. CLI binary | 16–21 | Shippable `migrate` command |
| 5. Docs + CI | 22–24 | README, example app, GitHub Actions |

---

## File Structure (created during this plan)

```
go-schema-migrate/
├── go.mod, go.sum
├── migrate.go                  # public API surface: Migrator, Config, New
├── migrator.go                 # Up, Down, Plan, PlanDown, Status, Close
├── plan.go                     # pure diff logic: applied + onDisk → pending
├── source.go                   # wraps golang-migrate source.Driver, parses filenames
├── errors.go                   # sentinel errors
├── logger.go                   # Logger interface + noop default
├── types.go                    # AppliedMigration, PlannedMigration, MigrationStatus
├── driver/
│   ├── driver.go               # DBDriver interface + registry (Register/Get)
│   ├── postgres/postgres.go
│   ├── mysql/mysql.go
│   └── sqlite/sqlite.go
├── cmd/migrate/
│   ├── main.go                 # flag parsing + command dispatch
│   ├── config.go               # flag/env/yaml resolution
│   └── commands.go             # up/down/status/create/version handlers
├── internal/
│   └── testhelpers/
│       ├── drivercontract.go   # RunContract(t, drv) — shared driver behavior
│       ├── fakedriver.go       # in-memory fake for migrator_test.go
│       └── migrationfiles.go   # tmp-dir migration file scaffolding
├── testdata/migrations/        # real file fixtures for integration tests
├── docs/superpowers/{specs,plans}/
├── docs/README.md
├── examples/embed/             # example app using embed://
└── .github/workflows/ci.yml
```

---

# Phase 1: Core Engine + SQLite Driver

## Task 1: Initialize Go module + dependencies

**Files:**
- Create: `go.mod`
- Modify: `.gitignore` (already exists — add Go-specific entries if missing)

- [ ] **Step 1: Initialize module**

Run from project root:
```bash
go mod init github.com/artak/go-schema-migrate
```

Expected: creates `go.mod` with `module github.com/artak/go-schema-migrate` and `go 1.23`.

If the current shell Go is older than 1.23, install/select Go 1.23+ first. Verify with `go version` — must print `go1.23.x` or newer.

- [ ] **Step 2: Pin Go version in go.mod**

Open `go.mod` and ensure the `go` directive is exactly:
```
go 1.23
```

(Not `1.23.0`; use the minor-version form so patch upgrades are automatic.)

- [ ] **Step 3: (No dependencies at this stage)**

Per the spec, we do NOT depend on `github.com/golang-migrate/migrate/v4` — its `source.Driver` interface enumerates by `uint version`, incompatible with our timestamp-based discovery. We'll load files directly via `io/fs`. Test deps like `stretchr/testify` are added in later tasks when the first tests need them.

- [ ] **Step 4: Verify the module compiles empty**

Run:
```bash
go build ./...
```

Expected: no output (no .go files yet, but `go build` on an empty module succeeds silently).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: initialize go module with core dependencies"
```

---

## Task 2: Sentinel errors

**Files:**
- Create: `errors.go`
- Test: `errors_test.go`

- [ ] **Step 1: Write failing test**

Create `errors_test.go`:
```go
package migrate

import (
	"errors"
	"testing"
)

func TestSentinelErrorsAreDistinct(t *testing.T) {
	// All sentinel errors must be comparable via errors.Is and
	// must not accidentally collapse into each other.
	all := []error{
		ErrInvalidSteps,
		ErrNoRollback,
		ErrDriverNotRegistered,
		ErrInvalidMigrationName,
		ErrOrphanDownFile,
	}
	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Fatalf("sentinel %v must not match %v", a, b)
			}
		}
	}
}
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test -run TestSentinelErrorsAreDistinct ./...
```

Expected: compile error, "undefined: ErrInvalidSteps" etc.

- [ ] **Step 3: Implement errors.go**

Create `errors.go`:
```go
package migrate

import "errors"

var (
	// ErrInvalidSteps is returned by Down when steps < 1.
	ErrInvalidSteps = errors.New("migrate: steps must be >= 1")

	// ErrNoRollback is returned by Down when a migration to roll back
	// has no corresponding .down.sql file.
	ErrNoRollback = errors.New("migrate: migration has no down file")

	// ErrDriverNotRegistered is returned by New when Config.DriverName
	// is not a registered driver. Callers must blank-import the driver
	// subpackage (e.g. _ "github.com/artak/go-schema-migrate/driver/postgres").
	ErrDriverNotRegistered = errors.New("migrate: driver not registered")

	// ErrInvalidMigrationName is returned by New when a file in the
	// source does not match the required ^\d{14}_[a-z0-9_]+\.(up|down)\.sql$ pattern.
	ErrInvalidMigrationName = errors.New("migrate: invalid migration filename")

	// ErrOrphanDownFile is returned by New when a .down.sql file has
	// no corresponding .up.sql file.
	ErrOrphanDownFile = errors.New("migrate: .down.sql file has no matching .up.sql")
)
```

- [ ] **Step 4: Run test — expect pass**

```bash
go test -run TestSentinelErrorsAreDistinct ./...
```

Expected: `PASS`.

- [ ] **Step 5: Commit**

```bash
git add errors.go errors_test.go
git commit -m "feat(errors): add sentinel errors"
```

---

## Task 3: Logger interface

**Files:**
- Create: `logger.go`
- Test: `logger_test.go`

- [ ] **Step 1: Write failing test**

Create `logger_test.go`:
```go
package migrate

import "testing"

func TestNoopLoggerSatisfiesInterface(t *testing.T) {
	var l Logger = noopLogger{}
	l.Debugf("hello %s", "world")
	l.Infof("x=%d", 1)
	l.Warnf("oops")
	// no panic = pass
}

func TestDefaultLoggerIsNoop(t *testing.T) {
	got := defaultLogger(nil)
	if got == nil {
		t.Fatal("defaultLogger(nil) must not return nil")
	}
	if _, ok := got.(noopLogger); !ok {
		t.Fatalf("defaultLogger(nil) must return noopLogger, got %T", got)
	}
}

func TestDefaultLoggerPassesThrough(t *testing.T) {
	custom := &recordingLogger{}
	got := defaultLogger(custom)
	if got != custom {
		t.Fatalf("defaultLogger should pass non-nil logger through unchanged")
	}
}

type recordingLogger struct{ lines []string }

func (r *recordingLogger) Debugf(format string, args ...any) {}
func (r *recordingLogger) Infof(format string, args ...any)  {}
func (r *recordingLogger) Warnf(format string, args ...any)  {}
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test -run TestNoopLogger ./...
```

Expected: "undefined: Logger" etc.

- [ ] **Step 3: Implement logger.go**

Create `logger.go`:
```go
package migrate

// Logger receives diagnostic messages from the Migrator. Implement
// this to plug in your own logging library. Pass nil to use a no-op
// logger (recommended for most callers).
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Debugf(string, ...any) {}
func (noopLogger) Infof(string, ...any)  {}
func (noopLogger) Warnf(string, ...any)  {}

func defaultLogger(l Logger) Logger {
	if l == nil {
		return noopLogger{}
	}
	return l
}
```

- [ ] **Step 4: Run test — expect pass**

```bash
go test ./...
```

Expected: `PASS`.

- [ ] **Step 5: Commit**

```bash
git add logger.go logger_test.go
git commit -m "feat(logger): add Logger interface with noop default"
```

---

## Task 4: DBDriver interface + registry

**Files:**
- Create: `driver/driver.go`
- Test: `driver/driver_test.go`

- [ ] **Step 1: Write failing test**

Create `driver/driver_test.go`:
```go
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
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test ./driver/...
```

Expected: "undefined: Register" etc.

- [ ] **Step 3: Implement driver/driver.go**

Create `driver/driver.go`:
```go
// Package driver defines the contract database-specific adapters must
// satisfy for use with the migrate library.
package driver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNotRegistered is returned by Get when the named driver has not
// been registered. Callers typically blank-import a driver subpackage
// (e.g. _ "github.com/artak/go-schema-migrate/driver/postgres").
var ErrNotRegistered = errors.New("driver: not registered")

// AppliedRow represents one row from the history table.
type AppliedRow struct {
	Name      string
	Batch     int
	AppliedAt time.Time
}

// DBDriver is the per-database contract. Implementations are expected
// to be safe for concurrent reads but Up/Down callers coordinate write
// serialization at the library level.
type DBDriver interface {
	// Name returns the driver's registration key, e.g. "postgres".
	Name() string

	// EnsureHistoryTable creates the history table if it does not exist.
	EnsureHistoryTable(ctx context.Context, db *sql.DB, table string) error

	// AppliedNames returns the set of migration names already applied.
	AppliedNames(ctx context.Context, db *sql.DB, table string) ([]string, error)

	// NextBatch returns the batch number a new Up() run should use.
	// Equal to MAX(batch) + 1, or 1 if history is empty.
	NextBatch(ctx context.Context, db *sql.DB, table string) (int, error)

	// ApplyUp executes sqlStmt and, in the same transaction, inserts
	// (name, batch) into the history table.
	ApplyUp(ctx context.Context, db *sql.DB, table, name, sqlStmt string, batch int) error

	// ApplyDown executes sqlStmt and, in the same transaction, deletes
	// the matching row from the history table.
	ApplyDown(ctx context.Context, db *sql.DB, table, name, sqlStmt string) error

	// LastBatchMigrations returns the rows belonging to the highest
	// `batches` batch numbers, newest-first (by id DESC).
	LastBatchMigrations(ctx context.Context, db *sql.DB, table string, batches int) ([]AppliedRow, error)

	// AllMigrations returns every row in history, ordered by id ASC.
	AllMigrations(ctx context.Context, db *sql.DB, table string) ([]AppliedRow, error)
}

var (
	regMu    sync.RWMutex
	registry = map[string]DBDriver{}
)

// Register makes a DBDriver available by the name returned by d.Name().
// Typically called from an init() in a driver subpackage.
// Panics if a driver with the same name is already registered.
func Register(d DBDriver) {
	regMu.Lock()
	defer regMu.Unlock()
	name := d.Name()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("driver: %q already registered", name))
	}
	registry[name] = d
}

// Get returns the registered driver with the given name, or
// ErrNotRegistered if no such driver exists.
func Get(name string) (DBDriver, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	d, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotRegistered, name)
	}
	return d, nil
}

// resetRegistry clears the registry. Test-only helper.
func resetRegistry() {
	regMu.Lock()
	defer regMu.Unlock()
	registry = map[string]DBDriver{}
}
```

- [ ] **Step 4: Run test — expect pass**

```bash
go test ./driver/...
```

Expected: `PASS`.

- [ ] **Step 5: Commit**

```bash
git add driver/driver.go driver/driver_test.go
git commit -m "feat(driver): add DBDriver interface and registry"
```

---

## Task 5: Shared types (AppliedMigration, PlannedMigration, MigrationStatus)

**Files:**
- Create: `types.go`
- Test: `types_test.go`

- [ ] **Step 1: Write failing test**

Create `types_test.go`:
```go
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
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test -run TestAppliedMigrationZero ./...
```

Expected: "undefined: AppliedMigration".

- [ ] **Step 3: Implement types.go**

Create `types.go`:
```go
package migrate

import "time"

// AppliedMigration describes a migration that has been applied to the
// database.
type AppliedMigration struct {
	Name      string
	Batch     int
	AppliedAt time.Time
}

// PlannedMigration describes a migration that Plan or PlanDown would
// execute, but has not executed. SQL holds the file contents; Path is
// the file's absolute path.
type PlannedMigration struct {
	Name  string
	Path  string
	SQL   string
	Batch int
}

// MigrationStatus pairs a migration name with its applied state.
// Batch and AppliedAt are zero if Applied is false.
type MigrationStatus struct {
	Name      string
	Applied   bool
	Batch     int
	AppliedAt time.Time
}
```

- [ ] **Step 4: Run test — expect pass**

```bash
go test ./...
```

Expected: `PASS`.

- [ ] **Step 5: Commit**

```bash
git add types.go types_test.go
git commit -m "feat(types): add AppliedMigration, PlannedMigration, MigrationStatus"
```

---

## Task 6: Filename parsing + validation

**Files:**
- Create: `source.go` (partial — just filename helpers for now)
- Test: `source_test.go`

- [ ] **Step 1: Write failing test**

Create `source_test.go`:
```go
package migrate

import (
	"errors"
	"testing"
)

func TestParseMigrationFilename(t *testing.T) {
	tests := []struct {
		in        string
		wantName  string
		wantDir   direction
		wantError bool
	}{
		{"20260416143052_add_users.up.sql", "20260416143052_add_users", dirUp, false},
		{"20260416143052_add_users.down.sql", "20260416143052_add_users", dirDown, false},
		{"20260416143052_multi_word_name.up.sql", "20260416143052_multi_word_name", dirUp, false},
		{"20260416143052_with_123_nums.up.sql", "20260416143052_with_123_nums", dirUp, false},
		// failure cases
		{"no_timestamp.up.sql", "", "", true},
		{"2026041614305_too_short.up.sql", "", "", true}, // 13 digits
		{"202604161430521_too_long.up.sql", "", "", true}, // 15 digits
		{"20260416143052_Upper.up.sql", "", "", true},    // uppercase
		{"20260416143052_name.sideways.sql", "", "", true},
		{"20260416143052_name.up.SQL", "", "", true}, // uppercase ext
		{"20260416143052_name.sql", "", "", true},    // missing up/down
		{".up.sql", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			gotName, gotDir, err := parseMigrationFilename(tc.in)
			if tc.wantError {
				if !errors.Is(err, ErrInvalidMigrationName) {
					t.Fatalf("want ErrInvalidMigrationName, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotName != tc.wantName {
				t.Fatalf("name: want %q got %q", tc.wantName, gotName)
			}
			if gotDir != tc.wantDir {
				t.Fatalf("dir: want %q got %q", tc.wantDir, gotDir)
			}
		})
	}
}
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test -run TestParseMigrationFilename ./...
```

Expected: "undefined: parseMigrationFilename".

- [ ] **Step 3: Implement source.go (filename helpers)**

Create `source.go`:
```go
package migrate

import (
	"fmt"
	"regexp"
)

type direction string

const (
	dirUp   direction = "up"
	dirDown direction = "down"
)

// filenameRE matches migration filenames. 14 digits, underscore, snake
// case name, either .up.sql or .down.sql.
var filenameRE = regexp.MustCompile(`^(\d{14}_[a-z0-9_]+)\.(up|down)\.sql$`)

// parseMigrationFilename splits a filename into its (name, direction)
// components. Returns ErrInvalidMigrationName for any filename that
// does not match the required pattern.
func parseMigrationFilename(filename string) (name string, dir direction, err error) {
	m := filenameRE.FindStringSubmatch(filename)
	if m == nil {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidMigrationName, filename)
	}
	return m[1], direction(m[2]), nil
}
```

- [ ] **Step 4: Run test — expect pass**

```bash
go test -run TestParseMigrationFilename ./...
```

Expected: all subtests `PASS`.

- [ ] **Step 5: Commit**

```bash
git add source.go source_test.go
git commit -m "feat(source): add migration filename parser with timestamp regex"
```

---

## Task 7: Source loader — wrap golang-migrate source.Driver

**Files:**
- Modify: `source.go` (add the loader)
- Modify: `source_test.go` (add loader tests)

- [ ] **Step 1: Write failing test**

Append to `source_test.go`:
```go
import "testing/fstest"

func TestLoadSource_PairsUpAndDown(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
		"20260102000000_b.up.sql":   {Data: []byte("CREATE TABLE b();")},
		"20260102000000_b.down.sql": {Data: []byte("DROP TABLE b;")},
	}
	got, err := loadFromFS(fs)
	if err != nil {
		t.Fatalf("loadFromFS: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 migrations, got %d", len(got))
	}
	if got[0].Name != "20260101000000_a" || got[1].Name != "20260102000000_b" {
		t.Fatalf("order wrong: %+v", got)
	}
	if got[0].UpSQL != "CREATE TABLE a();" || got[0].DownSQL != "DROP TABLE a;" {
		t.Fatalf("SQL contents wrong: %+v", got[0])
	}
}

func TestLoadSource_OrphanDownErrors(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
	}
	_, err := loadFromFS(fs)
	if !errors.Is(err, ErrOrphanDownFile) {
		t.Fatalf("want ErrOrphanDownFile, got %v", err)
	}
}

func TestLoadSource_MissingDownAllowed(t *testing.T) {
	// .up.sql without .down.sql is permitted (non-reversible migration).
	fs := fstest.MapFS{
		"20260101000000_a.up.sql": {Data: []byte("CREATE TABLE a();")},
	}
	got, err := loadFromFS(fs)
	if err != nil {
		t.Fatalf("loadFromFS: %v", err)
	}
	if got[0].DownSQL != "" {
		t.Fatalf("missing down file should yield empty DownSQL, got %q", got[0].DownSQL)
	}
}

func TestLoadSource_RejectsInvalidName(t *testing.T) {
	fs := fstest.MapFS{
		"not_a_migration.txt": {Data: []byte("ignored?")},
	}
	_, err := loadFromFS(fs)
	if !errors.Is(err, ErrInvalidMigrationName) {
		t.Fatalf("want ErrInvalidMigrationName, got %v", err)
	}
}

func TestLoadSource_SortsLexically(t *testing.T) {
	// Deliberately out-of-order map iteration; loader must sort.
	fs := fstest.MapFS{
		"20260103000000_c.up.sql": {Data: []byte("")},
		"20260101000000_a.up.sql": {Data: []byte("")},
		"20260102000000_b.up.sql": {Data: []byte("")},
	}
	got, err := loadFromFS(fs)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	want := []string{"20260101000000_a", "20260102000000_b", "20260103000000_c"}
	for i := range names {
		if names[i] != want[i] {
			t.Fatalf("want sorted %v, got %v", want, names)
		}
	}
}
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test -run TestLoadSource ./...
```

Expected: "undefined: loadFromFS".

- [ ] **Step 3: Extend source.go**

Append to `source.go` (and ensure the imports block at the top includes `"io/fs"`, `"os"`, `"path/filepath"`, `"sort"`):

```go
// sourceMigration is a loaded migration with both direction SQL bodies.
type sourceMigration struct {
	Name    string // e.g. "20260416143052_add_users"
	UpPath  string // absolute path of the .up.sql file (or fs-relative in tests)
	UpSQL   string
	DownSQL string // empty if no .down.sql exists
}

// loadFromFS reads every top-level file from fsys, parses migration
// filenames, pairs up/down bodies, and returns the sorted list.
// Returns ErrInvalidMigrationName on any non-conforming filename, and
// ErrOrphanDownFile on a .down.sql without a matching .up.sql.
func loadFromFS(fsys fs.FS) ([]sourceMigration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("source: read dir: %w", err)
	}
	type half struct {
		path string
		data []byte
	}
	ups := map[string]half{}
	downs := map[string]half{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name, dir, perr := parseMigrationFilename(e.Name())
		if perr != nil {
			return nil, perr
		}
		data, rerr := fs.ReadFile(fsys, e.Name())
		if rerr != nil {
			return nil, fmt.Errorf("source: read %q: %w", e.Name(), rerr)
		}
		h := half{path: e.Name(), data: data}
		switch dir {
		case dirUp:
			ups[name] = h
		case dirDown:
			downs[name] = h
		}
	}
	for name := range downs {
		if _, ok := ups[name]; !ok {
			return nil, fmt.Errorf("%w: %s.down.sql", ErrOrphanDownFile, name)
		}
	}
	out := make([]sourceMigration, 0, len(ups))
	for name, u := range ups {
		m := sourceMigration{Name: name, UpPath: u.path, UpSQL: string(u.data)}
		if d, ok := downs[name]; ok {
			m.DownSQL = string(d.data)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// loadFromDir is the disk-path entry point used by New when the source
// URL is "file://...". Callers pass the resolved absolute path. Rewrites
// UpPath to an absolute path for better error messages.
func loadFromDir(dir string) ([]sourceMigration, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	got, err := loadFromFS(os.DirFS(abs))
	if err != nil {
		return nil, err
	}
	for i := range got {
		got[i].UpPath = filepath.Join(abs, got[i].UpPath)
	}
	return got, nil
}
```

- [ ] **Step 4: Run test — expect pass**

```bash
go test ./...
```

Expected: all source tests `PASS`.

- [ ] **Step 5: Commit**

```bash
git add source.go source_test.go
git commit -m "feat(source): load + pair migration files with lexical sort"
```

---

## Task 8: Plan diff logic

**Files:**
- Create: `plan.go`
- Test: `plan_test.go`

- [ ] **Step 1: Write failing test**

Create `plan_test.go`:
```go
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
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test -run TestComputePending ./...
```

Expected: "undefined: computePending".

- [ ] **Step 3: Implement plan.go**

Create `plan.go`:
```go
package migrate

import "time"

// historyRow is the library's internal view of a row from the driver's
// AppliedRow. Kept separate so plan.go is decoupled from driver types.
type historyRow struct {
	Name      string
	Batch     int
	AppliedAt time.Time
}

// computePending returns the subset of onDisk migrations whose names
// are not present in applied. Input ordering of onDisk is preserved.
func computePending(onDisk []sourceMigration, applied []string) []sourceMigration {
	set := make(map[string]struct{}, len(applied))
	for _, a := range applied {
		set[a] = struct{}{}
	}
	out := make([]sourceMigration, 0, len(onDisk))
	for _, m := range onDisk {
		if _, already := set[m.Name]; !already {
			out = append(out, m)
		}
	}
	return out
}

// buildStatuses left-joins onDisk against history and returns one
// MigrationStatus per file. Applied status comes from history.
func buildStatuses(onDisk []sourceMigration, history []historyRow) []MigrationStatus {
	byName := make(map[string]historyRow, len(history))
	for _, h := range history {
		byName[h.Name] = h
	}
	out := make([]MigrationStatus, 0, len(onDisk))
	for _, m := range onDisk {
		s := MigrationStatus{Name: m.Name}
		if h, ok := byName[m.Name]; ok {
			s.Applied = true
			s.Batch = h.Batch
			s.AppliedAt = h.AppliedAt
		}
		out = append(out, s)
	}
	return out
}
```

- [ ] **Step 4: Run test — expect pass**

```bash
go test ./...
```

Expected: all plan tests `PASS`.

- [ ] **Step 5: Commit**

```bash
git add plan.go plan_test.go
git commit -m "feat(plan): add pure diff logic for pending migrations and statuses"
```

---

## Task 9: Fake DBDriver + Migrator orchestration

**Files:**
- Create: `internal/testhelpers/fakedriver.go`
- Create: `migrator.go`
- Create: `migrate.go`
- Test: `migrator_test.go`

- [ ] **Step 1: Write the fake driver**

Create `internal/testhelpers/fakedriver.go`:
```go
// Package testhelpers holds shared test fixtures. Importable from tests
// in this repo only.
package testhelpers

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"time"

	"github.com/artak/go-schema-migrate/driver"
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
	FailOnApply string // if non-empty, ApplyUp returns this name's file as failed
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
	// Collect unique batch numbers, take top `batches`.
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
	for i := len(f.History) - 1; i >= 0; i-- { // id DESC
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
```

Note: this test helper imports from the main module. Because Go modules resolve internal/ paths correctly within the same module, this compiles only for tests within `github.com/artak/go-schema-migrate/...`.

- [ ] **Step 2: Write failing migrator tests**

Create `migrator_test.go`:
```go
package migrate

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/artak/go-schema-migrate/driver"
	"github.com/artak/go-schema-migrate/internal/testhelpers"
)

func newTestMigrator(t *testing.T, files fstest.MapFS) (*Migrator, *testhelpers.FakeDriver) {
	t.Helper()
	drv := &testhelpers.FakeDriver{}
	// Register the fake driver under a unique name so tests don't collide.
	// (Unregister via t.Cleanup is implemented through resetRegistry.)
	driver.Register(drv)
	t.Cleanup(func() { driver.ResetRegistryForTest() })

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
	// Alice's migration is already applied; Bob's earlier timestamp
	// arrives on disk afterward. Bob's must be applied.
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
		t.Fatalf("expected batch 2 (new run after alice's batch 1), got %d", drv.UpCalls[0].Batch)
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
		t.Fatalf("newest migration must roll back first, got %s", drv.DownCalls[0].Name)
	}
}

func TestDown_OrphanDownReturnsError(t *testing.T) {
	// History has a migration that exists on disk but its .down.sql is missing.
	fs := fstest.MapFS{
		"20260101000000_a.up.sql": {Data: []byte("")},
		// intentionally no .down.sql
	}
	m, drv := newTestMigrator(t, fs)
	drv.History = []driver.AppliedRow{{Name: "20260101000000_a", Batch: 1}}

	_, err := m.Down(context.Background(), 1)
	if !errors.Is(err, ErrNoRollback) {
		t.Fatalf("want ErrNoRollback, got %v", err)
	}
	if len(drv.DownCalls) != 0 {
		t.Fatal("no down SQL should execute when a rollback is missing")
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
```

The test imports `ResetRegistryForTest` — add that as a small exported helper in `driver/driver.go`:

Open `driver/driver.go` and add:
```go
// ResetRegistryForTest clears the registry. Exported for tests in
// other packages of this module; not part of the public API and may
// be removed in a later refactor.
func ResetRegistryForTest() {
	resetRegistry()
}
```

- [ ] **Step 3: Run tests — expect compile failure**

```bash
go test ./...
```

Expected: "undefined: Migrator" and similar — neither `migrator.go` nor `migrate.go` exist yet.

- [ ] **Step 4: Implement migrator.go and migrate.go**

Create `migrate.go`:
```go
// Package migrate provides a Laravel-style history-table database
// migration tool for Go.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/artak/go-schema-migrate/driver"
)

// Config configures a Migrator.
type Config struct {
	Source       string
	DriverName   string
	DB           *sql.DB
	HistoryTable string
	Logger       Logger
}

// Migrator runs migrations against a database.
type Migrator struct {
	cfg Config
	drv driver.DBDriver
	src []sourceMigration
	log Logger
}

// New constructs a Migrator from cfg. Returns an error if the driver
// is not registered, the source URL can't be opened, or the history
// table cannot be created.
func New(cfg Config) (*Migrator, error) {
	if cfg.HistoryTable == "" {
		cfg.HistoryTable = "schema_migrations"
	}
	if cfg.DB == nil {
		return nil, fmt.Errorf("migrate: Config.DB must be non-nil")
	}
	if cfg.Source == "" {
		return nil, fmt.Errorf("migrate: Config.Source must be non-empty")
	}
	d, err := driver.Get(cfg.DriverName)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDriverNotRegistered, cfg.DriverName)
	}
	src, err := loadSource(cfg.Source)
	if err != nil {
		return nil, err
	}
	log := defaultLogger(cfg.Logger)

	if err := d.EnsureHistoryTable(context.Background(), cfg.DB, cfg.HistoryTable); err != nil {
		return nil, fmt.Errorf("migrate: ensure history table: %w", err)
	}

	return &Migrator{cfg: cfg, drv: d, src: src, log: log}, nil
}

// Close releases any Migrator-owned resources. Does NOT close cfg.DB.
func (m *Migrator) Close() error { return nil }

// loadSource resolves a source URL into sourceMigration list. Only
// "file://" is supported today; other schemes will be added as
// golang-migrate source drivers are wired in.
func loadSource(url string) ([]sourceMigration, error) {
	const filePrefix = "file://"
	if strings.HasPrefix(url, filePrefix) {
		return loadFromDir(strings.TrimPrefix(url, filePrefix))
	}
	return nil, fmt.Errorf("migrate: unsupported source scheme %q (only file:// is supported in v1)", url)
}
```

Create `migrator.go`:
```go
package migrate

import (
	"context"
	"errors"
	"fmt"
)

// Up applies every pending migration in filename order as a new batch.
func (m *Migrator) Up(ctx context.Context) ([]AppliedMigration, error) {
	applied, err := m.drv.AppliedNames(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, fmt.Errorf("migrate: read applied: %w", err)
	}
	pending := computePending(m.src, applied)
	if len(pending) == 0 {
		m.log.Infof("migrate: nothing to do")
		return nil, nil
	}
	batch, err := m.drv.NextBatch(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, fmt.Errorf("migrate: next batch: %w", err)
	}
	out := make([]AppliedMigration, 0, len(pending))
	for _, mig := range pending {
		m.log.Infof("migrate: applying %s (batch %d)", mig.Name, batch)
		if err := m.drv.ApplyUp(ctx, m.cfg.DB, m.cfg.HistoryTable, mig.Name, mig.UpSQL, batch); err != nil {
			return out, fmt.Errorf("migrate: apply %s: %w", mig.Name, err)
		}
		out = append(out, AppliedMigration{Name: mig.Name, Batch: batch})
	}
	return out, nil
}

// Down rolls back the last `steps` batches.
func (m *Migrator) Down(ctx context.Context, steps int) ([]AppliedMigration, error) {
	if steps < 1 {
		return nil, ErrInvalidSteps
	}
	rows, err := m.drv.LastBatchMigrations(ctx, m.cfg.DB, m.cfg.HistoryTable, steps)
	if err != nil {
		return nil, fmt.Errorf("migrate: read last batches: %w", err)
	}
	// Validate every migration has a down SQL before touching the DB.
	byName := make(map[string]sourceMigration, len(m.src))
	for _, s := range m.src {
		byName[s.Name] = s
	}
	for _, r := range rows {
		s, ok := byName[r.Name]
		if !ok || s.DownSQL == "" {
			return nil, fmt.Errorf("%w: %s", ErrNoRollback, r.Name)
		}
	}
	out := make([]AppliedMigration, 0, len(rows))
	for _, r := range rows {
		s := byName[r.Name]
		m.log.Infof("migrate: rolling back %s (batch %d)", r.Name, r.Batch)
		if err := m.drv.ApplyDown(ctx, m.cfg.DB, m.cfg.HistoryTable, r.Name, s.DownSQL); err != nil {
			return out, fmt.Errorf("migrate: rollback %s: %w", r.Name, err)
		}
		out = append(out, AppliedMigration{Name: r.Name, Batch: r.Batch, AppliedAt: r.AppliedAt})
	}
	return out, nil
}

// Plan returns migrations Up would execute. Does not modify the DB.
func (m *Migrator) Plan(ctx context.Context) ([]PlannedMigration, error) {
	applied, err := m.drv.AppliedNames(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, err
	}
	pending := computePending(m.src, applied)
	if len(pending) == 0 {
		return nil, nil
	}
	batch, err := m.drv.NextBatch(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, err
	}
	out := make([]PlannedMigration, 0, len(pending))
	for _, p := range pending {
		out = append(out, PlannedMigration{Name: p.Name, Path: p.UpPath, SQL: p.UpSQL, Batch: batch})
	}
	return out, nil
}

// PlanDown returns migrations Down(steps) would roll back.
func (m *Migrator) PlanDown(ctx context.Context, steps int) ([]PlannedMigration, error) {
	if steps < 1 {
		return nil, ErrInvalidSteps
	}
	rows, err := m.drv.LastBatchMigrations(ctx, m.cfg.DB, m.cfg.HistoryTable, steps)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]sourceMigration, len(m.src))
	for _, s := range m.src {
		byName[s.Name] = s
	}
	out := make([]PlannedMigration, 0, len(rows))
	for _, r := range rows {
		s, ok := byName[r.Name]
		if !ok || s.DownSQL == "" {
			return nil, fmt.Errorf("%w: %s", ErrNoRollback, r.Name)
		}
		out = append(out, PlannedMigration{Name: r.Name, Path: s.UpPath, SQL: s.DownSQL, Batch: r.Batch})
	}
	return out, nil
}

// Status returns every migration paired with its applied state.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	rows, err := m.drv.AllMigrations(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, err
	}
	history := make([]historyRow, 0, len(rows))
	for _, r := range rows {
		history = append(history, historyRow{Name: r.Name, Batch: r.Batch, AppliedAt: r.AppliedAt})
	}
	return buildStatuses(m.src, history), nil
}

// Ensure we used errors to silence import if somehow unused.
var _ = errors.New
```

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./...
```

Expected: all tests `PASS`, including `TestUp_OutOfOrderRegression`.

- [ ] **Step 6: Commit**

```bash
git add migrate.go migrator.go internal/testhelpers/fakedriver.go migrator_test.go driver/driver.go
git commit -m "feat(migrator): add Up, Down, Plan, PlanDown, Status with fake-driver tests"
```

---

## Task 10: SQLite driver

**Files:**
- Create: `driver/sqlite/sqlite.go`
- Test: `driver/sqlite/sqlite_test.go`

- [ ] **Step 1: Add SQLite dependency**

```bash
go get modernc.org/sqlite@latest
```

Expected: added to go.mod (pure-Go SQLite, no CGo required).

- [ ] **Step 2: Write failing driver test**

Create `driver/sqlite/sqlite_test.go`:
```go
package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/artak/go-schema-migrate/driver/sqlite" // register
	"github.com/artak/go-schema-migrate/driver"
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
	// Idempotent — second call must not error.
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

	// Real table was created.
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

	// ApplyDown undoes both migration and history row in one tx.
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

	// Bad SQL — must fail, and must NOT insert a history row.
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

	// Batch 1: two migrations.
	_ = d.ApplyUp(ctx, db, "schema_migrations", "a", "CREATE TABLE t1(id INT);", 1)
	_ = d.ApplyUp(ctx, db, "schema_migrations", "b", "CREATE TABLE t2(id INT);", 1)
	// Batch 2: one migration.
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
	// Newest-first by id DESC: c, b, a.
	if rows[0].Name != "c" || rows[1].Name != "b" || rows[2].Name != "a" {
		t.Fatalf("wrong order newest-first: %+v", rows)
	}
}
```

- [ ] **Step 3: Run tests — expect compile failure**

```bash
go test ./driver/sqlite/...
```

Expected: "no Go files" or "undefined: sqlite driver" since the package does not exist yet.

- [ ] **Step 4: Implement driver/sqlite/sqlite.go**

Create `driver/sqlite/sqlite.go`:
```go
// Package sqlite implements the migrate.driver.DBDriver interface for
// SQLite via modernc.org/sqlite (pure-Go, no CGo).
//
// Register by blank-importing the package:
//
//	import _ "github.com/artak/go-schema-migrate/driver/sqlite"
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/artak/go-schema-migrate/driver"
)

func init() { driver.Register(&sqliteDriver{}) }

type sqliteDriver struct{}

func (*sqliteDriver) Name() string { return "sqlite" }

func (*sqliteDriver) EnsureHistoryTable(ctx context.Context, db *sql.DB, table string) error {
	q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL UNIQUE,
		batch      INTEGER NOT NULL,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`, quoteIdent(table))
	if _, err := db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("sqlite: create %s: %w", table, err)
	}
	idx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_batch ON %s(batch)`, table, quoteIdent(table))
	if _, err := db.ExecContext(ctx, idx); err != nil {
		return fmt.Errorf("sqlite: create batch index: %w", err)
	}
	return nil
}

func (*sqliteDriver) AppliedNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	q := fmt.Sprintf(`SELECT name FROM %s ORDER BY id ASC`, quoteIdent(table))
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (*sqliteDriver) NextBatch(ctx context.Context, db *sql.DB, table string) (int, error) {
	q := fmt.Sprintf(`SELECT COALESCE(MAX(batch), 0) + 1 FROM %s`, quoteIdent(table))
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (*sqliteDriver) ApplyUp(ctx context.Context, db *sql.DB, table, name, sqlStmt string, batch int) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("sqlite: exec migration %s: %w", name, err)
		}
		ins := fmt.Sprintf(`INSERT INTO %s (name, batch) VALUES (?, ?)`, quoteIdent(table))
		if _, err := tx.ExecContext(ctx, ins, name, batch); err != nil {
			return fmt.Errorf("sqlite: record history: %w", err)
		}
		return nil
	})
}

func (*sqliteDriver) ApplyDown(ctx context.Context, db *sql.DB, table, name, sqlStmt string) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("sqlite: exec rollback %s: %w", name, err)
		}
		del := fmt.Sprintf(`DELETE FROM %s WHERE name = ?`, quoteIdent(table))
		if _, err := tx.ExecContext(ctx, del, name); err != nil {
			return fmt.Errorf("sqlite: delete history row: %w", err)
		}
		return nil
	})
}

func (*sqliteDriver) LastBatchMigrations(ctx context.Context, db *sql.DB, table string, batches int) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf(`
		SELECT name, batch, applied_at FROM %s
		WHERE batch IN (
			SELECT batch FROM %s GROUP BY batch ORDER BY batch DESC LIMIT ?
		)
		ORDER BY id DESC
	`, quoteIdent(table), quoteIdent(table))
	return queryAppliedRows(ctx, db, q, batches)
}

func (*sqliteDriver) AllMigrations(ctx context.Context, db *sql.DB, table string) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf(`SELECT name, batch, applied_at FROM %s ORDER BY id ASC`, quoteIdent(table))
	return queryAppliedRows(ctx, db, q)
}

func queryAppliedRows(ctx context.Context, db *sql.DB, query string, args ...any) ([]driver.AppliedRow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []driver.AppliedRow
	for rows.Next() {
		var r driver.AppliedRow
		if err := rows.Scan(&r.Name, &r.Batch, &r.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// quoteIdent wraps an identifier in double quotes, doubling any embedded quotes.
// Callers should pass trusted table names (schema_migrations is the default),
// but quoting is defensive.
func quoteIdent(ident string) string {
	out := []byte{'"'}
	for i := 0; i < len(ident); i++ {
		c := ident[i]
		if c == '"' {
			out = append(out, '"')
		}
		out = append(out, c)
	}
	out = append(out, '"')
	return string(out)
}
```

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./driver/sqlite/...
```

Expected: all subtests `PASS`.

- [ ] **Step 6: Commit**

```bash
git add driver/sqlite/ go.mod go.sum
git commit -m "feat(driver/sqlite): implement DBDriver for SQLite via modernc"
```

---

## Task 11: End-to-end integration test (SQLite + real Migrator)

**Files:**
- Create: `migrate_integration_test.go`

- [ ] **Step 1: Write the end-to-end test**

Create `migrate_integration_test.go`:
```go
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

	// After full rollback, status shows everything pending.
	status, _ = m.Status(context.Background())
	for _, s := range status {
		if s.Applied {
			t.Fatalf("%s should be pending after rollback", s.Name)
		}
	}
}

func TestE2E_OutOfOrderMerge_AppliesBothMigrations(t *testing.T) {
	// Setup: Alice's DB, already migrated with her 10:00 migration.
	// Then Bob's earlier 09:00 migration arrives in the filesystem.
	// Running Up must apply Bob's — the core regression test.
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
	// Open a fresh Migrator so source is re-read.
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

// newInMemoryMigratorSharingDB re-opens a Migrator against the same in-memory
// DB as `orig`. Since SQLite :memory: is per-connection, we borrow the DB by
// reflection-free means: both migrators share the same *sql.DB handle.
func newInMemoryMigratorSharingDB(t *testing.T, dir string, orig *migrate.Migrator) *migrate.Migrator {
	t.Helper()
	db := dbFromMigrator(orig)
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
```

The test needs `dbFromMigrator` — a testing-only accessor. Expose one in `migrate.go`:

```go
// DBForTest returns the *sql.DB wired into the Migrator. Exported for
// tests across packages in this module; not part of the public API.
func (m *Migrator) DBForTest() *sql.DB { return m.cfg.DB }
```

Then add to `migrate_integration_test.go`:
```go
func dbFromMigrator(m *migrate.Migrator) *sql.DB { return m.DBForTest() }
```

- [ ] **Step 2: Run tests — expect pass**

```bash
go test ./...
```

Expected: both `TestE2E_UpDownPlanStatus` and `TestE2E_OutOfOrderMerge_AppliesBothMigrations` `PASS`.

- [ ] **Step 3: Check coverage**

```bash
go test -cover ./...
```

Expected: total coverage ≥ 80%. `migrator.go` and `plan.go` ≥ 90%. If any file falls short, add a targeted unit test before continuing.

- [ ] **Step 4: Commit**

```bash
git add migrate.go migrate_integration_test.go
git commit -m "test(e2e): cover out-of-order merge regression with real SQLite"
```

---

# Phase 2: PostgreSQL Driver

## Task 12: Driver contract test helper

**Files:**
- Create: `internal/testhelpers/drivercontract.go`

- [ ] **Step 1: Write the shared contract**

Create `internal/testhelpers/drivercontract.go`:
```go
// RunContract exercises a DBDriver against a live *sql.DB. Every
// real driver (postgres, mysql, sqlite) should invoke RunContract
// from its own integration test to prove it behaves identically.
package testhelpers

import (
	"context"
	"database/sql"
	"testing"

	"github.com/artak/go-schema-migrate/driver"
)

// RunContract runs the standard DBDriver behavior suite against drv
// using db. db must be a clean database — RunContract assumes nothing
// else is writing to it.
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
}
```

- [ ] **Step 2: Wire SQLite to use the contract**

Append to `driver/sqlite/sqlite_test.go`:
```go
import "github.com/artak/go-schema-migrate/internal/testhelpers"

func TestSQLiteDriver_Contract(t *testing.T) {
	d, _ := driver.Get("sqlite")
	db := openSQLite(t)
	testhelpers.RunContract(t, d, db)
}
```

- [ ] **Step 3: Run tests — expect pass**

```bash
go test ./...
```

Expected: all subtests `PASS`.

- [ ] **Step 4: Commit**

```bash
git add internal/testhelpers/drivercontract.go driver/sqlite/sqlite_test.go
git commit -m "test(drivercontract): add shared contract and wire SQLite through it"
```

---

## Task 13: Postgres driver + integration tests (local Postgres)

**Files:**
- Create: `driver/postgres/postgres.go`
- Create: `driver/postgres/postgres_test.go`

**Testing approach:** uses a locally running Postgres (not testcontainers). DSN comes from env var `MIGRATE_TEST_PG_DSN` with a default that works for most dev setups. Each test uses a uniquely-named table to avoid collisions with other data in the DB.

- [ ] **Step 1: Add pgx dep only**

```bash
go get github.com/jackc/pgx/v5/stdlib@latest
```

(No testcontainers — we use the host Postgres directly.)

- [ ] **Step 2: Write failing integration test (behind build tag)**

Create `driver/postgres/postgres_test.go`:
```go
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

// uniqueTable returns a test-only table name. Using the test name and
// a timestamp prevents collisions when tests run in parallel or back-to-back.
func uniqueTable(t *testing.T) string {
	return fmt.Sprintf("mig_test_%d_%s", time.Now().UnixNano(), safeName(t.Name()))
}

// safeName strips characters illegal in Postgres identifiers.
func safeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32) // lowercase
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

func TestPostgresDriver_Contract(t *testing.T) {
	d, err := driver.Get("postgres")
	if err != nil {
		t.Fatal(err)
	}
	db := openPG(t)
	// The contract test uses `test_schema_migrations` plus `contract_t1`/`contract_t2`.
	// Drop them up-front in case a previous failing run left debris.
	ctx := context.Background()
	for _, tbl := range []string{"test_schema_migrations", "contract_t1", "contract_t2", "c_a", "c_b", "c_c"} {
		_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl)
	}
	t.Cleanup(func() {
		for _, tbl := range []string{"test_schema_migrations", "contract_t1", "contract_t2", "c_a", "c_b", "c_c"} {
			_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl)
		}
	})
	testhelpers.RunContract(t, d, db)
}
```

The `t.Skipf` when Postgres is unreachable means anyone running `go test -tags=integration ./...` without Postgres configured gets a clean skip rather than a failure.

- [ ] **Step 3: Run test — expect compile failure (or skip)**

```bash
go test -tags=integration ./driver/postgres/...
```

Expected: "no Go files" — the driver package doesn't exist yet.

- [ ] **Step 4: Implement driver/postgres/postgres.go**

Create `driver/postgres/postgres.go`:
```go
// Package postgres implements the migrate.driver.DBDriver interface
// for PostgreSQL via github.com/jackc/pgx/v5/stdlib.
//
// Register by blank-importing:
//
//	import _ "github.com/artak/go-schema-migrate/driver/postgres"
package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/artak/go-schema-migrate/driver"
)

func init() { driver.Register(&pgDriver{}) }

type pgDriver struct{}

func (*pgDriver) Name() string { return "postgres" }

func (*pgDriver) EnsureHistoryTable(ctx context.Context, db *sql.DB, table string) error {
	q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id         BIGSERIAL PRIMARY KEY,
		name       VARCHAR(255) NOT NULL UNIQUE,
		batch      INTEGER NOT NULL,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`, quoteIdent(table))
	if _, err := db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("postgres: create %s: %w", table, err)
	}
	idx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_batch ON %s(batch)`, table, quoteIdent(table))
	if _, err := db.ExecContext(ctx, idx); err != nil {
		return fmt.Errorf("postgres: create batch index: %w", err)
	}
	return nil
}

func (*pgDriver) AppliedNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	q := fmt.Sprintf(`SELECT name FROM %s ORDER BY id ASC`, quoteIdent(table))
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (*pgDriver) NextBatch(ctx context.Context, db *sql.DB, table string) (int, error) {
	q := fmt.Sprintf(`SELECT COALESCE(MAX(batch), 0) + 1 FROM %s`, quoteIdent(table))
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (*pgDriver) ApplyUp(ctx context.Context, db *sql.DB, table, name, sqlStmt string, batch int) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("postgres: exec migration %s: %w", name, err)
		}
		ins := fmt.Sprintf(`INSERT INTO %s (name, batch) VALUES ($1, $2)`, quoteIdent(table))
		if _, err := tx.ExecContext(ctx, ins, name, batch); err != nil {
			return fmt.Errorf("postgres: record history: %w", err)
		}
		return nil
	})
}

func (*pgDriver) ApplyDown(ctx context.Context, db *sql.DB, table, name, sqlStmt string) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("postgres: exec rollback %s: %w", name, err)
		}
		del := fmt.Sprintf(`DELETE FROM %s WHERE name = $1`, quoteIdent(table))
		if _, err := tx.ExecContext(ctx, del, name); err != nil {
			return fmt.Errorf("postgres: delete history row: %w", err)
		}
		return nil
	})
}

func (*pgDriver) LastBatchMigrations(ctx context.Context, db *sql.DB, table string, batches int) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf(`
		SELECT name, batch, applied_at FROM %s
		WHERE batch IN (SELECT batch FROM %s GROUP BY batch ORDER BY batch DESC LIMIT $1)
		ORDER BY id DESC
	`, quoteIdent(table), quoteIdent(table))
	return queryAppliedRows(ctx, db, q, batches)
}

func (*pgDriver) AllMigrations(ctx context.Context, db *sql.DB, table string) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf(`SELECT name, batch, applied_at FROM %s ORDER BY id ASC`, quoteIdent(table))
	return queryAppliedRows(ctx, db, q)
}

func queryAppliedRows(ctx context.Context, db *sql.DB, query string, args ...any) ([]driver.AppliedRow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []driver.AppliedRow
	for rows.Next() {
		var r driver.AppliedRow
		if err := rows.Scan(&r.Name, &r.Batch, &r.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func quoteIdent(ident string) string {
	out := []byte{'"'}
	for i := 0; i < len(ident); i++ {
		c := ident[i]
		if c == '"' {
			out = append(out, '"')
		}
		out = append(out, c)
	}
	out = append(out, '"')
	return string(out)
}
```

- [ ] **Step 5: Run integration test**

Requires Docker. If Docker is not running, skip this step and verify in CI.

```bash
go test -tags=integration ./driver/postgres/...
```

Expected: `PASS` after Postgres container boots (~30-60s).

- [ ] **Step 6: Commit**

```bash
git add driver/postgres/ go.mod go.sum
git commit -m "feat(driver/postgres): implement DBDriver via pgx/v5 with contract test"
```

---

# Phase 3: MySQL Driver

## Task 14: MySQL driver + integration tests

**Files:**
- Create: `driver/mysql/mysql.go`
- Create: `driver/mysql/mysql_test.go`

- [ ] **Step 1: Add MySQL deps**

```bash
go get github.com/go-sql-driver/mysql@latest
go get github.com/testcontainers/testcontainers-go/modules/mysql@latest
```

- [ ] **Step 2: Write integration test**

Create `driver/mysql/mysql_test.go`:
```go
//go:build integration
// +build integration

package mysql_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/artak/go-schema-migrate/driver"
	_ "github.com/artak/go-schema-migrate/driver/mysql"
	"github.com/artak/go-schema-migrate/internal/testhelpers"
	_ "github.com/go-sql-driver/mysql"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
)

func TestMySQLDriver_Contract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	my, err := tcmysql.Run(ctx, "mysql:8.0",
		tcmysql.WithDatabase("migrate"),
		tcmysql.WithUsername("user"),
		tcmysql.WithPassword("pass"),
	)
	if err != nil {
		t.Fatalf("testcontainer: %v", err)
	}
	t.Cleanup(func() { _ = my.Terminate(ctx) })

	dsn, err := my.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	d, err := driver.Get("mysql")
	if err != nil {
		t.Fatal(err)
	}
	testhelpers.RunContract(t, d, db)
}
```

- [ ] **Step 3: Implement driver/mysql/mysql.go**

Create `driver/mysql/mysql.go`:
```go
// Package mysql implements migrate.driver.DBDriver for MySQL/MariaDB
// via github.com/go-sql-driver/mysql.
//
// MySQL caveat: most DDL statements auto-commit. If a single migration
// file contains multiple DDL statements and a later one fails, earlier
// DDL has already committed and the history row will NOT be written.
// Recovery: manually fix state or drop & recreate. Prefer one DDL per
// migration file to avoid this.
//
// Register by blank-importing:
//
//	import _ "github.com/artak/go-schema-migrate/driver/mysql"
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/artak/go-schema-migrate/driver"
)

func init() { driver.Register(&myDriver{}) }

type myDriver struct{}

func (*myDriver) Name() string { return "mysql" }

func (*myDriver) EnsureHistoryTable(ctx context.Context, db *sql.DB, table string) error {
	q := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s ("+
		"id BIGINT AUTO_INCREMENT PRIMARY KEY,"+
		"name VARCHAR(255) NOT NULL UNIQUE,"+
		"batch INT NOT NULL,"+
		"applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP"+
		") ENGINE=InnoDB", quoteIdent(table))
	if _, err := db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("mysql: create %s: %w", table, err)
	}
	idx := fmt.Sprintf("CREATE INDEX idx_%s_batch ON %s(batch)", strings.ReplaceAll(table, "`", ""), quoteIdent(table))
	// MySQL has no IF NOT EXISTS on CREATE INDEX; ignore duplicate-key errors.
	if _, err := db.ExecContext(ctx, idx); err != nil && !isDuplicateIndexErr(err) {
		return fmt.Errorf("mysql: create batch index: %w", err)
	}
	return nil
}

func (*myDriver) AppliedNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	q := fmt.Sprintf("SELECT name FROM %s ORDER BY id ASC", quoteIdent(table))
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (*myDriver) NextBatch(ctx context.Context, db *sql.DB, table string) (int, error) {
	q := fmt.Sprintf("SELECT COALESCE(MAX(batch), 0) + 1 FROM %s", quoteIdent(table))
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (*myDriver) ApplyUp(ctx context.Context, db *sql.DB, table, name, sqlStmt string, batch int) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("mysql: exec migration %s: %w", name, err)
		}
		ins := fmt.Sprintf("INSERT INTO %s (name, batch) VALUES (?, ?)", quoteIdent(table))
		if _, err := tx.ExecContext(ctx, ins, name, batch); err != nil {
			return fmt.Errorf("mysql: record history: %w", err)
		}
		return nil
	})
}

func (*myDriver) ApplyDown(ctx context.Context, db *sql.DB, table, name, sqlStmt string) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("mysql: exec rollback %s: %w", name, err)
		}
		del := fmt.Sprintf("DELETE FROM %s WHERE name = ?", quoteIdent(table))
		if _, err := tx.ExecContext(ctx, del, name); err != nil {
			return fmt.Errorf("mysql: delete history row: %w", err)
		}
		return nil
	})
}

func (*myDriver) LastBatchMigrations(ctx context.Context, db *sql.DB, table string, batches int) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf(
		"SELECT name, batch, applied_at FROM %s "+
			"WHERE batch IN (SELECT * FROM (SELECT batch FROM %s GROUP BY batch ORDER BY batch DESC LIMIT ?) AS t) "+
			"ORDER BY id DESC",
		quoteIdent(table), quoteIdent(table))
	return queryAppliedRows(ctx, db, q, batches)
}

func (*myDriver) AllMigrations(ctx context.Context, db *sql.DB, table string) ([]driver.AppliedRow, error) {
	q := fmt.Sprintf("SELECT name, batch, applied_at FROM %s ORDER BY id ASC", quoteIdent(table))
	return queryAppliedRows(ctx, db, q)
}

func queryAppliedRows(ctx context.Context, db *sql.DB, query string, args ...any) ([]driver.AppliedRow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []driver.AppliedRow
	for rows.Next() {
		var r driver.AppliedRow
		if err := rows.Scan(&r.Name, &r.Batch, &r.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// isDuplicateIndexErr detects MySQL error 1061 (Duplicate key name)
// so EnsureHistoryTable remains idempotent despite the lack of
// IF NOT EXISTS on CREATE INDEX.
func isDuplicateIndexErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "1061")
}

// quoteIdent wraps an identifier in backticks, doubling embedded backticks.
func quoteIdent(ident string) string {
	out := []byte{'`'}
	for i := 0; i < len(ident); i++ {
		c := ident[i]
		if c == '`' {
			out = append(out, '`')
		}
		out = append(out, c)
	}
	out = append(out, '`')
	return string(out)
}
```

- [ ] **Step 4: Run integration test (requires Docker)**

```bash
go test -tags=integration ./driver/mysql/...
```

Expected: `PASS` after MySQL container boots.

- [ ] **Step 5: Commit**

```bash
git add driver/mysql/ go.mod go.sum
git commit -m "feat(driver/mysql): implement DBDriver via go-sql-driver/mysql"
```

---

## Task 15: Cross-driver out-of-order regression

**Files:**
- Modify: `driver/postgres/postgres_test.go`
- Modify: `driver/mysql/mysql_test.go`

- [ ] **Step 1: Extract common end-to-end regression helper**

Append to `internal/testhelpers/drivercontract.go`:
```go
// RunOutOfOrderRegression verifies the library's core invariant — that
// a later-arriving migration with an earlier timestamp is applied in a
// new batch — against a real DBDriver. Call from each driver's
// integration test.
func RunOutOfOrderRegression(t *testing.T, drv driver.DBDriver, db *sql.DB) {
	ctx := context.Background()
	const table = "test_out_of_order_migrations"

	// Clean slate.
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS alice_tbl")
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS bob_tbl")
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table)

	if err := drv.EnsureHistoryTable(ctx, db, table); err != nil {
		t.Fatal(err)
	}

	// Alice's migration — timestamp 10:00 — applied first (batch 1).
	if err := drv.ApplyUp(ctx, db, table, "20260416100000_alice",
		"CREATE TABLE alice_tbl (id INTEGER)", 1); err != nil {
		t.Fatal(err)
	}

	// Now Bob's earlier-timestamp migration arrives "on disk".
	// We simulate a new Up() run: check applied set, compute pending,
	// apply in batch 2.
	applied, err := drv.AppliedNames(ctx, db, table)
	if err != nil {
		t.Fatal(err)
	}
	// Applied set contains only alice — bob is "pending".
	containsBob := false
	for _, n := range applied {
		if n == "20260416090000_bob" {
			containsBob = true
		}
	}
	if containsBob {
		t.Fatal("bob should not be in applied set before second run")
	}

	batch, err := drv.NextBatch(ctx, db, table)
	if err != nil {
		t.Fatal(err)
	}
	if batch != 2 {
		t.Fatalf("expected batch 2, got %d", batch)
	}
	if err := drv.ApplyUp(ctx, db, table, "20260416090000_bob",
		"CREATE TABLE bob_tbl (id INTEGER)", batch); err != nil {
		t.Fatal(err)
	}

	// Both migrations applied exactly once.
	all, _ := drv.AllMigrations(ctx, db, table)
	if len(all) != 2 {
		t.Fatalf("want 2 migrations, got %d", len(all))
	}
}
```

- [ ] **Step 2: Invoke from Postgres and MySQL integration tests**

Append to `driver/postgres/postgres_test.go`:
```go
func TestPostgresDriver_OutOfOrderRegression(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pg, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("migrate"),
		postgres.WithUsername("user"),
		postgres.WithPassword("pass"),
	)
	if err != nil {
		t.Fatalf("testcontainer: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	dsn, _ := pg.ConnectionString(ctx, "sslmode=disable")
	db, _ := sql.Open("pgx", dsn)
	t.Cleanup(func() { _ = db.Close() })
	d, _ := driver.Get("postgres")
	testhelpers.RunOutOfOrderRegression(t, d, db)
}
```

Append to `driver/mysql/mysql_test.go`:
```go
func TestMySQLDriver_OutOfOrderRegression(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	my, err := tcmysql.Run(ctx, "mysql:8.0",
		tcmysql.WithDatabase("migrate"),
		tcmysql.WithUsername("user"),
		tcmysql.WithPassword("pass"),
	)
	if err != nil {
		t.Fatalf("testcontainer: %v", err)
	}
	t.Cleanup(func() { _ = my.Terminate(ctx) })
	dsn, _ := my.ConnectionString(ctx)
	db, _ := sql.Open("mysql", dsn)
	t.Cleanup(func() { _ = db.Close() })
	d, _ := driver.Get("mysql")
	testhelpers.RunOutOfOrderRegression(t, d, db)
}
```

- [ ] **Step 3: Run integration tests**

```bash
go test -tags=integration ./driver/postgres/... ./driver/mysql/...
```

Expected: both `TestPostgresDriver_OutOfOrderRegression` and `TestMySQLDriver_OutOfOrderRegression` `PASS`.

- [ ] **Step 4: Commit**

```bash
git add internal/testhelpers/drivercontract.go driver/postgres/ driver/mysql/
git commit -m "test: verify out-of-order regression against real Postgres and MySQL"
```

---

# Phase 4: CLI Binary

## Task 16: CLI skeleton + config resolution

**Files:**
- Create: `cmd/migrate/main.go`
- Create: `cmd/migrate/config.go`
- Test: `cmd/migrate/config_test.go`

- [ ] **Step 1: Add yaml dep**

```bash
go get gopkg.in/yaml.v3@latest
```

- [ ] **Step 2: Write failing config test**

Create `cmd/migrate/config_test.go`:
```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfig_FlagsWinOverEnv(t *testing.T) {
	t.Setenv("MIGRATE_SOURCE", "file:///env-src")
	got, err := resolveConfig([]string{"--source", "file:///flag-src", "--database", "sqlite:///x.db"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "file:///flag-src" {
		t.Fatalf("flag must win: %q", got.Source)
	}
}

func TestResolveConfig_EnvFallback(t *testing.T) {
	t.Setenv("MIGRATE_SOURCE", "file:///env-src")
	t.Setenv("MIGRATE_DATABASE", "sqlite:///env.db")
	got, err := resolveConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "file:///env-src" || got.Database != "sqlite:///env.db" {
		t.Fatalf("env not applied: %+v", got)
	}
}

func TestResolveConfig_FromYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "migrate.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
source: file:///yaml-src
database: sqlite:///yaml.db
history_table: my_migrations
`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveConfig([]string{"--config", cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "file:///yaml-src" || got.HistoryTable != "my_migrations" {
		t.Fatalf("yaml not applied: %+v", got)
	}
}

func TestResolveConfig_DriverDerivedFromDSN(t *testing.T) {
	got, err := resolveConfig([]string{"--source", "file:///x", "--database", "postgres://u:p@h/d"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Driver != "postgres" {
		t.Fatalf("driver derivation: %q", got.Driver)
	}
}
```

- [ ] **Step 3: Run test — expect compile failure**

```bash
go test ./cmd/migrate/...
```

Expected: "undefined: resolveConfig".

- [ ] **Step 4: Implement cmd/migrate/config.go**

Create `cmd/migrate/config.go`:
```go
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type resolvedConfig struct {
	Source       string
	Database     string
	Driver       string
	HistoryTable string
	Verbose      bool
	// subcommand-specific flags populated later
}

type yamlFile struct {
	Source       string `yaml:"source"`
	Database     string `yaml:"database"`
	HistoryTable string `yaml:"history_table"`
	Verbose      bool   `yaml:"verbose"`
}

// resolveConfig parses args (not including program name) and merges with
// env and optional yaml. Precedence: flag > env > yaml > default.
func resolveConfig(args []string) (resolvedConfig, error) {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		fSource       = fs.String("source", "", "source URL (file://...)")
		fDatabase     = fs.String("database", "", "driver-prefixed DSN")
		fHistoryTable = fs.String("history-table", "", "history table name")
		fConfigPath   = fs.String("config", "", "path to migrate.yaml")
		fVerbose      = fs.Bool("verbose", false, "verbose logging")
	)
	if err := fs.Parse(args); err != nil {
		return resolvedConfig{}, err
	}

	out := resolvedConfig{}
	var yml yamlFile
	cfgPath := *fConfigPath
	if cfgPath == "" {
		cfgPath = os.Getenv("MIGRATE_CONFIG")
	}
	if cfgPath != "" {
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			return out, fmt.Errorf("read config %s: %w", cfgPath, err)
		}
		if err := yaml.Unmarshal(raw, &yml); err != nil {
			return out, fmt.Errorf("parse config %s: %w", cfgPath, err)
		}
	}

	pick := func(flagVal, envKey, yamlVal string) string {
		if flagVal != "" {
			return flagVal
		}
		if v := os.Getenv(envKey); v != "" {
			return v
		}
		return yamlVal
	}
	out.Source = pick(*fSource, "MIGRATE_SOURCE", yml.Source)
	out.Database = pick(*fDatabase, "MIGRATE_DATABASE", yml.Database)
	out.HistoryTable = pick(*fHistoryTable, "MIGRATE_HISTORY_TABLE", yml.HistoryTable)
	out.Verbose = *fVerbose || yml.Verbose || os.Getenv("MIGRATE_VERBOSE") != ""
	if out.Database != "" {
		out.Driver = driverFromDSN(out.Database)
	}
	return out, nil
}

// driverFromDSN returns the driver name for a DSN scheme.
// postgres:// → "postgres", mysql:// → "mysql", sqlite:// → "sqlite".
// Returns "" for unrecognized schemes.
func driverFromDSN(dsn string) string {
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return "postgres"
	case strings.HasPrefix(dsn, "mysql://"):
		return "mysql"
	case strings.HasPrefix(dsn, "sqlite://"), strings.HasPrefix(dsn, "sqlite3://"):
		return "sqlite"
	}
	return ""
}
```

- [ ] **Step 5: Create minimal main.go**

Create `cmd/migrate/main.go`:
```go
// Command migrate is the CLI wrapper over github.com/artak/go-schema-migrate.
package main

import (
	"fmt"
	"os"

	_ "github.com/artak/go-schema-migrate/driver/mysql"
	_ "github.com/artak/go-schema-migrate/driver/postgres"
	_ "github.com/artak/go-schema-migrate/driver/sqlite"
)

const usage = `migrate — schema migration tool with full history tracking

Usage:
  migrate <command> [flags]

Commands:
  up       Apply all pending migrations
  down     Roll back the last batch (or N batches with --step)
  status   Show applied/pending state of every migration
  create   Scaffold a new migration pair
  version  Print version info

Run 'migrate <command> --help' for command-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var code int
	switch cmd {
	case "up":
		code = runUp(args)
	case "down":
		code = runDown(args)
	case "status":
		code = runStatus(args)
	case "create":
		code = runCreate(args)
	case "version":
		code = runVersion(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		code = 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", cmd, usage)
		code = 1
	}
	os.Exit(code)
}

// Stub implementations; each gets filled in a later task.
func runUp(args []string) int      { return notImplemented("up") }
func runDown(args []string) int    { return notImplemented("down") }
func runStatus(args []string) int  { return notImplemented("status") }
func runCreate(args []string) int  { return notImplemented("create") }
func runVersion(args []string) int { return notImplemented("version") }

func notImplemented(cmd string) int {
	fmt.Fprintf(os.Stderr, "%s: not implemented in this build\n", cmd)
	return 1
}
```

- [ ] **Step 6: Run tests — expect pass**

```bash
go test ./cmd/migrate/...
go build ./cmd/migrate
```

Expected: tests pass; binary builds.

- [ ] **Step 7: Commit**

```bash
git add cmd/migrate/ go.mod go.sum
git commit -m "feat(cmd/migrate): CLI skeleton with config resolution"
```

---

## Task 17: `migrate up` and `--dry-run`

**Files:**
- Create: `cmd/migrate/commands.go`
- Modify: `cmd/migrate/main.go` (wire runUp)
- Test: `cmd/migrate/commands_test.go`

- [ ] **Step 1: Write failing end-to-end test for up**

Create `cmd/migrate/commands_test.go`:
```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
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

func TestCmdUp_AppliesAll(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})

	var out, errBuf bytes.Buffer
	code := cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("up exited %d: %s", code, errBuf.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("20260101000000_a")) {
		t.Fatalf("output missing migration name: %q", out.String())
	}
}

func TestCmdUp_DryRun_DoesNotTouchDB(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})

	var out, errBuf bytes.Buffer
	code := cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath, "--dry-run"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("dry-run exited %d: %s", code, errBuf.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("dry-run")) {
		t.Fatalf("dry-run label missing: %q", out.String())
	}
	// Now a real up should still have work to do.
	var out2, err2 bytes.Buffer
	code2 := cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &out2, &err2)
	if code2 != 0 {
		t.Fatalf("real up after dry-run exited %d: %s", code2, err2.String())
	}
	if !bytes.Contains(out2.Bytes(), []byte("20260101000000_a")) {
		t.Fatalf("real up should still report the migration, got %q", out2.String())
	}
}
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test ./cmd/migrate/...
```

Expected: "undefined: cmdUp".

- [ ] **Step 3: Implement commands.go — cmdUp**

Create `cmd/migrate/commands.go`:
```go
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"

	migrate "github.com/artak/go-schema-migrate"
)

// cmdUp implements `migrate up`. Writes human output to stdout and
// errors to stderr. Returns the process exit code.
func cmdUp(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "show what would run without executing")
	verbose := fs.Bool("verbose", false, "include SQL bodies in dry-run output")
	// Pass-through global flags.
	source := fs.String("source", "", "source URL")
	database := fs.String("database", "", "DSN")
	historyTable := fs.String("history-table", "", "history table name")
	configPath := fs.String("config", "", "config file path")

	if err := fs.Parse(args); err != nil {
		return 1
	}
	// Recreate an arg list for resolveConfig (minus up-specific flags).
	shared := []string{}
	if *source != "" {
		shared = append(shared, "--source", *source)
	}
	if *database != "" {
		shared = append(shared, "--database", *database)
	}
	if *historyTable != "" {
		shared = append(shared, "--history-table", *historyTable)
	}
	if *configPath != "" {
		shared = append(shared, "--config", *configPath)
	}
	cfg, err := resolveConfig(shared)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	m, db, err := openMigrator(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	if *dryRun {
		plan, err := m.Plan(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return printPlan(stdout, plan, "Batch %d (dry-run, nothing applied):", *verbose)
	}
	applied, err := m.Up(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(applied) == 0 {
		fmt.Fprintln(stdout, "Nothing to migrate.")
		return 0
	}
	fmt.Fprintf(stdout, "Applied %d migration(s) in batch %d:\n", len(applied), applied[0].Batch)
	for _, a := range applied {
		fmt.Fprintf(stdout, "  ✓ %s\n", a.Name)
	}
	return 0
}

func printPlan(w io.Writer, plan []migrate.PlannedMigration, header string, verbose bool) int {
	if len(plan) == 0 {
		fmt.Fprintln(w, "Nothing to migrate.")
		return 0
	}
	fmt.Fprintf(w, header+"\n", plan[0].Batch)
	for _, p := range plan {
		fmt.Fprintf(w, "  → %s    %s    (%d bytes)\n", p.Name, p.Path, len(p.SQL))
		if verbose {
			fmt.Fprintln(w, indent(p.SQL, "      "))
		}
	}
	return 0
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

// openMigrator builds a *migrate.Migrator from resolvedConfig,
// returning the underlying *sql.DB so the caller can close it.
func openMigrator(cfg resolvedConfig) (*migrate.Migrator, *sql.DB, error) {
	if cfg.Source == "" {
		return nil, nil, fmt.Errorf("--source is required")
	}
	if cfg.Database == "" {
		return nil, nil, fmt.Errorf("--database is required")
	}
	if cfg.Driver == "" {
		return nil, nil, fmt.Errorf("cannot infer driver from DSN: %s", cfg.Database)
	}
	sqlName, dsn, err := toSQLOpen(cfg.Driver, cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	db, err := sql.Open(sqlName, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	m, err := migrate.New(migrate.Config{
		Source:       cfg.Source,
		DriverName:   cfg.Driver,
		DB:           db,
		HistoryTable: cfg.HistoryTable,
	})
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return m, db, nil
}

// toSQLOpen maps our internal driver name + user-facing DSN into the
// (driverName, dsn) pair that database/sql.Open actually expects.
//
//   postgres: sql driver name is "pgx"; pgx accepts postgres:// URLs as-is.
//   sqlite:   sql driver name is "sqlite"; modernc wants a file path, not a URL.
//   mysql:    sql driver name is "mysql"; go-sql-driver/mysql wants
//             user:pass@tcp(host:port)/db?params, NOT mysql://user:pass@host/db.
func toSQLOpen(ourDriver, userDSN string) (string, string, error) {
	switch ourDriver {
	case "postgres":
		return "pgx", userDSN, nil
	case "sqlite":
		return "sqlite", strings.TrimPrefix(strings.TrimPrefix(userDSN, "sqlite3://"), "sqlite://"), nil
	case "mysql":
		conv, err := mysqlDSNFromURL(userDSN)
		if err != nil {
			return "", "", err
		}
		return "mysql", conv, nil
	}
	return "", "", fmt.Errorf("unknown driver %q", ourDriver)
}

// mysqlDSNFromURL converts a URL-form DSN (mysql://user:pass@host:port/db?params)
// into go-sql-driver/mysql's native format (user:pass@tcp(host:port)/db?params).
// If the input does not start with "mysql://" it is returned unchanged, so users
// can pass native DSNs directly when they prefer.
func mysqlDSNFromURL(in string) (string, error) {
	if !strings.HasPrefix(in, "mysql://") {
		return in, nil
	}
	u, err := url.Parse(in)
	if err != nil {
		return "", fmt.Errorf("parse mysql DSN: %w", err)
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	host := u.Host
	if host == "" {
		host = "127.0.0.1:3306"
	}
	dbname := strings.TrimPrefix(u.Path, "/")
	out := fmt.Sprintf("%s:%s@tcp(%s)/%s", user, pass, host, dbname)
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out, nil
}
```

- [ ] **Step 4: Wire runUp in main.go**

Edit `cmd/migrate/main.go`, replace the runUp stub:
```go
func runUp(args []string) int { return cmdUp(args, os.Stdout, os.Stderr) }
```

And blank-import the actual database/sql drivers at the CLI layer (our driver/* subpackages register the `migrate` DBDriver; the blank imports below register the `database/sql` driver so `sql.Open("pgx", ...)` etc. work):
```go
import (
	_ "github.com/artak/go-schema-migrate/driver/mysql"
	_ "github.com/artak/go-schema-migrate/driver/postgres"
	_ "github.com/artak/go-schema-migrate/driver/sqlite"
	_ "github.com/go-sql-driver/mysql"  // registers "mysql" sql driver
	_ "github.com/jackc/pgx/v5/stdlib"  // registers "pgx" sql driver
	_ "modernc.org/sqlite"              // registers "sqlite" sql driver
)
```

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./cmd/migrate/...
```

Expected: `TestCmdUp_AppliesAll` and `TestCmdUp_DryRun_DoesNotTouchDB` `PASS`.

- [ ] **Step 6: Commit**

```bash
git add cmd/migrate/
git commit -m "feat(cmd/migrate): implement up with --dry-run and --verbose"
```

---

## Task 18: `migrate down` with `--step`, `--force`, `--dry-run`

**Files:**
- Modify: `cmd/migrate/commands.go`
- Modify: `cmd/migrate/main.go` (wire runDown)
- Modify: `cmd/migrate/commands_test.go`

- [ ] **Step 1: Write failing tests**

Append to `cmd/migrate/commands_test.go`:
```go
import "golang.org/x/term" // ensure compile — actually no, not needed; use isatty style

func TestCmdDown_DryRun(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})
	// First: up
	var u1, u2 bytes.Buffer
	if code := cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &u1, &u2); code != 0 {
		t.Fatalf("setup up failed: %s", u2.String())
	}
	// Then: dry-run down
	var out, errB bytes.Buffer
	code := cmdDown([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath, "--dry-run"}, &out, &errB, nonInteractiveStdin{})
	if code != 0 {
		t.Fatalf("down --dry-run exited %d: %s", code, errB.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("dry-run")) {
		t.Fatalf("dry-run label missing: %q", out.String())
	}
}

func TestCmdDown_NonTTYWithoutForceRefuses(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})
	_ = cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &bytes.Buffer{}, &bytes.Buffer{})

	var out, errB bytes.Buffer
	code := cmdDown([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &out, &errB, nonInteractiveStdin{})
	if code != 3 {
		t.Fatalf("want exit 3 for non-TTY without --force, got %d", code)
	}
}

func TestCmdDown_ForceSkipsPrompt(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})
	_ = cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &bytes.Buffer{}, &bytes.Buffer{})

	var out, errB bytes.Buffer
	code := cmdDown([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath, "--force"}, &out, &errB, nonInteractiveStdin{})
	if code != 0 {
		t.Fatalf("--force should succeed, got exit %d: %s", code, errB.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("20260101000000_a")) {
		t.Fatalf("rollback output missing migration: %q", out.String())
	}
}

// nonInteractiveStdin is a stdin substitute that signals "not a TTY".
type nonInteractiveStdin struct{}

func (nonInteractiveStdin) IsTerminal() bool       { return false }
func (nonInteractiveStdin) Read([]byte) (int, error) { return 0, io.EOF }
```

Ensure `import "io"` is present.

- [ ] **Step 2: Implement cmdDown**

Append to `cmd/migrate/commands.go`:
```go
// terminalDetector lets tests inject a fake stdin.
type terminalDetector interface {
	IsTerminal() bool
	Read([]byte) (int, error)
}

// realStdin adapts os.Stdin for use as terminalDetector in production.
type realStdin struct{}

func (realStdin) IsTerminal() bool { return isTerminalFD(int(os.Stdin.Fd())) }
func (realStdin) Read(p []byte) (int, error) { return os.Stdin.Read(p) }

func cmdDown(args []string, stdout, stderr io.Writer, in terminalDetector) int {
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	fs.SetOutput(stderr)
	step := fs.Int("step", 1, "number of batches to roll back")
	dryRun := fs.Bool("dry-run", false, "show what would roll back")
	force := fs.Bool("force", false, "skip interactive confirmation")
	forceShort := fs.Bool("f", false, "alias for --force")
	source := fs.String("source", "", "source URL")
	database := fs.String("database", "", "DSN")
	historyTable := fs.String("history-table", "", "history table name")
	configPath := fs.String("config", "", "config file path")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*force = *force || *forceShort

	shared := []string{}
	if *source != "" {
		shared = append(shared, "--source", *source)
	}
	if *database != "" {
		shared = append(shared, "--database", *database)
	}
	if *historyTable != "" {
		shared = append(shared, "--history-table", *historyTable)
	}
	if *configPath != "" {
		shared = append(shared, "--config", *configPath)
	}
	cfg, err := resolveConfig(shared)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	m, db, err := openMigrator(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	plan, err := m.PlanDown(ctx, *step)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(plan) == 0 {
		fmt.Fprintln(stdout, "Nothing to roll back.")
		return 0
	}
	if *dryRun {
		return printPlan(stdout, plan, "Rolling back %d migration(s) (dry-run, nothing changed):", false)
	}
	if !*force {
		if !in.IsTerminal() {
			fmt.Fprintln(stderr, "down: refusing to run interactively from non-TTY — pass --force to confirm")
			return 3
		}
		fmt.Fprintf(stdout, "About to roll back %d migration(s):\n", len(plan))
		for _, p := range plan {
			fmt.Fprintf(stdout, "  - %s\n", p.Name)
		}
		fmt.Fprint(stdout, "Continue? [y/N] ")
		buf := make([]byte, 16)
		n, _ := in.Read(buf)
		resp := strings.TrimSpace(strings.ToLower(string(buf[:n])))
		if resp != "y" && resp != "yes" {
			fmt.Fprintln(stdout, "Aborted.")
			return 3
		}
	}
	rolled, err := m.Down(ctx, *step)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintf(stdout, "Rolled back %d migration(s):\n", len(rolled))
	for _, r := range rolled {
		fmt.Fprintf(stdout, "  ✓ %s (was batch %d)\n", r.Name, r.Batch)
	}
	return 0
}
```

Add terminal detection. Since Go 1.23 stdlib has no `term.IsTerminal`, use `golang.org/x/term`:
```bash
go get golang.org/x/term@latest
```

And at the top of `cmd/migrate/commands.go`:
```go
import "golang.org/x/term"

func isTerminalFD(fd int) bool { return term.IsTerminal(fd) }
```

- [ ] **Step 3: Wire runDown in main.go**

Replace the stub:
```go
func runDown(args []string) int { return cmdDown(args, os.Stdout, os.Stderr, realStdin{}) }
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./cmd/migrate/...
```

Expected: all three new tests `PASS`.

- [ ] **Step 5: Commit**

```bash
git add cmd/migrate/ go.mod go.sum
git commit -m "feat(cmd/migrate): implement down with --step, --dry-run, --force"
```

---

## Task 19: `migrate status` with `--pending` and `--json`

**Files:**
- Modify: `cmd/migrate/commands.go`
- Modify: `cmd/migrate/main.go`
- Modify: `cmd/migrate/commands_test.go`

- [ ] **Step 1: Write failing tests**

Append to `commands_test.go`:
```go
import "encoding/json"

func TestCmdStatus_JSON(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
		"20260102000000_b.up.sql":   "CREATE TABLE b(id INTEGER);",
		"20260102000000_b.down.sql": "DROP TABLE b;",
	})
	_ = cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &bytes.Buffer{}, &bytes.Buffer{})

	var out, errB bytes.Buffer
	code := cmdStatus([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath, "--json"}, &out, &errB)
	if code != 0 {
		t.Fatalf("status --json exited %d: %s", code, errB.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
}

func TestCmdStatus_PendingOnly(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})
	_ = cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &bytes.Buffer{}, &bytes.Buffer{})
	writeMigrations(t, mig, map[string]string{
		"20260102000000_b.up.sql":   "CREATE TABLE b(id INTEGER);",
		"20260102000000_b.down.sql": "DROP TABLE b;",
	})

	var out, errB bytes.Buffer
	code := cmdStatus([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath, "--pending"}, &out, &errB)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, errB.String())
	}
	if bytes.Contains(out.Bytes(), []byte("20260101000000_a")) {
		t.Fatalf("--pending must not include applied migrations: %q", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("20260102000000_b")) {
		t.Fatalf("--pending must include pending migration: %q", out.String())
	}
}
```

- [ ] **Step 2: Run tests — expect compile failure**

```bash
go test ./cmd/migrate/...
```

Expected: "undefined: cmdStatus".

- [ ] **Step 3: Implement cmdStatus**

Append to `commands.go`:
```go
import "encoding/json"

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pendingOnly := fs.Bool("pending", false, "show only pending migrations")
	jsonOut := fs.Bool("json", false, "machine-readable JSON output")
	source := fs.String("source", "", "source URL")
	database := fs.String("database", "", "DSN")
	historyTable := fs.String("history-table", "", "history table name")
	configPath := fs.String("config", "", "config file path")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	shared := []string{}
	if *source != "" {
		shared = append(shared, "--source", *source)
	}
	if *database != "" {
		shared = append(shared, "--database", *database)
	}
	if *historyTable != "" {
		shared = append(shared, "--history-table", *historyTable)
	}
	if *configPath != "" {
		shared = append(shared, "--config", *configPath)
	}
	cfg, err := resolveConfig(shared)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	m, db, err := openMigrator(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()

	rows, err := m.Status(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *pendingOnly {
		filtered := rows[:0]
		for _, r := range rows {
			if !r.Applied {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	if *jsonOut {
		enc := json.NewEncoder(stdout)
		if err := enc.Encode(rows); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "%-35s %-10s %-6s %s\n", "NAME", "STATE", "BATCH", "APPLIED_AT")
	for _, r := range rows {
		state := "pending"
		batch := "-"
		applied := "-"
		if r.Applied {
			state = "applied"
			batch = fmt.Sprintf("%d", r.Batch)
			applied = r.AppliedAt.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(stdout, "%-35s %-10s %-6s %s\n", r.Name, state, batch, applied)
	}
	return 0
}
```

- [ ] **Step 4: Wire runStatus**

In `main.go` replace stub:
```go
func runStatus(args []string) int { return cmdStatus(args, os.Stdout, os.Stderr) }
```

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./cmd/migrate/...
```

Expected: both status tests `PASS`.

- [ ] **Step 6: Commit**

```bash
git add cmd/migrate/
git commit -m "feat(cmd/migrate): implement status with --pending and --json"
```

---

## Task 20: `migrate create` command

**Files:**
- Modify: `cmd/migrate/commands.go`
- Modify: `cmd/migrate/main.go`
- Modify: `cmd/migrate/commands_test.go`

- [ ] **Step 1: Write failing test**

Append to `commands_test.go`:
```go
import "time"

func TestCmdCreate_WritesTimestampedPair(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}

	var out, errB bytes.Buffer
	code := cmdCreate([]string{"add_users"}, mig, &out, &errB, func() time.Time {
		return time.Date(2026, 4, 16, 15, 23, 10, 0, time.UTC)
	})
	if code != 0 {
		t.Fatalf("create exited %d: %s", code, errB.String())
	}
	for _, suffix := range []string{".up.sql", ".down.sql"} {
		p := filepath.Join(mig, "20260416152310_add_users"+suffix)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing file: %s", p)
		}
	}
}

func TestCmdCreate_InvalidName(t *testing.T) {
	dir := t.TempDir()
	var out, errB bytes.Buffer
	code := cmdCreate([]string{"Bad Name!"}, dir, &out, &errB, time.Now)
	if code == 0 {
		t.Fatal("invalid name should fail")
	}
	if !bytes.Contains(errB.Bytes(), []byte("invalid")) {
		t.Fatalf("error should mention invalid: %q", errB.String())
	}
}
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test ./cmd/migrate/...
```

Expected: "undefined: cmdCreate".

- [ ] **Step 3: Implement cmdCreate**

Append to `commands.go`:
```go
import (
	"regexp"
	"time"
)

var createNameRE = regexp.MustCompile(`^[a-z0-9_]+$`)

// cmdCreate scaffolds a new up/down migration pair. Returns exit code.
// The `now` func is injected for tests.
func cmdCreate(args []string, migrationsDir string, stdout, stderr io.Writer, now func() time.Time) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "create: migration name required")
		return 1
	}
	name := args[0]
	if !createNameRE.MatchString(name) {
		fmt.Fprintf(stderr, "create: invalid name %q — must match %s\n", name, createNameRE)
		return 1
	}
	ts := now().UTC().Format("20060102150405")
	base := ts + "_" + name
	upPath := filepath.Join(migrationsDir, base+".up.sql")
	downPath := filepath.Join(migrationsDir, base+".down.sql")
	if err := os.WriteFile(upPath, []byte("-- +up\n"), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.WriteFile(downPath, []byte("-- +down\n"), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Created:\n  %s\n  %s\n", upPath, downPath)
	return 0
}
```

Add needed imports at the top: `"path/filepath"`, `"os"`, `"regexp"`, `"time"`.

- [ ] **Step 4: Wire runCreate**

In `main.go` replace stub:
```go
func runCreate(args []string) int {
	// Source directory defaults to ./migrations; --source overrides.
	dir := "./migrations"
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--source" && i+1 < len(args) {
			dir = strings.TrimPrefix(args[i+1], "file://")
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return cmdCreate(rest, dir, os.Stdout, os.Stderr, time.Now)
}
```

Add imports `"strings"` and `"time"` in main.go.

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./cmd/migrate/...
```

Expected: both create tests `PASS`.

- [ ] **Step 6: Commit**

```bash
git add cmd/migrate/
git commit -m "feat(cmd/migrate): scaffold migration files with create"
```

---

## Task 21: `migrate version` + end-to-end CLI build test

**Files:**
- Modify: `cmd/migrate/commands.go`
- Modify: `cmd/migrate/main.go`
- Create: `cmd/migrate/e2e_test.go`

- [ ] **Step 1: Implement runVersion**

Append to `commands.go`:
```go
// Version is populated at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func cmdVersion(stdout io.Writer) int {
	fmt.Fprintf(stdout, "migrate %s\n", Version)
	return 0
}
```

Wire in main.go:
```go
func runVersion(args []string) int { return cmdVersion(os.Stdout) }
```

- [ ] **Step 2: Write failing end-to-end build test**

Create `cmd/migrate/e2e_test.go`:
```go
package main_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildBinary compiles cmd/migrate once per test binary run.
func buildBinary(t *testing.T) string {
	t.Helper()
	outDir := t.TempDir()
	bin := filepath.Join(outDir, "migrate")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, errOut.String())
	}
	return bin
}

func TestE2E_CLIUpStatusDown(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(mig, "20260101000000_a.up.sql"), []byte("CREATE TABLE a(id INTEGER);"), 0o644)
	_ = os.WriteFile(filepath.Join(mig, "20260101000000_a.down.sql"), []byte("DROP TABLE a;"), 0o644)

	run := func(args ...string) (string, int) {
		cmd := exec.Command(bin, args...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		_ = cmd.Run()
		return out.String(), cmd.ProcessState.ExitCode()
	}

	srcFlag := "--source=file://" + mig
	dbFlag := "--database=sqlite://" + dbPath

	out, code := run("up", srcFlag, dbFlag)
	if code != 0 || !strings.Contains(out, "20260101000000_a") {
		t.Fatalf("up failed (%d): %s", code, out)
	}

	out, code = run("status", srcFlag, dbFlag)
	if code != 0 || !strings.Contains(out, "applied") {
		t.Fatalf("status failed (%d): %s", code, out)
	}

	out, code = run("down", srcFlag, dbFlag, "--force")
	if code != 0 || !strings.Contains(out, "Rolled back") {
		t.Fatalf("down failed (%d): %s", code, out)
	}
}

func TestE2E_CLIVersion(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "version")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("version failed: %v", err)
	}
	if !strings.Contains(out.String(), "migrate ") {
		t.Fatalf("unexpected version output: %q", out.String())
	}
}
```

- [ ] **Step 3: Run tests — expect pass**

```bash
go test ./cmd/migrate/...
```

Expected: both E2E tests `PASS` (requires local Go toolchain — `go build` runs during test).

- [ ] **Step 4: Commit**

```bash
git add cmd/migrate/
git commit -m "feat(cmd/migrate): add version command and end-to-end CLI tests"
```

---

# Phase 5: Docs + CI

## Task 22: README + basic docs

**Files:**
- Create: `README.md`
- Create: `docs/README.md` (redirect/alias if desired; skip if README is root-only)

- [ ] **Step 1: Write README.md**

Create `README.md` at the repo root:
```markdown
# go-schema-migrate

A Go migration library and CLI with **full history tracking**, so concurrent-developer migrations are always applied exactly once per environment regardless of merge order.

Compatible with `golang-migrate`'s file sources; incompatible with its database drivers by design.

## Why

`golang-migrate` stores only the current version number. When two developers create migrations in parallel and merge in a different order than wall-clock creation order, one developer's migration can be silently skipped on environments that already advanced past its version.

This tool records every applied migration filename in a `schema_migrations` table (like Laravel) and diffs against files on disk — un-applied files are detected regardless of merge order.

## Install

```
go get github.com/artak/go-schema-migrate
go install github.com/artak/go-schema-migrate/cmd/migrate@latest
```

## Usage (library)

```go
package main

import (
	"context"
	"database/sql"
	"log"

	migrate "github.com/artak/go-schema-migrate"
	_ "github.com/artak/go-schema-migrate/driver/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	db, err := sql.Open("pgx", "postgres://user:pw@localhost/app?sslmode=disable")
	if err != nil { log.Fatal(err) }
	defer db.Close()

	m, err := migrate.New(migrate.Config{
		Source:     "file://./migrations",
		DriverName: "postgres",
		DB:         db,
	})
	if err != nil { log.Fatal(err) }

	applied, err := m.Up(context.Background())
	if err != nil { log.Fatal(err) }
	log.Printf("applied %d migration(s)", len(applied))
}
```

## Usage (CLI)

```
migrate up --source file://./migrations --database postgres://u:p@h/d
migrate status
migrate down --step 1 --force
migrate create add_users
```

See `migrate --help` for the full command list.

## Filename convention

```
<14-digit timestamp>_<snake_case_name>.up.sql
<14-digit timestamp>_<snake_case_name>.down.sql
```

e.g. `20260416143052_add_users.up.sql`.

## Supported databases

PostgreSQL, MySQL/MariaDB, SQLite.

## Design

See [docs/superpowers/specs/2026-04-16-go-schema-migrate-design.md](docs/superpowers/specs/2026-04-16-go-schema-migrate-design.md) for the full design rationale.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add README with quickstart and rationale"
```

---

## Task 23: Example app using embed://

**Files:**
- Create: `examples/embed/main.go`
- Create: `examples/embed/migrations/20260101000000_example.up.sql`
- Create: `examples/embed/migrations/20260101000000_example.down.sql`

Note: full `embed://` source support is not implemented in v1 (only `file://`). Instead, show the `file://` pattern with a committed migrations directory. We can extend later.

- [ ] **Step 1: Write example main.go**

Create `examples/embed/main.go`:
```go
// Example app demonstrating go-schema-migrate with SQLite.
//
//	cd examples/embed
//	go run . up
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	migrate "github.com/artak/go-schema-migrate"
	_ "github.com/artak/go-schema-migrate/driver/sqlite"
	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: example up | status")
	}
	db, err := sql.Open("sqlite", "example.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	m, err := migrate.New(migrate.Config{
		Source:     "file://./migrations",
		DriverName: "sqlite",
		DB:         db,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	switch os.Args[1] {
	case "up":
		applied, err := m.Up(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("applied %d migration(s)\n", len(applied))
	case "status":
		rows, err := m.Status(ctx)
		if err != nil {
			log.Fatal(err)
		}
		for _, r := range rows {
			fmt.Printf("%s\tapplied=%v\tbatch=%d\n", r.Name, r.Applied, r.Batch)
		}
	default:
		log.Fatalf("unknown command: %s", os.Args[1])
	}
}
```

- [ ] **Step 2: Write example migrations**

Create `examples/embed/migrations/20260101000000_example.up.sql`:
```sql
CREATE TABLE widgets (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL
);
```

Create `examples/embed/migrations/20260101000000_example.down.sql`:
```sql
DROP TABLE widgets;
```

- [ ] **Step 3: Verify example compiles**

```bash
cd examples/embed && go build . && cd ../..
```

Expected: builds without error.

- [ ] **Step 4: Commit**

```bash
git add examples/
git commit -m "docs: add runnable example app"
```

---

## Task 24: GitHub Actions CI

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: Write CI workflow**

Create `.github/workflows/ci.yml`:
```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  unit:
    name: unit tests (Go ${{ matrix.go }})
    runs-on: ubuntu-latest
    strategy:
      matrix:
        go: ["1.23", "1.24"]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ matrix.go }}
      - run: go mod download
      - run: go vet ./...
      - run: go test -race -cover ./...

  integration:
    name: integration tests (Postgres + MySQL)
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.23"
      - run: go mod download
      - run: go test -tags=integration -race ./driver/postgres/... ./driver/mysql/...
```

- [ ] **Step 2: Commit**

```bash
git add .github/
git commit -m "ci: add GitHub Actions workflow with unit and integration jobs"
```

---

## Final Verification

- [ ] **Step 1: Full test sweep**

```bash
go test -race -cover ./...
```

Expected: all non-integration tests `PASS`; overall coverage ≥ 80%.

- [ ] **Step 2: Check `go vet`**

```bash
go vet ./...
```

Expected: no output.

- [ ] **Step 3: Integration tests locally (requires Docker)**

```bash
go test -tags=integration ./driver/postgres/... ./driver/mysql/...
```

Expected: all tests `PASS`.

- [ ] **Step 4: Build CLI**

```bash
go build ./cmd/migrate
./migrate version
```

Expected: prints `migrate dev`.

- [ ] **Step 5: Manual smoke test**

```bash
mkdir -p /tmp/mig-smoke/migrations
./migrate create --source file:///tmp/mig-smoke/migrations add_table
ls /tmp/mig-smoke/migrations
# expect: 20260416XXXXXX_add_table.up.sql and .down.sql
```

- [ ] **Step 6: Tag v0.1.0**

```bash
git tag -a v0.1.0 -m "v0.1.0: initial release"
```

(Do not push the tag without user confirmation — semantic versioning matters for Go modules.)

---
