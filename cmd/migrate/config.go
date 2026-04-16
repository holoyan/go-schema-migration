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
// postgres:// → "postgres", sqlite:// → "sqlite".
// (MySQL support deferred.) Returns "" for unrecognized schemes.
func driverFromDSN(dsn string) string {
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return "postgres"
	case strings.HasPrefix(dsn, "sqlite://"), strings.HasPrefix(dsn, "sqlite3://"):
		return "sqlite"
	}
	return ""
}
