package main_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildBinary compiles cmd/gomigrate once per test invocation.
func buildBinary(t *testing.T) string {
	t.Helper()
	outDir := t.TempDir()
	bin := filepath.Join(outDir, "gomigrate")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, errOut.String())
	}
	return bin
}

func TestE2E_CLIUpStatusDown(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	mig := filepath.Join(dir, "migs")
	dbPath := filepath.Join(dir, "db.sqlite")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(mig, "20260101000000_a.up.sql"), []byte("CREATE TABLE a(id INTEGER);"), 0o644)
	_ = os.WriteFile(filepath.Join(mig, "20260101000000_a.down.sql"), []byte("DROP TABLE a;"), 0o644)

	run := func(args ...string) (string, int) {
		cmd := exec.Command(bin, args...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		_ = cmd.Run()
		return out.String(), cmd.ProcessState.ExitCode()
	}

	srcFlag := "--source=file://" + mig
	dbFlag := "--database=sqlite://" + dbPath

	out, code := run("up", srcFlag, dbFlag)
	if code != 0 || !strings.Contains(out, "20260101000000_a") {
		t.Fatalf("up failed (%d): %s", code, out)
	}

	out, code = run("status", srcFlag, dbFlag)
	if code != 0 || !strings.Contains(out, "applied") {
		t.Fatalf("status failed (%d): %s", code, out)
	}

	out, code = run("down", srcFlag, dbFlag, "--force")
	if code != 0 || !strings.Contains(out, "Rolled back") {
		t.Fatalf("down failed (%d): %s", code, out)
	}
}

func TestE2E_CLIVersion(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "version")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("version failed: %v", err)
	}
	if !strings.Contains(out.String(), "gomigrate ") {
		t.Fatalf("unexpected version output: %q", out.String())
	}
}
