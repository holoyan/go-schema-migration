package migrate

import (
	"errors"
	"testing"
)

func TestSentinelErrorsAreDistinct(t *testing.T) {
	// All sentinel errors must be comparable via errors.Is and
	// must not accidentally collapse into each other.
	all := []error{
		ErrInvalidSteps,
		ErrNoRollback,
		ErrDriverNotRegistered,
		ErrInvalidMigrationName,
		ErrOrphanDownFile,
	}
	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Fatalf("sentinel %v must not match %v", a, b)
			}
		}
	}
}
