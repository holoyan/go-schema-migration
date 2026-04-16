package migrate

import (
	"fmt"
	"regexp"
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
