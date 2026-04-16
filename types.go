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
