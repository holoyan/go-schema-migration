# go-schema-migrate — Design

**Date:** 2026-04-16
**Status:** Approved design, pending implementation plan.

## Problem

`golang-migrate` stores only the current version number in `schema_migrations`. When two developers create migrations in parallel and merge in a different order than wall-clock creation order, one developer's migration can be silently skipped on environments that already advanced past its version. Laravel solves this by recording every applied migration filename and diffing against files on disk — un-applied files are detected regardless of merge order.

## Goal

Build a Go migration library and CLI that keeps `golang-migrate`'s file-source ecosystem but replaces its version-tracking model with a Laravel-style history table, so concurrent-developer migrations are always applied exactly once per environment.

## Non-goals (v1)

- Checksum / drift detection on applied migrations.
- Go-code (non-SQL) migrations.
- Distributed locking / concurrent-runner safety (documented as single-runner).
- Partial `Up` (goto / steps) — `Up` always applies all pending.
- `force <version>` / `drop` commands.
- SQL Server, CockroachDB, and other databases beyond Postgres/MySQL/SQLite.

## Architectural decisions

| Decision | Choice | Rationale |
|---|---|---|
| Shape | Library + CLI | Users embed in app startup or run via CI. |
| Relation to golang-migrate | Reuse its **source drivers only** (`file://`, `embed://`, `github://`, …); write our own DB layer | Its DB drivers assume single-version tracking — incompatible with history-table approach. |
| Filename convention | Timestamp prefix, e.g. `20260416143052_add_users.up.sql`. Enforced via regex `^\d{14}_[a-z0-9_]+\.(up|down)\.sql$` at source-load time | Natural lexical sort; second-level resolution prevents collisions between devs. Non-conforming names cause `New()` to return an error. |
| Rollback model | Laravel-style batches | Every `Up` creates a batch; `Down` undoes last `N` batches. Matches "undo my last deploy". |
| Databases (v1) | PostgreSQL, MySQL/MariaDB, SQLite | Covers the Go backend ecosystem. |
| Checksums | None | Keep simple; trust discipline. |
| Migration types | SQL only | No Go-function migrations. |
| Concurrency | No advisory locks | Documented as single-runner; users orchestrate via their deploy tooling. |
| Driver registration | `database/sql`-style blank imports | `import _ ".../driver/postgres"` registers. Familiar, keeps binaries slim. |
| Minimum Go version | 1.23 | Iterators available; broad enterprise adoption. |

## Project layout

```
go-schema-migrate/
├── go.mod                                   # module github.com/artak/go-schema-migrate
├── migrate.go                               # public API: Migrator, New, Up, Down, Plan, Status
├── migrator.go                              # internal orchestration
├── plan.go                                  # diffing filesystem vs history
├── source.go                                # wraps golang-migrate source.Driver
├── errors.go                                # sentinel errors
├── driver/
│   ├── driver.go                            # DBDriver interface + registry
│   ├── postgres/postgres.go
│   ├── mysql/mysql.go
│   └── sqlite/sqlite.go
├── cmd/
│   └── migrate/main.go                      # CLI binary (thin library wrapper)
├── internal/
│   └── testhelpers/                         # shared test fixtures + driver contract test
├── docs/
│   ├── README.md
│   └── superpowers/specs/
└── testdata/
    └── migrations/
```

## Public library API

```go
package migrate

// Migrator runs migrations against a database.
type Migrator struct { /* unexported */ }

// Config configures a Migrator.
type Config struct {
    // Source is a golang-migrate source URL: "file://./migrations",
    // "embed://", "github://owner/repo#path", etc.
    Source string

    // DriverName identifies a registered DB driver: "postgres",
    // "mysql", or "sqlite". Must be blank-imported by the caller.
    DriverName string

    // DB is an open *sql.DB. Caller owns the lifecycle.
    DB *sql.DB

    // HistoryTable name. Defaults to "schema_migrations".
    HistoryTable string

    // Logger is optional; defaults to no-op.
    Logger Logger
}

func New(cfg Config) (*Migrator, error)

// Up applies every pending migration in filename order as a new batch.
func (m *Migrator) Up(ctx context.Context) ([]AppliedMigration, error)

// Down rolls back the last `steps` batches (steps >= 1).
func (m *Migrator) Down(ctx context.Context, steps int) ([]AppliedMigration, error)

// Plan returns migrations Up would execute. Does not modify the DB.
func (m *Migrator) Plan(ctx context.Context) ([]PlannedMigration, error)

// PlanDown returns migrations Down(steps) would roll back. Does not modify the DB.
func (m *Migrator) PlanDown(ctx context.Context, steps int) ([]PlannedMigration, error)

// Status returns every on-disk migration paired with applied/pending state.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error)

// Close releases Migrator-owned resources. Does NOT close cfg.DB.
func (m *Migrator) Close() error

type AppliedMigration struct {
    Name      string
    Batch     int
    AppliedAt time.Time
}

type PlannedMigration struct {
    Name  string
    Path  string
    SQL   string
    Batch int
}

type MigrationStatus struct {
    Name      string
    Applied   bool
    Batch     int
    AppliedAt time.Time
}
```

**Contracts:**
- Caller owns `cfg.DB`. Library never opens or closes it.
- Every method takes `context.Context`.
- `Up` is all-or-partial at the migration level (each migration is atomic via transaction) but not at the batch level — if a mid-batch migration fails, earlier ones in the batch remain applied and are logged.
- `Down(ctx, steps)`: `steps < 1` returns `ErrInvalidSteps`. `steps` greater than the number of batches in history rolls back every batch (capped, not error).
- An on-disk file with only `.down.sql` and no corresponding `.up.sql` causes `New()` to return an error (cannot sort an orphan down-only file into the pending list coherently).
- The `Logger` interface is a minimal `Debugf / Infof / Warnf` trio; a no-op default is used when `cfg.Logger` is nil.

## History table schema

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
    id         BIGSERIAL PRIMARY KEY,
    name       VARCHAR(255) NOT NULL UNIQUE,
    batch      INTEGER NOT NULL,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_schema_migrations_batch ON schema_migrations(batch);
```

`id` uses per-dialect auto-increment (`BIGSERIAL` Postgres, `BIGINT AUTO_INCREMENT` MySQL, `INTEGER PRIMARY KEY` SQLite). Shape is identical across dialects.

**Operations:**

| Operation | SQL |
|---|---|
| Discover applied set | `SELECT name FROM schema_migrations` |
| Next batch number | `SELECT COALESCE(MAX(batch), 0) + 1 FROM schema_migrations` |
| Record an up | `INSERT INTO schema_migrations(name, batch) VALUES (?, ?)` |
| Last batch's migrations | `SELECT name FROM schema_migrations WHERE batch = (SELECT MAX(batch) FROM schema_migrations) ORDER BY id DESC` |
| Record a down | `DELETE FROM schema_migrations WHERE name = ?` |

**Transactional boundary:** each migration's SQL executes in the same transaction as its history `INSERT` / `DELETE`. If the migration fails, the transaction rolls back and the history row is never written — no drift.

**MySQL caveat:** MySQL auto-commits most DDL. A mid-migration failure can leave DDL partially applied with history un-updated. Documented: prefer one DDL statement per migration file on MySQL, or accept manual recovery.

## Lifecycle algorithms

### `Up(ctx)`

1. `applied := SELECT name FROM schema_migrations` (set).
2. `onDisk := source.List()` — files, strip `.up.sql`/`.down.sql`, lexical sort.
3. `pending := onDisk - applied`, preserving sorted order.
4. If `len(pending) == 0` → return `([], nil)`; log "nothing to do".
5. `batch := MAX(batch) + 1` (or `1` if empty).
6. For each `name` in `pending`:
   - `BEGIN`
   - Execute `<name>.up.sql`.
   - `INSERT INTO schema_migrations(name, batch) VALUES (name, batch)`.
   - `COMMIT`. On error: abort the run; earlier migrations in the batch remain applied; return the partial-success error.
7. Return `[]AppliedMigration`.

### `Down(ctx, steps)`

1. Compute `targetBatches` := highest `steps` batch numbers in history.
2. `rollback := SELECT name FROM schema_migrations WHERE batch IN targetBatches ORDER BY id DESC`.
3. For each `name` in `rollback`:
   - Locate `<name>.down.sql`. If missing → return `ErrNoRollback` (no rows removed).
   - `BEGIN` → execute `.down.sql` → `DELETE FROM schema_migrations WHERE name = ?` → `COMMIT`.
4. Return `[]AppliedMigration` (the rolled-back rows).

### `Plan(ctx)` / `PlanDown(ctx, steps)`

Same steps 1–5 (or 1–2 for Down) but: open no write transactions, read each file into memory, return a `[]PlannedMigration` with `Name`, `Path`, `SQL`, and the prospective `Batch`. `Plan` doubles as a pre-flight check.

### `Status(ctx)`

Left-join `onDisk` ∪ `applied`; return every migration with its applied state, batch, and `AppliedAt` (zero if pending).

## Concurrent-developer scenario (the core win)

| Event | Alice's DB | Bob's DB |
|---|---|---|
| Both branch from main at HEAD | (empty) | (empty) |
| Alice creates `20260416100000_add_users`, merges, deploys | batch 1: `add_users` | — |
| Bob's earlier-in-time `20260416090000_add_orders` merges after Alice, deploys | `pending = {20260416090000_add_orders}` → batch 2 applies it | fresh run → batch 1 applies both in order |

Every environment ends up with both migrations run exactly once. This is why the history-table approach exists.

## CLI surface

Binary: `migrate` via `go install github.com/artak/go-schema-migrate/cmd/migrate@latest`.

**Configuration** (flags > env > `migrate.yaml`):

| Flag | Env | Purpose |
|---|---|---|
| `--source` | `MIGRATE_SOURCE` | Source URL |
| `--database` | `MIGRATE_DATABASE` | Driver-prefixed DSN |
| `--history-table` | `MIGRATE_HISTORY_TABLE` | Default `schema_migrations` |
| `--config` | `MIGRATE_CONFIG` | `migrate.yaml` path |
| `--verbose` | `MIGRATE_VERBOSE` | Log SQL and timings |

Driver derives from DSN scheme (`postgres://`, `mysql://`, `sqlite://`). CLI blank-imports all three drivers.

**Commands:**

```
migrate up                       # apply all pending (new batch)
migrate up --dry-run             # show what would run
migrate up --dry-run --verbose   # also print SQL bodies

migrate down                     # rollback last batch (prompts [y/N])
migrate down --step 3            # rollback last 3 batches
migrate down --dry-run           # show what would be rolled back
migrate down --force             # skip prompt (for CI / scripts)
migrate down -f --step 2         # short form + combinable flags

migrate status                   # every migration + applied/pending + batch
migrate status --pending         # only pending
migrate status --json            # machine-readable

migrate create <name>            # scaffold new pair with current timestamp
                                 # → 20260416152310_<name>.up.sql / .down.sql

migrate version                  # CLI version + build info
```

**Exit codes:**

| Code | Meaning |
|---|---|
| 0 | Success (incl. dry-run and "nothing to do") |
| 1 | Generic failure (config, connection) |
| 2 | Migration file failed to execute |
| 3 | Confirmation declined or non-TTY without `--force` |

**Interactive safety:**
- `down` without `--force` shows the plan and prompts `Roll back N migrations? [y/N]`.
- Non-TTY runs without `--force` refuse to execute (exit 3).

**Intentionally absent:** `force <version>` (no dirty-version concept), `drop` (too dangerous by default).

## Testing strategy

Three layers, all under `go test ./...`.

### 1. Unit tests

- `plan_test.go` — diff logic against fake `applied` sets and on-disk lists. No DB, no filesystem.
- `migrator_test.go` — orchestration against a fake `driver.DBDriver`. Verifies batch numbering, rollback ordering, error propagation, dry-run never calls write methods.
- `source_test.go` — source-driver wrapping, filename parsing, timestamp validation.

### 2. Driver integration tests

- One subpackage per DB. Postgres and MySQL via `testcontainers-go`. SQLite uses `:memory:`.
- Shared contract: `internal/testhelpers/drivercontract.go` exposes `RunContract(t, drv driver.DBDriver)`. Every real driver must pass it.
- Named regression test: `TestOutOfOrderMerge_AppliesBothMigrations` — the concurrent-developer scenario, executed against each real driver.

### 3. End-to-end CLI tests

- `cmd/migrate/e2e_test.go` builds the binary, runs as subprocess against a real SQLite file, asserts stdout/stderr/exit-codes for every command path.
- Golden files for `status --json` and `--dry-run` output.

### Coverage targets

- 80%+ overall.
- 90%+ on `migrator.go` and `plan.go`.

### CI

- Matrix: Go 1.23 and Go 1.24.
- Separate job: docker-compose brings up Postgres + MySQL; driver tests run under `-tags=integration`.

## Implementation phasing

| Phase | Deliverable | Verification |
|---|---|---|
| 1 | Core engine + SQLite driver | Unit + SQLite integration including out-of-order regression test |
| 2 | Postgres driver | Driver contract passes against real Postgres container |
| 3 | MySQL driver | Driver contract passes; MySQL DDL caveat documented |
| 4 | CLI binary | E2E for every command incl. `--dry-run`, `--force`, non-TTY refusal |
| 5 | Docs + examples | README, godoc, example app using `embed://` source |

Each phase is independently shippable and testable.
