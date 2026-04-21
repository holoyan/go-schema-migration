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
	// subpackage (e.g. _ "github.com/holoyan/go-schema-migration/driver/postgres").
	ErrDriverNotRegistered = errors.New("migrate: driver not registered")

	// ErrInvalidMigrationName is returned by New when a file in the
	// source does not match the required ^\d{14}_[a-z0-9_]+\.(up|down)\.sql$ pattern.
	ErrInvalidMigrationName = errors.New("migrate: invalid migration filename")

	// ErrOrphanDownFile is returned by New when a .down.sql file has
	// no corresponding .up.sql file.
	ErrOrphanDownFile = errors.New("migrate: .down.sql file has no matching .up.sql")
)
