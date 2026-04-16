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

// filenameRE matches migration filenames. 14 digits, underscore, snake
// case name, either .up.sql or .down.sql.
var filenameRE = regexp.MustCompile(`^(\d{14}_[a-z0-9_]+)\.(up|down)\.sql$`)

// parseMigrationFilename splits a filename into its (name, direction)
// components. Returns ErrInvalidMigrationName for any filename that
// does not match the required pattern.
func parseMigrationFilename(filename string) (name string, dir direction, err error) {
	m := filenameRE.FindStringSubmatch(filename)
	if m == nil {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidMigrationName, filename)
	}
	return m[1], direction(m[2]), nil
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
// filenames, pairs up/down bodies, and returns the sorted list.
// Returns ErrInvalidMigrationName on any non-conforming filename, and
// ErrOrphanDownFile on a .down.sql without a matching .up.sql.
func loadFromFS(fsys fs.FS) ([]sourceMigration, error) {
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
		name, dir, perr := parseMigrationFilename(e.Name())
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

// loadFromDir is the disk-path entry point used by New when the source
// URL is "file://...". Callers pass the resolved absolute path. Rewrites
// UpPath to an absolute path for better error messages.
func loadFromDir(dir string) ([]sourceMigration, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	got, err := loadFromFS(os.DirFS(abs))
	if err != nil {
		return nil, err
	}
	for i := range got {
		got[i].UpPath = filepath.Join(abs, got[i].UpPath)
	}
	return got, nil
}
