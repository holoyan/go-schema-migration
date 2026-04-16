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

// ResetRegistryForTest clears the registry. Exported for tests in
// other packages of this module; not part of the public API and may
// be removed in a later refactor.
func ResetRegistryForTest() {
	resetRegistry()
}

// UnregisterForTest removes a single named driver from the registry.
// Use this instead of ResetRegistryForTest when other drivers (e.g.
// those registered by init() in a real driver package) must remain.
func UnregisterForTest(name string) {
	regMu.Lock()
	defer regMu.Unlock()
	delete(registry, name)
}
