package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestCmdDown_DryRun(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})
	var u1, u2 bytes.Buffer
	if code := cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &u1, &u2); code != 0 {
		t.Fatalf("setup up failed: %s", u2.String())
	}
	var out, errB bytes.Buffer
	code := cmdDown([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath, "--dry-run"}, &out, &errB, nonInteractiveStdin{})
	if code != 0 {
		t.Fatalf("down --dry-run exited %d: %s", code, errB.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("dry-run")) {
		t.Fatalf("dry-run label missing: %q", out.String())
	}
}

func TestCmdDown_NonTTYWithoutForceRefuses(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})
	_ = cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &bytes.Buffer{}, &bytes.Buffer{})

	var out, errB bytes.Buffer
	code := cmdDown([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &out, &errB, nonInteractiveStdin{})
	if code != 3 {
		t.Fatalf("want exit 3 for non-TTY without --force, got %d", code)
	}
}

func TestCmdDown_ForceSkipsPrompt(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})
	_ = cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &bytes.Buffer{}, &bytes.Buffer{})

	var out, errB bytes.Buffer
	code := cmdDown([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath, "--force"}, &out, &errB, nonInteractiveStdin{})
	if code != 0 {
		t.Fatalf("--force should succeed, got exit %d: %s", code, errB.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("20260101000000_a")) {
		t.Fatalf("rollback output missing migration: %q", out.String())
	}
}

// nonInteractiveStdin is a stdin substitute that signals "not a TTY".
type nonInteractiveStdin struct{}

func (nonInteractiveStdin) IsTerminal() bool         { return false }
func (nonInteractiveStdin) Read([]byte) (int, error) { return 0, io.EOF }

func TestCmdStatus_JSON(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
		"20260102000000_b.up.sql":   "CREATE TABLE b(id INTEGER);",
		"20260102000000_b.down.sql": "DROP TABLE b;",
	})
	_ = cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &bytes.Buffer{}, &bytes.Buffer{})

	var out, errB bytes.Buffer
	code := cmdStatus([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath, "--json"}, &out, &errB)
	if code != 0 {
		t.Fatalf("status --json exited %d: %s", code, errB.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
}

func TestCmdStatus_PendingOnly(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})
	_ = cmdUp([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &bytes.Buffer{}, &bytes.Buffer{})
	writeMigrations(t, mig, map[string]string{
		"20260102000000_b.up.sql":   "CREATE TABLE b(id INTEGER);",
		"20260102000000_b.down.sql": "DROP TABLE b;",
	})

	var out, errB bytes.Buffer
	code := cmdStatus([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath, "--pending"}, &out, &errB)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, errB.String())
	}
	if bytes.Contains(out.Bytes(), []byte("20260101000000_a")) {
		t.Fatalf("--pending must not include applied migrations: %q", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("20260102000000_b")) {
		t.Fatalf("--pending must include pending migration: %q", out.String())
	}
}

func TestCmdCreate_WritesTimestampedPair(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}

	var out, errB bytes.Buffer
	code := cmdCreate([]string{"add_users"}, mig, &out, &errB, func() time.Time {
		return time.Date(2026, 4, 16, 15, 23, 10, 0, time.UTC)
	})
	if code != 0 {
		t.Fatalf("create exited %d: %s", code, errB.String())
	}
	for _, suffix := range []string{".up.sql", ".down.sql"} {
		p := filepath.Join(mig, "20260416152310_add_users"+suffix)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing file: %s", p)
		}
	}
}

func TestCmdCreate_InvalidName(t *testing.T) {
	dir := t.TempDir()
	var out, errB bytes.Buffer
	code := cmdCreate([]string{"Bad Name!"}, dir, &out, &errB, time.Now)
	if code == 0 {
		t.Fatal("invalid name should fail")
	}
	if !bytes.Contains(errB.Bytes(), []byte("invalid")) {
		t.Fatalf("error should mention invalid: %q", errB.String())
	}
}

func TestCmdBackfill_Applies(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
		"20260102000000_b.up.sql":   "CREATE TABLE b(id INTEGER);",
		"20260102000000_b.down.sql": "DROP TABLE b;",
	})

	var out, errBuf bytes.Buffer
	code := cmdBackfill([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("backfill exited %d: %s", code, errBuf.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("20260101000000_a")) {
		t.Fatalf("output missing first migration: %q", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("20260102000000_b")) {
		t.Fatalf("output missing second migration: %q", out.String())
	}
	// batch 1 for first migration, batch 2 for second
	if !bytes.Contains(out.Bytes(), []byte("batch 1")) {
		t.Fatalf("output missing batch 1: %q", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("batch 2")) {
		t.Fatalf("output missing batch 2: %q", out.String())
	}
	// Running again should report nothing to backfill.
	var out2, err2 bytes.Buffer
	code2 := cmdBackfill([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &out2, &err2)
	if code2 != 0 {
		t.Fatalf("second backfill exited %d: %s", code2, err2.String())
	}
	if !bytes.Contains(out2.Bytes(), []byte("Nothing to backfill")) {
		t.Fatalf("expected 'Nothing to backfill' on second run, got: %q", out2.String())
	}
}

func TestCmdBackfill_DryRun(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	writeMigrations(t, mig, map[string]string{
		"20260101000000_a.up.sql":   "CREATE TABLE a(id INTEGER);",
		"20260101000000_a.down.sql": "DROP TABLE a;",
	})

	var out, errBuf bytes.Buffer
	code := cmdBackfill([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath, "--dry-run"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("backfill --dry-run exited %d: %s", code, errBuf.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("dry-run")) {
		t.Fatalf("output missing dry-run label: %q", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("Would record")) {
		t.Fatalf("output missing 'Would record': %q", out.String())
	}

	// A real backfill after dry-run should still have work to do.
	var out2, err2 bytes.Buffer
	code2 := cmdBackfill([]string{"--source", "file://" + mig, "--database", "sqlite://" + dbPath}, &out2, &err2)
	if code2 != 0 {
		t.Fatalf("real backfill after dry-run exited %d: %s", code2, err2.String())
	}
	if !bytes.Contains(out2.Bytes(), []byte("20260101000000_a")) {
		t.Fatalf("real backfill should still report the migration: %q", out2.String())
	}
}

func TestCmdBackfill_CustomRegex(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	// golang-migrate-style filenames
	writeMigrations(t, mig, map[string]string{
		"000001_create_users.up.sql":   "CREATE TABLE users(id INTEGER);",
		"000001_create_users.down.sql": "DROP TABLE users;",
	})

	var out, errBuf bytes.Buffer
	code := cmdBackfill([]string{
		"--source", "file://" + mig,
		"--database", "sqlite://" + dbPath,
		"--filename-regex", `^(\d+_[a-zA-Z0-9_]+)\.(up|down)\.sql$`,
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("backfill --filename-regex exited %d: %s", code, errBuf.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("000001_create_users")) {
		t.Fatalf("output missing migration name: %q", out.String())
	}
}
