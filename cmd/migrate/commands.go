package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	migrate "github.com/artak/go-schema-migrate"
	"golang.org/x/term"
)

// cmdUp implements `migrate up`. Writes human output to stdout and
// errors to stderr. Returns the process exit code.
func cmdUp(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "show what would run without executing")
	verbose := fs.Bool("verbose", false, "include SQL bodies in dry-run output")
	source := fs.String("source", "", "source URL")
	database := fs.String("database", "", "DSN")
	historyTable := fs.String("history-table", "", "history table name")
	configPath := fs.String("config", "", "config file path")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	shared := []string{}
	if *source != "" {
		shared = append(shared, "--source", *source)
	}
	if *database != "" {
		shared = append(shared, "--database", *database)
	}
	if *historyTable != "" {
		shared = append(shared, "--history-table", *historyTable)
	}
	if *configPath != "" {
		shared = append(shared, "--config", *configPath)
	}

	cfg, err := resolveConfig(shared)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	m, db, err := openMigrator(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	if *dryRun {
		plan, err := m.Plan(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return printPlan(stdout, plan, "Batch %d (dry-run, nothing applied):", *verbose)
	}

	applied, err := m.Up(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(applied) == 0 {
		fmt.Fprintln(stdout, "Nothing to migrate.")
		return 0
	}
	fmt.Fprintf(stdout, "Applied %d migration(s) in batch %d:\n", len(applied), applied[0].Batch)
	for _, a := range applied {
		fmt.Fprintf(stdout, "  ✓ %s\n", a.Name)
	}
	return 0
}

func printPlan(w io.Writer, plan []migrate.PlannedMigration, header string, verbose bool) int {
	if len(plan) == 0 {
		fmt.Fprintln(w, "Nothing to migrate.")
		return 0
	}
	fmt.Fprintf(w, header+"\n", plan[0].Batch)
	for _, p := range plan {
		fmt.Fprintf(w, "  → %s    %s    (%d bytes)\n", p.Name, p.Path, len(p.SQL))
		if verbose {
			fmt.Fprintln(w, indent(p.SQL, "      "))
		}
	}
	return 0
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

// openMigrator builds a *migrate.Migrator from resolvedConfig,
// returning the underlying *sql.DB so the caller can close it.
func openMigrator(cfg resolvedConfig) (*migrate.Migrator, *sql.DB, error) {
	if cfg.Source == "" {
		return nil, nil, fmt.Errorf("--source is required")
	}
	if cfg.Database == "" {
		return nil, nil, fmt.Errorf("--database is required")
	}
	if cfg.Driver == "" {
		return nil, nil, fmt.Errorf("cannot infer driver from DSN: %s", cfg.Database)
	}

	sqlName, dsn, err := toSQLOpen(cfg.Driver, cfg.Database)
	if err != nil {
		return nil, nil, err
	}

	db, err := sql.Open(sqlName, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}

	m, err := migrate.New(migrate.Config{
		Source:       cfg.Source,
		DriverName:   cfg.Driver,
		DB:           db,
		HistoryTable: cfg.HistoryTable,
	})
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return m, db, nil
}

// toSQLOpen maps internal driver name + user DSN into the (driverName, dsn)
// pair that database/sql.Open actually expects.
//
//   postgres: sql driver name is "pgx"; pgx accepts postgres:// URLs as-is.
//   sqlite:   sql driver name is "sqlite"; modernc wants a file path, not a URL.
func toSQLOpen(ourDriver, userDSN string) (string, string, error) {
	switch ourDriver {
	case "postgres":
		return "pgx", userDSN, nil
	case "sqlite":
		filePath := strings.TrimPrefix(strings.TrimPrefix(userDSN, "sqlite3://"), "sqlite://")
		return "sqlite", filePath, nil
	}
	return "", "", fmt.Errorf("unknown driver %q", ourDriver)
}

// terminalDetector lets tests inject a fake stdin.
type terminalDetector interface {
	IsTerminal() bool
	Read([]byte) (int, error)
}

// realStdin adapts os.Stdin for use as terminalDetector in production.
type realStdin struct{}

func (realStdin) IsTerminal() bool           { return term.IsTerminal(int(os.Stdin.Fd())) }
func (realStdin) Read(p []byte) (int, error) { return os.Stdin.Read(p) }

func cmdDown(args []string, stdout, stderr io.Writer, in terminalDetector) int {
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	fs.SetOutput(stderr)
	step := fs.Int("step", 1, "number of batches to roll back")
	dryRun := fs.Bool("dry-run", false, "show what would roll back")
	force := fs.Bool("force", false, "skip interactive confirmation")
	forceShort := fs.Bool("f", false, "alias for --force")
	source := fs.String("source", "", "source URL")
	database := fs.String("database", "", "DSN")
	historyTable := fs.String("history-table", "", "history table name")
	configPath := fs.String("config", "", "config file path")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*force = *force || *forceShort

	shared := []string{}
	if *source != "" {
		shared = append(shared, "--source", *source)
	}
	if *database != "" {
		shared = append(shared, "--database", *database)
	}
	if *historyTable != "" {
		shared = append(shared, "--history-table", *historyTable)
	}
	if *configPath != "" {
		shared = append(shared, "--config", *configPath)
	}
	cfg, err := resolveConfig(shared)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	m, db, err := openMigrator(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	plan, err := m.PlanDown(ctx, *step)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(plan) == 0 {
		fmt.Fprintln(stdout, "Nothing to roll back.")
		return 0
	}
	if *dryRun {
		return printPlan(stdout, plan, "Rolling back %d migration(s) (dry-run, nothing changed):", false)
	}
	if !*force {
		if !in.IsTerminal() {
			fmt.Fprintln(stderr, "down: refusing to run interactively from non-TTY — pass --force to confirm")
			return 3
		}
		fmt.Fprintf(stdout, "About to roll back %d migration(s):\n", len(plan))
		for _, p := range plan {
			fmt.Fprintf(stdout, "  - %s\n", p.Name)
		}
		fmt.Fprint(stdout, "Continue? [y/N] ")
		buf := make([]byte, 16)
		n, _ := in.Read(buf)
		resp := strings.TrimSpace(strings.ToLower(string(buf[:n])))
		if resp != "y" && resp != "yes" {
			fmt.Fprintln(stdout, "Aborted.")
			return 3
		}
	}
	rolled, err := m.Down(ctx, *step)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintf(stdout, "Rolled back %d migration(s):\n", len(rolled))
	for _, r := range rolled {
		fmt.Fprintf(stdout, "  ✓ %s (was batch %d)\n", r.Name, r.Batch)
	}
	return 0
}
