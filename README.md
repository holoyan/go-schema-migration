# go-schema-migration

A Go migration library and CLI with **full history tracking**, so concurrent-developer migrations apply exactly once per environment regardless of merge order.

- **Library** — a small, dependency-light Go API (`Up`, `Down`, `Plan`, `Status`, `Backfill`).
- **CLI** — `gomigrate` binary for interactive use, CI pipelines, and Docker images.
- **Databases** — PostgreSQL, MySQL/MariaDB, SQLite.
- **Go 1.23+.**

---

## Table of contents

- [Install](#install)
- [Quick start](#quick-start)
- [Filename convention](#filename-convention)
- [CLI reference](#cli-reference)
  - [`create`](#create--scaffold-a-new-migration-pair)
  - [`up`](#up--apply-all-pending-migrations)
  - [`down`](#down--roll-back-the-last-batch)
  - [`status`](#status--show-applied--pending)
  - [`backfill`](#backfill--register-existing-migrations-without-executing)
  - [`version`](#version)
- [Configuration](#configuration)
- [Library usage](#library-usage)
- [Migrating from another tool (e.g. golang-migrate)](#migrating-from-another-tool-eg-golang-migrate)
- [Supported databases](#supported-databases)
- [How the history table works](#how-the-history-table-works)
- [Non-goals](#non-goals)

---

## Install

```bash
# Library
go get github.com/holoyan/go-schema-migration

# CLI
go install github.com/holoyan/go-schema-migration/cmd/gomigrate@latest
```

Minimum Go version: **1.23**.

Verify the CLI is on your `PATH`:

```bash
gomigrate version
# → migrate dev
```

---

## Quick start

60-second tour against SQLite (no setup required):

```bash
# 1. Scaffold a migrations directory with a new pair
mkdir -p migrations
gomigrate create add_users --source file://./migrations
# → migrations/20260421120000_add_users.up.sql
#   migrations/20260421120000_add_users.down.sql
```

Edit the two files so the `up` creates the table and the `down` drops it:

```sql
-- migrations/20260421120000_add_users.up.sql
CREATE TABLE users (
    id    INTEGER PRIMARY KEY,
    email TEXT NOT NULL UNIQUE
);
```

```sql
-- migrations/20260421120000_add_users.down.sql
DROP TABLE users;
```

Then apply, inspect, roll back:

```bash
# 2. Apply
gomigrate up \
  --source file://./migrations \
  --database "sqlite:///tmp/app.db"
# → Applied 1 migration(s) in batch 1:
#     ✓ 20260421120000_add_users

# 3. Inspect
gomigrate status \
  --source file://./migrations \
  --database "sqlite:///tmp/app.db"
# → NAME                              STATE     BATCH  APPLIED_AT
#   20260421120000_add_users          applied   1      2026-04-21 12:00:15

# 4. Roll back the last batch
gomigrate down \
  --source file://./migrations \
  --database "sqlite:///tmp/app.db" \
  --force
# → Rolled back 1 migration(s):
#     ✓ 20260421120000_add_users (was batch 1)
```

---

## Filename convention

```
<14-digit timestamp>_<snake_case_name>.up.sql
<14-digit timestamp>_<snake_case_name>.down.sql
```

Example: `20260416143052_add_users.up.sql`

The 14-digit timestamp (`YYYYMMDDHHMMSS`) gives second-level resolution — enough to prevent two developers from picking the same prefix in practice. Files are processed in **lexical (= chronological) order**.

The regex enforced at load time:

```regex
^(\d{14}_[a-z0-9_]+)\.(up|down)\.sql$
```

*(Non-conforming filenames are rejected. For importing existing migrations in a different scheme — e.g. `000001_init.up.sql` — use [`backfill --filename-regex`](#backfill--register-existing-migrations-without-executing).)*

A `.up.sql` with no matching `.down.sql` is allowed (the migration is non-reversible). An orphan `.down.sql` without an `.up.sql` is an error.

---

## CLI reference

Every command takes `--source`, `--database`, and optionally `--history-table`, `--config`, `--verbose`. See [Configuration](#configuration) for env vars and YAML.

### `create` — scaffold a new migration pair

```bash
gomigrate create <name> [--source file://./migrations]
```

- `<name>` must match `^[a-z0-9_]+$`.
- Creates `NNN_<name>.up.sql` and `NNN_<name>.down.sql` with the current UTC timestamp.
- `--source` defaults to `./migrations`.

```bash
gomigrate create add_email_index
# → Created:
#     migrations/20260421143052_add_email_index.up.sql
#     migrations/20260421143052_add_email_index.down.sql
```

### `up` — apply all pending migrations

```bash
gomigrate up \
  --source file://./migrations \
  --database "postgres://user:pw@host/db" \
  [--dry-run] [--verbose]
```

Applies every pending migration in one new batch (higher than the current max batch number).

```bash
# Preview without executing
gomigrate up --source ... --database ... --dry-run

# Dry-run with full SQL dumped
gomigrate up --source ... --database ... --dry-run --verbose
```

### `down` — roll back the last batch

```bash
gomigrate down \
  --source file://./migrations \
  --database "..." \
  [--step N] [--force|-f] [--dry-run]
```

- `--step N` — roll back the last `N` batches (default `1`).
- `--force` / `-f` — skip the `[y/N]` confirmation prompt (required in CI/non-TTY).
- `--dry-run` — preview without touching the DB.

```bash
# Roll back the last batch, interactively
gomigrate down --source ... --database ...

# Roll back 3 batches in CI
gomigrate down --source ... --database ... --step 3 --force

# Preview what would roll back
gomigrate down --source ... --database ... --dry-run
```

### `status` — show applied / pending

```bash
gomigrate status \
  --source file://./migrations \
  --database "..." \
  [--pending] [--json]
```

Default output:

```
NAME                              STATE     BATCH  APPLIED_AT
20260101000000_create_users       applied   1      2026-01-01 00:00:05
20260102000000_add_orders         applied   1      2026-01-01 00:00:05
20260201000000_add_email_index    pending   -      -
```

- `--pending` — show only migrations that are not yet applied.
- `--json` — machine-readable output (stable schema; useful for CI dashboards).

```bash
gomigrate status --source ... --database ... --json | jq '.[] | select(.Applied==false)'
```

### `backfill` — register existing migrations without executing

See the dedicated [Migrating from another tool](#migrating-from-another-tool-eg-golang-migrate) section for the full story. In short:

```bash
gomigrate backfill \
  --source file://./migrations \
  --database "..." \
  [--batch N] [--filename-regex PATTERN] [--dry-run]
```

Records every pending migration file into the history table **without executing its SQL**. For users whose schema is already applied (by another tool or a previous deploy) and who want `gomigrate` to start tracking.

### `version`

```bash
gomigrate version
# → migrate dev
```

Overridable at build time: `go build -ldflags "-X main.Version=v0.1.0" ./cmd/gomigrate`.

---

## Configuration

Every flag has an environment-variable fallback and can also come from a YAML file.

**Precedence:** flag > env > YAML > default.

| Flag | Env | YAML key |
|---|---|---|
| `--source` | `MIGRATE_SOURCE` | `source` |
| `--database` | `MIGRATE_DATABASE` | `database` |
| `--history-table` | `MIGRATE_HISTORY_TABLE` | `history_table` |
| `--verbose` | `MIGRATE_VERBOSE` | `verbose` |
| `--config` | `MIGRATE_CONFIG` | — |

### Custom history table

Default is `schema_migrations`. Override with `--history-table`:

```bash
gomigrate up --history-table my_app_migrations \
  --source file://./migrations \
  --database "..."
```

Useful if you share a DB with another migration tool, or maintain separate migration pipelines per microservice in the same schema.

### YAML config

```yaml
# migrate.yaml
source: file://./migrations
database: postgres://user:pw@localhost:5432/myapp?sslmode=disable
history_table: schema_migrations
verbose: false
```

```bash
gomigrate up --config ./migrate.yaml
# Env var also works:
MIGRATE_CONFIG=./migrate.yaml gomigrate up
```

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Success (incl. dry-run and "nothing to do") |
| `1` | Generic failure (config, connection, bad args) |
| `2` | A migration failed to execute |
| `3` | Confirmation declined, or non-TTY without `--force` |

### Non-TTY safety

`gomigrate down` without `--force` refuses to run when stdin is not a terminal — prevents accidents in scripts where no one can answer `[y/N]`.

---

## Library usage

### Minimal example

```go
package main

import (
    "context"
    "database/sql"
    "log"

    migrate "github.com/holoyan/go-schema-migration"
    _ "github.com/holoyan/go-schema-migration/driver/postgres"
    _ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
    db, err := sql.Open("pgx", "postgres://user:pw@localhost:5432/app?sslmode=disable")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    m, err := migrate.New(migrate.Config{
        Source:     "file://./migrations",
        DriverName: "postgres",
        DB:         db,
    })
    if err != nil {
        log.Fatal(err)
    }
    defer m.Close()

    applied, err := m.Up(context.Background())
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("applied %d migration(s)", len(applied))
}
```

### Example: status dashboard endpoint

```go
func migrationsStatusHandler(m *migrate.Migrator) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        rows, err := m.Status(r.Context())
        if err != nil {
            http.Error(w, err.Error(), 500)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(rows)
    }
}
```

### Example: dry-run on boot, fail loudly if drift

```go
// Fail startup if migrations are out of date, don't auto-apply in prod.
plan, err := m.Plan(ctx)
if err != nil {
    log.Fatal(err)
}
if len(plan) > 0 {
    log.Fatalf("%d pending migration(s); run 'gomigrate up' before starting", len(plan))
}
```

### Example: apply on startup in dev

```go
if env == "development" {
    if _, err := m.Up(ctx); err != nil {
        log.Fatal(err)
    }
}
```

### Public API

```go
func New(cfg Config) (*Migrator, error)

func (m *Migrator) Up(ctx context.Context) ([]AppliedMigration, error)
func (m *Migrator) Down(ctx context.Context, steps int) ([]AppliedMigration, error)
func (m *Migrator) Plan(ctx context.Context) ([]PlannedMigration, error)
func (m *Migrator) PlanDown(ctx context.Context, steps int) ([]PlannedMigration, error)
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error)
func (m *Migrator) Backfill(ctx context.Context, opts BackfillOptions) ([]AppliedMigration, error)
func (m *Migrator) Close() error
```

```go
type Config struct {
    Source        string           // "file://./migrations"
    DriverName    string           // "postgres" | "mysql" | "sqlite"
    DB            *sql.DB          // caller owns lifecycle
    HistoryTable  string           // default: "schema_migrations"
    Logger        Logger           // optional; no-op by default
    FilenameRegex *regexp.Regexp   // optional; default enforces YYYYMMDDHHMMSS_name.up|down.sql
}

type BackfillOptions struct {
    Batch         int              // 0 = each migration gets its own incrementing batch
    FilenameRegex *regexp.Regexp   // optional per-call override
    DryRun        bool
}
```

### Guarantees

- **Caller owns `cfg.DB`.** The library never opens or closes it.
- **Every method takes `context.Context`** — cancellation and timeouts are honored.
- **Atomicity.** Each migration runs in its own transaction that also contains the history `INSERT` / `DELETE`. If the SQL fails, the transaction rolls back and no history row is written — no drift.
  *(MySQL caveat: DDL auto-commits. See [Supported databases](#supported-databases).)*
- **`Up` is all-or-partial-at-migration-level.** All pending migrations are applied as one batch. If a mid-batch migration fails, earlier ones stay applied; the error is returned and the batch is partial (logged).
- **`Down(0)` / `Down(-1)` → `ErrInvalidSteps`.** `Down(N)` with `N > batches` caps silently (rolls back everything).
- **`Plan` never touches the DB.** Uses only read queries.

---

## Migrating from another tool (e.g. golang-migrate)

If your database schema was managed by a different migration tool, you don't need to drop and reapply everything. Use `backfill` to register the existing migration files in `gomigrate`'s history table **without executing their SQL** — then continue normal operation from there.

### Typical workflow

Suppose you're on `golang-migrate`, your DB already has the schema, and you want to switch:

```bash
# 1. You already have migration files in ./migrations (from golang-migrate)
ls migrations/
# → 000001_init.up.sql  000001_init.down.sql
#   000002_users.up.sql 000002_users.down.sql
#   ...
```

**Option A — rename to match gomigrate's convention** (recommended: timestamps, not sequential numbers):

```bash
git mv migrations/000001_init.up.sql       migrations/20260101000000_init.up.sql
git mv migrations/000001_init.down.sql     migrations/20260101000000_init.down.sql
# ... etc

# Then just run backfill (uses default regex)
gomigrate backfill --source file://./migrations --database "..."
```

**Option B — keep the original filenames** (use `--filename-regex` to match `golang-migrate`'s naming):

```bash
gomigrate backfill \
  --source file://./migrations \
  --database "..." \
  --filename-regex '^(\d+_[a-zA-Z0-9_-]+)\.(up|down)\.sql$'
```

The regex must have **two capture groups**: the first becomes the migration name recorded in history, the second must be `up` or `down`.

### Batch assignment

Default: each file gets its own batch (1, 2, 3, …). This means `gomigrate down` rolls back one migration at a time, giving you fine-grained rollback control.

```bash
gomigrate backfill --source file://./migrations --database "..."
# → Backfilling history table (batch: auto-incrementing):
#     ✓ 20260101000000_init    → batch 1
#     ✓ 20260102000000_users   → batch 2
#     ✓ 20260201000000_orders  → batch 3
#   Recorded 3 migration(s).
```

Alternative: assign everything to a single batch with `--batch`:

```bash
gomigrate backfill --source ... --database ... --batch 1
# → all migrations land in batch 1 together; gomigrate down would roll them ALL back at once.
```

### Preview before writing

Always sanity-check with `--dry-run` first:

```bash
gomigrate backfill --source ... --database ... --dry-run
# → Would record 3 migration(s) (dry-run, nothing written):
#     20260101000000_init    → batch 1
#     20260102000000_users   → batch 2
#     20260201000000_orders  → batch 3
```

### Idempotency

Safe to re-run. Already-registered migrations are skipped silently:

```bash
gomigrate backfill --source ... --database ...
# → Nothing to backfill — all files already in history.
```

### After backfill

Delete the old tool's migration tracking (e.g. drop `schema_migrations` if it belonged to golang-migrate and you used a different table name for gomigrate, or just leave it). From now on, `gomigrate up`, `gomigrate down`, `gomigrate status`, `gomigrate create` all work as usual — only **new** migrations you haven't backfilled will appear as pending.

---

## Supported databases

| Driver | Package | DSN scheme | sql.DriverName | Driver library |
|---|---|---|---|---|
| **PostgreSQL** | `driver/postgres` | `postgres://` | `pgx` | `jackc/pgx/v5/stdlib` |
| **MySQL / MariaDB** | `driver/mysql` | `mysql://` | `mysql` | `go-sql-driver/mysql` |
| **SQLite** | `driver/sqlite` | `sqlite://` | `sqlite` | `modernc.org/sqlite` (pure-Go, no CGo) |

Register a driver by blank-importing it:

```go
import (
    _ "github.com/holoyan/go-schema-migration/driver/postgres"
    _ "github.com/holoyan/go-schema-migration/driver/mysql"
    _ "github.com/holoyan/go-schema-migration/driver/sqlite"
)
```

The CLI already blank-imports all three.

### DSN examples

| Database | DSN |
|---|---|
| PostgreSQL | `postgres://user:pw@localhost:5432/mydb?sslmode=disable` |
| MySQL | `mysql://user:pw@localhost:3306/mydb` *(CLI converts to go-sql-driver's native form and appends `parseTime=true`)* |
| SQLite | `sqlite:///absolute/path/to.db` or `sqlite://./relative.db` |

### MySQL caveat

MySQL auto-commits most DDL. If a migration file contains a DDL statement followed by something that fails, the DDL stays committed but the history row is **not** written. Recovery options: rewrite the migration to be idempotent, or fix state manually. **Prefer one DDL statement per migration file** to avoid this.

---

## How the history table works

A single table (default name `schema_migrations`) tracks every applied migration:

```sql
CREATE TABLE schema_migrations (
    id         BIGSERIAL PRIMARY KEY,          -- per-dialect type
    name       VARCHAR(255) NOT NULL UNIQUE,   -- e.g. "20260416143052_add_users"
    batch      INTEGER NOT NULL,               -- 1, 2, 3, ...
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_schema_migrations_batch ON schema_migrations(batch);
```

**`Up(ctx)`:**
1. `SELECT name FROM schema_migrations` → applied set.
2. List source files → on-disk set.
3. `pending = on_disk - applied`, sorted by filename.
4. Assign `batch = MAX(batch) + 1` (or `1` if empty).
5. For each pending, in order: `BEGIN → exec migration SQL → INSERT history row → COMMIT`.

**`Down(ctx, steps)`:**
1. Find rows with the highest `steps` batch numbers.
2. For each (newest-first by id): `BEGIN → exec .down.sql → DELETE history row → COMMIT`.

**`Backfill(ctx, opts)`:**
1. Same diff step as Up (`pending = on_disk - applied`).
2. For each pending: `INSERT history row` — **no SQL execution, no transaction**.

### The concurrent-developer scenario

| Event | Alice's DB | Bob's DB |
|---|---|---|
| Both branch from `main` at HEAD | *(empty)* | *(empty)* |
| Alice creates `20260416100000_add_users`, merges, deploys | batch 1: `add_users` | — |
| Bob's **earlier-timestamped** `20260416090000_add_orders` merges after Alice, deploys | `pending = {add_orders}` → batch 2 | fresh run → batch 1 applies both in order |

Both environments end up with both migrations applied exactly once. This is the library's core guarantee — `golang-migrate`'s single-version tracking would silently skip Bob's migration on Alice's DB.

---

## Non-goals

Out of scope for v1 (intentional — keeps the surface small):

- Checksum / drift detection on applied migrations. Trust your team.
- Migration files written in Go code. SQL only.
- Distributed locking / concurrent-runner safety. Serialize via your deploy orchestrator.
- Partial `Up` (goto, step count). `Up` always applies all pending.
- `force <version>` / `drop` commands.
- Source schemes other than `file://` (no `embed://` / `github://` / `s3://`).

---

## Design

Full design rationale, trade-offs, and architectural decisions: [`docs/superpowers/specs/2026-04-16-go-schema-migrate-design.md`](docs/superpowers/specs/2026-04-16-go-schema-migrate-design.md).

## License

MIT.
