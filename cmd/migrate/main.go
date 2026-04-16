// Command migrate is the CLI wrapper over github.com/artak/go-schema-migrate.
package main

import (
	"fmt"
	"os"

	_ "github.com/artak/go-schema-migrate/driver/postgres"
	_ "github.com/artak/go-schema-migrate/driver/sqlite"
	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" sql driver
	_ "modernc.org/sqlite"             // registers "sqlite" sql driver
)

const usage = `migrate — schema migration tool with full history tracking

Usage:
  migrate <command> [flags]

Commands:
  up       Apply all pending migrations
  down     Roll back the last batch (or N batches with --step)
  status   Show applied/pending state of every migration
  create   Scaffold a new migration pair
  version  Print version info

Run 'migrate <command> --help' for command-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var code int
	switch cmd {
	case "up":
		code = runUp(args)
	case "down":
		code = runDown(args)
	case "status":
		code = runStatus(args)
	case "create":
		code = runCreate(args)
	case "version":
		code = runVersion(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		code = 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", cmd, usage)
		code = 1
	}
	os.Exit(code)
}

// Stub implementations; each gets filled in a later task.
func runUp(args []string) int      { return cmdUp(args, os.Stdout, os.Stderr) }
func runDown(args []string) int    { return cmdDown(args, os.Stdout, os.Stderr, realStdin{}) }
func runStatus(args []string) int  { return cmdStatus(args, os.Stdout, os.Stderr) }
func runCreate(args []string) int  { return notImplemented("create") }
func runVersion(args []string) int { return notImplemented("version") }

func notImplemented(cmd string) int {
	fmt.Fprintf(os.Stderr, "%s: not implemented in this build\n", cmd)
	return 1
}
