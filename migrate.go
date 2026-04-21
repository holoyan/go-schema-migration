// Package migrate provides a Laravel-style history-table database
// migration tool for Go.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/holoyan/go-schema-migration/driver"
)

// Config configures a Migrator.
type Config struct {
	Source       string
	DriverName   string
	DB           *sql.DB
	HistoryTable string
	Logger       Logger
	// FilenameRegex overrides the default filename pattern used to parse
	// migration files. If nil, DefaultFilenameRE is used. Must have two
	// capture groups: name and direction (up|down).
	FilenameRegex *regexp.Regexp
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
	re := cfg.FilenameRegex
	if re == nil {
		re = DefaultFilenameRE
	}
	src, err := loadSourceWithRegex(cfg.Source, re)
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

// DBForTest returns the *sql.DB wired into the Migrator. Exported for
// tests across packages in this module; not part of the public API.
func (m *Migrator) DBForTest() *sql.DB { return m.cfg.DB }

// loadSourceWithRegex resolves a source URL into sourceMigration list using a custom regex.
// Only "file://" is supported today. Pass DefaultFilenameRE for standard filename handling.
func loadSourceWithRegex(url string, re *regexp.Regexp) ([]sourceMigration, error) {
	const filePrefix = "file://"
	if strings.HasPrefix(url, filePrefix) {
		return loadFromDirWithRegex(strings.TrimPrefix(url, filePrefix), re)
	}
	return nil, fmt.Errorf("migrate: unsupported source scheme %q (only file:// is supported in v1)", url)
}
