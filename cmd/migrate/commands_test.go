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
