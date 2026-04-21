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
