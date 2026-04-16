# go-schema-migrate

A Go migration library and CLI with **full history tracking**, so concurrent-developer migrations apply exactly once per environment regardless of merge order.

## Install

```bash
# Library
go get github.com/artak/go-schema-migrate

# CLI
go install github.com/artak/go-schema-migrate/cmd/migrate@latest
```

Go 1.23+ required (1.25+ recommended since the SQLite driver pulls `modernc.org/sqlite` which declares a 1.25 toolchain).

## Filename convention

```
<14-digit timestamp>_<snake_case_name>.up.sql
<14-digit timestamp>_<snake_case_name>.down.sql
```

Example: `20260416143052_add_users.up.sql`

The 14-digit timestamp (`YYYYMMDDHHMMSS`) gives second-level resolution — enough to prevent two developers from picking the same prefix in practice. Files are processed in lexical (= chronological) order. The regex `^\d{14}_[a-z0-9_]+\.(up|down)\.sql$` is enforced at load time.

## CLI usage

```bash
# Apply all pending migrations (creates a new batch)
migrate up --source file://./migrations --database postgres://user:pw@host/db

# Preview what would run, without executing anything
migrate up --dry-run --verbose

# Roll back the last batch (prompts [y/N] on a TTY)
migrate down

# Roll back the last 3 batches, skip the prompt (for CI)
migrate down --step 3 --force

# Show every migration + applied/pending + batch
migrate status
migrate status --pending
migrate status --json

# Scaffold a new migration pair with the current UTC timestamp
migrate create add_users
# → 20260416152310_add_users.up.sql + .down.sql

migrate version
migrate --help
```

### Configuration

Every flag has an environment-variable fallback and can also come from a YAML file. Precedence: **flag > env > YAML > default**.

| Flag | Env | YAML key |
|---|---|---|
| `--source` | `MIGRATE_SOURCE` | `source` |
| `--database` | `MIGRATE_DATABASE` | `database` |
| `--history-table` | `MIGRATE_HISTORY_TABLE` | `history_table` |
| `--verbose` | `MIGRATE_VERBOSE` | `verbose` |
| `--config` | `MIGRATE_CONFIG` | — |

Example `migrate.yaml`:
```yaml
source: file://./migrations
database: postgres://user:pw@localhost:5432/myapp?sslmode=disable
history_table: schema_migrations
verbose: false
```

Run with `migrate up --config ./migrate.yaml`.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Success (including dry-run and "nothing to do") |
| `1` | Generic failure (config, connection, SQL parse) |
| `2` | Migration file failed to execute |
| `3` | Confirmation declined, or non-TTY without `--force` |

### Non-TTY safety

`migrate down` without `--force` refuses to run when stdin is not a terminal — prevents accidents in scripts or CI where no one can answer the `[y/N]` prompt.

## Library usage

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

### Public API

```go
func New(cfg Config) (*Migrator, error)

func (m *Migrator) Up(ctx context.Context) ([]AppliedMigration, error)
func (m *Migrator) Down(ctx context.Context, steps int) ([]AppliedMigration, error)
func (m *Migrator) Plan(ctx context.Context) ([]PlannedMigration, error)       // dry-run preview
func (m *Migrator) PlanDown(ctx context.Context, steps int) ([]PlannedMigration, error)
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error)
func (m *Migrator) Close() error
```

**Guarantees:**
- Caller owns `cfg.DB`. The library never opens or closes it.
- Every method takes `context.Context`.
- Each migration runs in its own transaction that also contains the history `INSERT`/`DELETE` — if the SQL fails, the transaction rolls back and no history row is written (no drift).
- `Up` applies every pending migration as one batch. `Down(steps)` rolls back the most recent `steps` batches.
- `Down(0)` / `Down(-1)` → returns `ErrInvalidSteps`. `Down(N)` when `N > batches` caps silently.

## Supported databases

| Driver | Package | DSN scheme | Registered sql.DriverName |
|---|---|---|---|
| **PostgreSQL** | `driver/postgres` | `postgres://` | `pgx` (via `jackc/pgx/v5/stdlib`) |
| **MySQL / MariaDB** | `driver/mysql` | `mysql://` | `mysql` (via `go-sql-driver/mysql`) |
| **SQLite** | `driver/sqlite` | `sqlite://` | `sqlite` (via `modernc.org/sqlite`, pure-Go) |

Register a driver by blank-importing it:

```go
import _ "github.com/artak/go-schema-migrate/driver/postgres"
import _ "github.com/artak/go-schema-migrate/driver/mysql"
import _ "github.com/artak/go-schema-migrate/driver/sqlite"
```

The CLI already blank-imports all three.

### DSN examples

| Database | DSN |
|---|---|
| PostgreSQL | `postgres://user:pw@localhost:5432/mydb?sslmode=disable` |
| MySQL | `mysql://user:pw@localhost:3306/mydb` *(CLI converts to go-sql-driver's native form)* |
| SQLite | `sqlite:///absolute/path/to.db` or `sqlite://./relative.db` |

### MySQL caveat

MySQL auto-commits most DDL. If a migration file contains a DDL statement followed by something that fails, the DDL stays committed but the history row is **not** written. Recovery options: rewrite the migration to be idempotent, or fix state manually. Prefer **one DDL statement per migration file** to avoid this.

## How the history table works

A single table (default name `schema_migrations`) tracks every applied migration:

```sql
CREATE TABLE schema_migrations (
    id         BIGSERIAL PRIMARY KEY,          -- per-dialect type
    name       VARCHAR(255) NOT NULL UNIQUE,   -- "20260416143052_add_users"
    batch      INTEGER NOT NULL,               -- 1, 2, 3, ...
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

On `Up(ctx)`:
1. Read `SELECT name FROM schema_migrations` → applied set.
2. List files in source → on-disk set.
3. `pending = on_disk - applied`, sorted by filename.
4. Pick `batch = MAX(batch) + 1` (or `1` if empty).
5. For each pending migration, in order: `BEGIN → exec migration SQL → INSERT history row → COMMIT`.

On `Down(ctx, steps)`:
1. Find rows with the highest `steps` batch numbers.
2. For each (newest-first by id), execute `.down.sql` and delete the history row in one transaction.

### The concurrent-developer scenario — walked through

| Event | Alice's DB | Bob's DB |
|---|---|---|
| Both branch from main at HEAD | *(empty)* | *(empty)* |
| Alice creates `20260416100000_add_users`, merges, deploys | batch 1: `add_users` | — |
| Bob's earlier-timestamped `20260416090000_add_orders` merges after Alice, deploys | `pending = {add_orders}` → batch 2 | fresh run → batch 1 applies both in order |

Both environments end up with both migrations applied exactly once. This is the library's core guarantee.

## Non-goals

Out of scope for v1 (intentional — keeps the surface small):

- Checksum / drift detection on applied migrations. Trust your team.
- Migration files written in Go code. SQL only.
- Distributed locking / concurrent-runner safety. Serialize via your deploy orchestrator.
- Partial `Up` (goto, step count). `Up` always applies all pending.
- `force <version>` / `drop` commands.
- Source schemes other than `file://` (no `embed://` / `github://` / `s3://`).

## Design

Full design rationale and trade-offs: [`docs/superpowers/specs/2026-04-16-go-schema-migrate-design.md`](docs/superpowers/specs/2026-04-16-go-schema-migrate-design.md).

## License

MIT.
