package migrate

import (
	"context"
	"fmt"
	"regexp"
)

// Up applies every pending migration in filename order as a new batch.
func (m *Migrator) Up(ctx context.Context) ([]AppliedMigration, error) {
	applied, err := m.drv.AppliedNames(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, fmt.Errorf("migrate: read applied: %w", err)
	}
	pending := computePending(m.src, applied)
	if len(pending) == 0 {
		m.log.Infof("migrate: nothing to do")
		return nil, nil
	}
	batch, err := m.drv.NextBatch(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, fmt.Errorf("migrate: next batch: %w", err)
	}
	out := make([]AppliedMigration, 0, len(pending))
	for _, mig := range pending {
		m.log.Infof("migrate: applying %s (batch %d)", mig.Name, batch)
		if err := m.drv.ApplyUp(ctx, m.cfg.DB, m.cfg.HistoryTable, mig.Name, mig.UpSQL, batch); err != nil {
			return out, fmt.Errorf("migrate: apply %s: %w", mig.Name, err)
		}
		out = append(out, AppliedMigration{Name: mig.Name, Batch: batch})
	}
	return out, nil
}

// Down rolls back the last `steps` batches.
func (m *Migrator) Down(ctx context.Context, steps int) ([]AppliedMigration, error) {
	if steps < 1 {
		return nil, ErrInvalidSteps
	}
	rows, err := m.drv.LastBatchMigrations(ctx, m.cfg.DB, m.cfg.HistoryTable, steps)
	if err != nil {
		return nil, fmt.Errorf("migrate: read last batches: %w", err)
	}
	byName := make(map[string]sourceMigration, len(m.src))
	for _, s := range m.src {
		byName[s.Name] = s
	}
	for _, r := range rows {
		s, ok := byName[r.Name]
		if !ok || !s.HasDown {
			return nil, fmt.Errorf("%w: %s", ErrNoRollback, r.Name)
		}
	}
	out := make([]AppliedMigration, 0, len(rows))
	for _, r := range rows {
		s := byName[r.Name]
		m.log.Infof("migrate: rolling back %s (batch %d)", r.Name, r.Batch)
		if err := m.drv.ApplyDown(ctx, m.cfg.DB, m.cfg.HistoryTable, r.Name, s.DownSQL); err != nil {
			return out, fmt.Errorf("migrate: rollback %s: %w", r.Name, err)
		}
		out = append(out, AppliedMigration{Name: r.Name, Batch: r.Batch, AppliedAt: r.AppliedAt})
	}
	return out, nil
}

// Plan returns migrations Up would execute. Does not modify the DB.
func (m *Migrator) Plan(ctx context.Context) ([]PlannedMigration, error) {
	applied, err := m.drv.AppliedNames(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, err
	}
	pending := computePending(m.src, applied)
	if len(pending) == 0 {
		return nil, nil
	}
	batch, err := m.drv.NextBatch(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, err
	}
	out := make([]PlannedMigration, 0, len(pending))
	for _, p := range pending {
		out = append(out, PlannedMigration{Name: p.Name, Path: p.UpPath, SQL: p.UpSQL, Batch: batch})
	}
	return out, nil
}

// PlanDown returns migrations Down(steps) would roll back.
func (m *Migrator) PlanDown(ctx context.Context, steps int) ([]PlannedMigration, error) {
	if steps < 1 {
		return nil, ErrInvalidSteps
	}
	rows, err := m.drv.LastBatchMigrations(ctx, m.cfg.DB, m.cfg.HistoryTable, steps)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]sourceMigration, len(m.src))
	for _, s := range m.src {
		byName[s.Name] = s
	}
	out := make([]PlannedMigration, 0, len(rows))
	for _, r := range rows {
		s, ok := byName[r.Name]
		if !ok || !s.HasDown {
			return nil, fmt.Errorf("%w: %s", ErrNoRollback, r.Name)
		}
		out = append(out, PlannedMigration{Name: r.Name, Path: s.UpPath, SQL: s.DownSQL, Batch: r.Batch})
	}
	return out, nil
}

// BackfillOptions configures Migrator.Backfill.
type BackfillOptions struct {
	// Batch, if > 0, assigns all backfilled migrations to this batch number.
	// If 0, each migration gets its own incrementing batch starting at MAX+1.
	Batch int

	// FilenameRegex overrides the default YYYYMMDDHHMMSS_name.(up|down).sql pattern.
	// Must have two capture groups: name and direction.
	// If nil, DefaultFilenameRE is used.
	FilenameRegex *regexp.Regexp

	// DryRun, if true, returns the plan but writes nothing to the DB.
	DryRun bool
}

// Backfill records every pending migration in the history table WITHOUT
// executing its SQL. For users migrating from another tool who already
// have the schema in place.
//
// Returns the list of migrations that would be (or were) recorded.
// Already-applied names are skipped automatically.
func (m *Migrator) Backfill(ctx context.Context, opts BackfillOptions) ([]AppliedMigration, error) {
	src := m.src
	if opts.FilenameRegex != nil && m.cfg.Source != "" {
		var err error
		src, err = loadSourceWithRegex(m.cfg.Source, opts.FilenameRegex)
		if err != nil {
			return nil, fmt.Errorf("migrate: backfill load source: %w", err)
		}
	}

	applied, err := m.drv.AppliedNames(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, fmt.Errorf("migrate: backfill read applied: %w", err)
	}
	pending := computePending(src, applied)
	if len(pending) == 0 {
		return nil, nil
	}

	out := make([]AppliedMigration, 0, len(pending))
	if opts.Batch > 0 {
		for _, mig := range pending {
			out = append(out, AppliedMigration{Name: mig.Name, Batch: opts.Batch})
		}
	} else {
		start, err := m.drv.NextBatch(ctx, m.cfg.DB, m.cfg.HistoryTable)
		if err != nil {
			return nil, fmt.Errorf("migrate: backfill next batch: %w", err)
		}
		for i, mig := range pending {
			out = append(out, AppliedMigration{Name: mig.Name, Batch: start + i})
		}
	}

	if opts.DryRun {
		return out, nil
	}

	for _, a := range out {
		m.log.Infof("migrate: backfilling %s (batch %d)", a.Name, a.Batch)
		if err := m.drv.RecordApplied(ctx, m.cfg.DB, m.cfg.HistoryTable, a.Name, a.Batch); err != nil {
			return out, fmt.Errorf("migrate: backfill record %s: %w", a.Name, err)
		}
	}
	return out, nil
}

// Status returns every migration paired with its applied state.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	rows, err := m.drv.AllMigrations(ctx, m.cfg.DB, m.cfg.HistoryTable)
	if err != nil {
		return nil, err
	}
	history := make([]historyRow, 0, len(rows))
	for _, r := range rows {
		history = append(history, historyRow{Name: r.Name, Batch: r.Batch, AppliedAt: r.AppliedAt})
	}
	return buildStatuses(m.src, history), nil
}
