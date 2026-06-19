package migration

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/schema-migrate/schema-migrate/internal/database"
	"github.com/schema-migrate/schema-migrate/internal/dependency"
	"github.com/schema-migrate/schema-migrate/internal/model"
	"github.com/schema-migrate/schema-migrate/internal/output"
	"github.com/schema-migrate/schema-migrate/internal/security"
)

type Executor struct {
	db         database.Database
	manager    *Manager
	security   *security.Checker
	dependency *dependency.Analyzer
	formatter  *output.Formatter
	cfg        *model.Config
}

func NewExecutor(db database.Database, cfg *model.Config, formatter *output.Formatter) *Executor {
	return &Executor{
		db:         db,
		manager:    NewManager(cfg.Migrations.Dir),
		security:   security.NewChecker(db),
		dependency: dependency.NewAnalyzer(),
		formatter:  formatter,
		cfg:        cfg,
	}
}

func (e *Executor) Create(name string) error {
	mig, err := e.manager.Create(name)
	if err != nil {
		return err
	}

	e.formatter.PrintSuccess(fmt.Sprintf("Created migration %s_%s", mig.Version, mig.Name))
	fmt.Printf("  Up:   %s\n", mig.UpPath)
	fmt.Printf("  Down: %s\n", mig.DownPath)
	return nil
}

func (e *Executor) Status(ctx context.Context) error {
	if err := e.db.EnsureMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to ensure migrations table: %w", err)
	}

	applied, err := e.db.GetAppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	appliedMap := make(map[string]model.MigrationRecord)
	for _, r := range applied {
		appliedMap[r.Version] = r
	}

	migrations, err := e.manager.GetMigrationsWithStatus(appliedMap)
	if err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	e.formatter.PrintMigrationStatus(migrations)

	allMigrations, err := e.manager.LoadAll()
	if err != nil {
		return err
	}

	conflicts := e.manager.DetectTimestampConflicts(allMigrations)
	if len(conflicts) > 0 {
		fmt.Println()
		e.formatter.PrintConflicts(conflicts)
	}

	checksumMismatches := e.manager.VerifyChecksums(allMigrations, appliedMap)
	if len(checksumMismatches) > 0 {
		fmt.Println()
		e.formatter.PrintChecksumMismatches(checksumMismatches)
	}

	return nil
}

func (e *Executor) Up(ctx context.Context, dryRun bool, force bool, step int) error {
	if err := e.db.EnsureMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to ensure migrations table: %w", err)
	}

	timeout := time.Duration(e.cfg.Concurrency.LockTimeout) * time.Second
	retryInterval := time.Duration(e.cfg.Concurrency.RetryInterval) * time.Millisecond

	e.formatter.PrintInfo("Acquiring advisory lock...")
	locked, err := e.db.AcquireAdvisoryLock(ctx, timeout, retryInterval)
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("could not acquire advisory lock within timeout")
	}
	defer e.db.ReleaseAdvisoryLock(ctx)
	e.formatter.PrintInfo("Lock acquired")

	applied, err := e.db.GetAppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	appliedMap := make(map[string]model.MigrationRecord)
	for _, r := range applied {
		appliedMap[r.Version] = r
	}

	pending, err := e.manager.GetPending(appliedMap)
	if err != nil {
		return fmt.Errorf("failed to load pending migrations: %w", err)
	}

	if len(pending) == 0 {
		e.formatter.PrintSuccess("No pending migrations to apply")
		return nil
	}

	ordered, err := e.dependency.GetMigrationOrder(pending)
	if err != nil {
		e.formatter.PrintWarning("Dependency analysis found issues: " + err.Error())
		e.formatter.PrintInfo("Using timestamp order instead")
		ordered = pending
	}

	if step > 0 && step < len(ordered) {
		ordered = ordered[:step]
	}

	allMigrations, err := e.manager.LoadAll()
	if err != nil {
		return err
	}

	conflicts := e.manager.DetectTimestampConflicts(allMigrations)
	if len(conflicts) > 0 {
		e.formatter.PrintConflicts(conflicts)
		if !force {
			return fmt.Errorf("timestamp conflicts detected. Use --force to apply anyway")
		}
	}

	checksumMismatches := e.manager.VerifyChecksums(allMigrations, appliedMap)
	if len(checksumMismatches) > 0 {
		e.formatter.PrintChecksumMismatches(checksumMismatches)
		if !force {
			return fmt.Errorf("checksum mismatches detected. Use --force to apply anyway")
		}
	}

	e.formatter.PrintInfo(fmt.Sprintf("Applying %d pending migration(s)...", len(ordered)))

	for i, mig := range ordered {
		checkResult, err := e.security.CheckSQL(ctx, mig.UpSQL)
		if err != nil {
			return fmt.Errorf("security check failed for %s: %w", mig.Version, err)
		}

		if checkResult.IsDangerous {
			e.formatter.PrintSecurityCheck(checkResult)
			if !force && !dryRun {
				fmt.Print("Do you want to continue? (y/N): ")
				reader := bufio.NewReader(os.Stdin)
				response, _ := reader.ReadString('\n')
				response = strings.TrimSpace(strings.ToLower(response))
				if response != "y" && response != "yes" {
					return fmt.Errorf("aborted by user due to dangerous operations")
				}
			} else if dryRun {
				e.formatter.PrintWarning("Dangerous operations detected in dry-run mode. Review the SQL carefully before executing.")
			}
		}
		_ = i
	}

	if dryRun {
		var allSQL strings.Builder
		for _, mig := range ordered {
			fmt.Fprintf(&allSQL, "-- Migration: %s_%s\n", mig.Version, mig.Name)
			fmt.Fprintln(&allSQL, mig.UpSQL)
			fmt.Fprintln(&allSQL)
		}
		e.formatter.PrintDryRunSQL(allSQL.String())
		return nil
	}

	for i, mig := range ordered {
		start := time.Now()
		e.formatter.PrintInfo(fmt.Sprintf("[%d/%d] Applying %s_%s...", i+1, len(ordered), mig.Version, mig.Name))

		_, err := e.db.Exec(ctx, mig.UpSQL)
		duration := time.Since(start)
		if err != nil {
			e.formatter.PrintExecutionResult(mig, "up", duration, err)
			return fmt.Errorf("failed to apply migration %s: %w", mig.Version, err)
		}

		checksum := CalculateChecksum(mig.UpSQL + mig.DownSQL)
		if err := e.db.RecordMigration(ctx, mig.Version, mig.Name, checksum, duration.Milliseconds()); err != nil {
			e.formatter.PrintExecutionResult(mig, "up", duration, err)
			return fmt.Errorf("failed to record migration %s: %w", mig.Version, err)
		}

		e.formatter.PrintExecutionResult(mig, "up", duration, nil)
	}

	e.formatter.PrintSuccess(fmt.Sprintf("Successfully applied %d migration(s)", len(ordered)))
	return nil
}

func (e *Executor) Down(ctx context.Context, dryRun bool, force bool, step int) error {
	if err := e.db.EnsureMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to ensure migrations table: %w", err)
	}

	timeout := time.Duration(e.cfg.Concurrency.LockTimeout) * time.Second
	retryInterval := time.Duration(e.cfg.Concurrency.RetryInterval) * time.Millisecond

	e.formatter.PrintInfo("Acquiring advisory lock...")
	locked, err := e.db.AcquireAdvisoryLock(ctx, timeout, retryInterval)
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("could not acquire advisory lock within timeout")
	}
	defer e.db.ReleaseAdvisoryLock(ctx)
	e.formatter.PrintInfo("Lock acquired")

	applied, err := e.db.GetAppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	if len(applied) == 0 {
		e.formatter.PrintSuccess("No migrations to rollback")
		return nil
	}

	appliedMap := make(map[string]model.MigrationRecord)
	for _, r := range applied {
		appliedMap[r.Version] = r
	}

	toRollback, err := e.manager.GetLastApplied(step, appliedMap)
	if err != nil {
		return fmt.Errorf("failed to get migrations to rollback: %w", err)
	}

	if len(toRollback) == 0 {
		e.formatter.PrintSuccess("No migrations to rollback")
		return nil
	}

	e.formatter.PrintWarning(fmt.Sprintf("This will rollback %d migration(s)!", len(toRollback)))

	for _, mig := range toRollback {
		checkResult, err := e.security.CheckSQL(ctx, mig.DownSQL)
		if err != nil {
			return fmt.Errorf("security check failed for %s: %w", mig.Version, err)
		}
		if checkResult.IsDangerous {
			e.formatter.PrintSecurityCheck(checkResult)
			if dryRun {
				e.formatter.PrintWarning("Dangerous operations detected in dry-run mode. Review the SQL carefully before executing.")
			}
		}
	}

	if dryRun {
		var allSQL strings.Builder
		for _, mig := range toRollback {
			fmt.Fprintf(&allSQL, "-- Rollback: %s_%s\n", mig.Version, mig.Name)
			fmt.Fprintln(&allSQL, mig.DownSQL)
			fmt.Fprintln(&allSQL)
		}
		e.formatter.PrintDryRunSQL(allSQL.String())
		return nil
	}

	if !force {
		fmt.Print("Are you sure you want to rollback? (y/N): ")
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			return fmt.Errorf("aborted by user")
		}
	}

	for i, mig := range toRollback {
		start := time.Now()
		e.formatter.PrintInfo(fmt.Sprintf("[%d/%d] Rolling back %s_%s...", i+1, len(toRollback), mig.Version, mig.Name))

		_, err = e.db.Exec(ctx, mig.DownSQL)
		duration := time.Since(start)
		if err != nil {
			e.formatter.PrintExecutionResult(mig, "down", duration, err)
			return fmt.Errorf("failed to rollback migration %s: %w", mig.Version, err)
		}

		if err := e.db.UnrecordMigration(ctx, mig.Version); err != nil {
			e.formatter.PrintExecutionResult(mig, "down", duration, err)
			return fmt.Errorf("failed to unrecord migration %s: %w", mig.Version, err)
		}

		e.formatter.PrintExecutionResult(mig, "down", duration, nil)
	}

	e.formatter.PrintSuccess(fmt.Sprintf("Successfully rolled back %d migration(s)", len(toRollback)))
	return nil
}

func (e *Executor) Redo(ctx context.Context, dryRun bool, force bool) error {
	e.formatter.PrintInfo("Rolling back last migration...")
	if err := e.Down(ctx, dryRun, true, 1); err != nil {
		return err
	}

	if !dryRun {
		e.formatter.PrintInfo("Re-applying migration...")
	}
	return e.Up(ctx, dryRun, force, 1)
}
