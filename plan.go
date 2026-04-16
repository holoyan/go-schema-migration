package migrate

import "time"

// historyRow is the library's internal view of a row from the driver's
// AppliedRow. Kept separate so plan.go is decoupled from driver types.
type historyRow struct {
	Name      string
	Batch     int
	AppliedAt time.Time
}

// computePending returns the subset of onDisk migrations whose names
// are not present in applied. Input ordering of onDisk is preserved.
func computePending(onDisk []sourceMigration, applied []string) []sourceMigration {
	set := make(map[string]struct{}, len(applied))
	for _, a := range applied {
		set[a] = struct{}{}
	}
	out := make([]sourceMigration, 0, len(onDisk))
	for _, m := range onDisk {
		if _, already := set[m.Name]; !already {
			out = append(out, m)
		}
	}
	return out
}

// buildStatuses left-joins onDisk against history and returns one
// MigrationStatus per file. Applied status comes from history.
func buildStatuses(onDisk []sourceMigration, history []historyRow) []MigrationStatus {
	byName := make(map[string]historyRow, len(history))
	for _, h := range history {
		byName[h.Name] = h
	}
	out := make([]MigrationStatus, 0, len(onDisk))
	for _, m := range onDisk {
		s := MigrationStatus{Name: m.Name}
		if h, ok := byName[m.Name]; ok {
			s.Applied = true
			s.Batch = h.Batch
			s.AppliedAt = h.AppliedAt
		}
		out = append(out, s)
	}
	return out
}
