package migrate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

type direction string

const (
	dirUp   direction = "up"
	dirDown direction = "down"
)

// DefaultFilenameRE is the standard filename pattern:
// YYYYMMDDHHMMSS_snake_name.up.sql (or .down.sql).
var DefaultFilenameRE = regexp.MustCompile(`^(\d{14}_[a-z0-9_]+)\.(up|down)\.sql$`)

// filenameRE is an alias kept for internal use; external code should use DefaultFilenameRE.
var filenameRE = DefaultFilenameRE

// parseMigrationFilename splits a filename into its (name, direction)
// components using DefaultFilenameRE. Returns ErrInvalidMigrationName
// for any filename that does not match the required pattern.
func parseMigrationFilename(filename string) (name string, dir direction, err error) {
	return parseMigrationFilenameWithRegex(filename, DefaultFilenameRE)
}

// parseMigrationFilenameWithRegex extracts (name, direction) using a custom regex.
// The regex MUST have two capture groups: first = name, second = "up" or "down".
func parseMigrationFilenameWithRegex(filename string, re *regexp.Regexp) (name string, dir direction, err error) {
	m := re.FindStringSubmatch(filename)
	if m == nil || len(m) < 3 {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidMigrationName, filename)
	}
	d := direction(m[2])
	if d != dirUp && d != dirDown {
		return "", "", fmt.Errorf("%w: %q (second capture group must be 'up' or 'down')", ErrInvalidMigrationName, filename)
	}
	return m[1], d, nil
}

// sourceMigration is a loaded migration with both direction SQL bodies.
type sourceMigration struct {
	Name    string // e.g. "20260416143052_add_users"
	UpPath  string // absolute path of the .up.sql file (or fs-relative in tests)
	UpSQL   string
	DownSQL string // empty if no .down.sql exists or file is empty
	HasDown bool   // true when a .down.sql file was present (even if empty)
}

// loadFromFS reads every top-level file from fsys, parses migration
// filenames using DefaultFilenameRE, pairs up/down bodies, and returns
// the sorted list. Returns ErrInvalidMigrationName on any non-conforming
// filename, and ErrOrphanDownFile on a .down.sql without a matching .up.sql.
func loadFromFS(fsys fs.FS) ([]sourceMigration, error) {
	return loadFromFSWithRegex(fsys, DefaultFilenameRE)
}

// loadFromFSWithRegex reads fsys using a custom filename regex.
// The regex must have two capture groups: first = migration name, second = "up" or "down".
func loadFromFSWithRegex(fsys fs.FS, re *regexp.Regexp) ([]sourceMigration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("source: read dir: %w", err)
	}
	type half struct {
		path string
		data []byte
	}
	ups := map[string]half{}
	downs := map[string]half{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name, dir, perr := parseMigrationFilenameWithRegex(e.Name(), re)
		if perr != nil {
			return nil, perr
		}
		data, rerr := fs.ReadFile(fsys, e.Name())
		if rerr != nil {
			return nil, fmt.Errorf("source: read %q: %w", e.Name(), rerr)
		}
		h := half{path: e.Name(), data: data}
		switch dir {
		case dirUp:
			ups[name] = h
		case dirDown:
			downs[name] = h
		}
	}
	for name := range downs {
		if _, ok := ups[name]; !ok {
			return nil, fmt.Errorf("%w: %s.down.sql", ErrOrphanDownFile, name)
		}
	}
	out := make([]sourceMigration, 0, len(ups))
	for name, u := range ups {
		m := sourceMigration{Name: name, UpPath: u.path, UpSQL: string(u.data)}
		if d, ok := downs[name]; ok {
			m.HasDown = true
			m.DownSQL = string(d.data)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// loadFromDirWithRegex is the disk-path entry point used by New when the source
// URL is "file://...". Callers pass the resolved absolute path. Rewrites UpPath
// to an absolute path for better error messages. Pass DefaultFilenameRE for
// standard filename handling.
func loadFromDirWithRegex(dir string, re *regexp.Regexp) ([]sourceMigration, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	got, err := loadFromFSWithRegex(os.DirFS(abs), re)
	if err != nil {
		return nil, err
	}
	for i := range got {
		got[i].UpPath = filepath.Join(abs, got[i].UpPath)
	}
	return got, nil
}
