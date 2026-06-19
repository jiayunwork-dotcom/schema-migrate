package seed

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/schema-migrate/schema-migrate/internal/database"
	"github.com/schema-migrate/schema-migrate/internal/model"
	"github.com/schema-migrate/schema-migrate/internal/output"
)

type Executor struct {
	db        database.Database
	manager   *Manager
	formatter *output.Formatter
	cfg       *model.Config
}

func NewExecutor(db database.Database, cfg *model.Config, formatter *output.Formatter) *Executor {
	return &Executor{
		db:        db,
		manager:   NewManager(cfg.Seeds.Dir),
		formatter: formatter,
		cfg:       cfg,
	}
}

func (e *Executor) Create(name string) error {
	seed, err := e.manager.Create(name)
	if err != nil {
		return err
	}

	e.formatter.PrintSuccess(fmt.Sprintf("Created seed %s_%s", seed.Version, seed.Name))
	fmt.Printf("  File: %s\n", seed.Path)
	return nil
}

func (e *Executor) Apply(ctx context.Context, env string, force bool) error {
	if err := e.db.EnsureMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to ensure migrations table: %w", err)
	}

	if err := e.checkAllMigrationsApplied(ctx); err != nil {
		return err
	}

	if err := e.db.EnsureSeedsTable(ctx); err != nil {
		return fmt.Errorf("failed to ensure seeds table: %w", err)
	}

	if env == "" {
		env = e.cfg.Seeds.DefaultEnv
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

	return e.applySeedsInternal(ctx, env, force)
}

func (e *Executor) applySeedsInternal(ctx context.Context, env string, force bool) error {
	allSeeds, err := e.manager.LoadAll()
	if err != nil {
		return fmt.Errorf("failed to load seeds: %w", err)
	}

	appliedSeeds, err := e.db.GetAppliedSeeds(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied seeds: %w", err)
	}

	appliedMap := make(map[string]model.SeedRecord)
	for _, r := range appliedSeeds {
		appliedMap[r.Version] = r
	}

	checksumMismatches := e.manager.VerifyChecksums(allSeeds, appliedMap)
	if len(checksumMismatches) > 0 {
		e.formatter.PrintChecksumMismatches(checksumMismatches)
		if !force {
			return fmt.Errorf("checksum mismatches detected. Use --force to apply anyway")
		}
	}

	filteredSeeds := e.manager.FilterByEnvironment(allSeeds, env)
	e.formatter.PrintInfo(fmt.Sprintf("Environment: %s", env))

	pendingSeeds := e.manager.GetPending(filteredSeeds, appliedMap)

	if len(pendingSeeds) == 0 {
		e.formatter.PrintSuccess("No pending seeds to apply")
		return nil
	}

	e.formatter.PrintInfo(fmt.Sprintf("Applying %d pending seed(s)...", len(pendingSeeds)))

	for i, s := range pendingSeeds {
		start := time.Now()
		envLabel := ""
		if s.Environment != "" {
			envLabel = fmt.Sprintf(" [%s]", s.Environment)
		}
		e.formatter.PrintInfo(fmt.Sprintf("[%d/%d] Applying %s_%s%s...",
			i+1, len(pendingSeeds), s.Version, s.Name, envLabel))

		_, err := e.db.Exec(ctx, s.SQL)
		duration := time.Since(start)
		if err != nil {
			e.formatter.PrintSeedExecutionResult(s, "apply", duration, err)
			return fmt.Errorf("failed to apply seed %s: %w", s.Version, err)
		}

		tablesStr := strings.Join(s.Tables, ",")
		if err := e.db.RecordSeed(ctx, s.Version, s.Name, s.Checksum, s.Environment, tablesStr); err != nil {
			e.formatter.PrintSeedExecutionResult(s, "apply", duration, err)
			return fmt.Errorf("failed to record seed %s: %w", s.Version, err)
		}

		e.formatter.PrintSeedExecutionResult(s, "apply", duration, nil)
	}

	e.formatter.PrintSuccess(fmt.Sprintf("Successfully applied %d seed(s)", len(pendingSeeds)))
	return nil
}

func (e *Executor) Reset(ctx context.Context, env string, force bool) error {
	if err := e.db.EnsureMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to ensure migrations table: %w", err)
	}

	if err := e.checkAllMigrationsApplied(ctx); err != nil {
		return err
	}

	if err := e.db.EnsureSeedsTable(ctx); err != nil {
		return fmt.Errorf("failed to ensure seeds table: %w", err)
	}

	if env == "" {
		env = e.cfg.Seeds.DefaultEnv
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

	appliedSeeds, err := e.db.GetAppliedSeeds(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied seeds: %w", err)
	}

	if len(appliedSeeds) == 0 {
		e.formatter.PrintInfo("No seeds have been applied, proceeding to apply all seeds")
	} else {
		e.formatter.PrintWarning(fmt.Sprintf("This will clear all seed data from %d table(s) and re-apply all seeds!",
			len(appliedSeeds)))

		if !force {
			fmt.Print("Are you sure you want to reset all seed data? (y/N): ")
			reader := bufio.NewReader(os.Stdin)
			response, _ := reader.ReadString('\n')
			response = strings.TrimSpace(strings.ToLower(response))
			if response != "y" && response != "yes" {
				return fmt.Errorf("aborted by user")
			}
		}

		tablesToTruncate := e.getUniqueTablesInReverseOrder(appliedSeeds)
		e.formatter.PrintInfo(fmt.Sprintf("Clearing data from %d table(s)...", len(tablesToTruncate)))

		for i, tableName := range tablesToTruncate {
			e.formatter.PrintInfo(fmt.Sprintf("[%d/%d] Clearing table %s...",
				i+1, len(tablesToTruncate), tableName))

			var sql string
			if e.db.Type() == model.DBSQLite {
				sql = e.db.GetDeleteFromSQL(tableName)
			} else {
				sql = e.db.GetTruncateSQL(tableName)
			}

			if _, err := e.db.Exec(ctx, sql); err != nil {
				return fmt.Errorf("failed to clear table %s: %w", tableName, err)
			}
		}

		if err := e.db.UnrecordAllSeeds(ctx); err != nil {
			return fmt.Errorf("failed to clear seed records: %w", err)
		}

		e.formatter.PrintSuccess("All seed data cleared")
	}

	return e.applySeedsInternal(ctx, env, true)
}

func (e *Executor) checkAllMigrationsApplied(ctx context.Context) error {
	allMigrationsDir := e.cfg.Migrations.Dir
	if allMigrationsDir == "" {
		allMigrationsDir = "./migrations"
	}

	files, err := os.ReadDir(allMigrationsDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to check migrations directory: %w", err)
	}

	migrationCount := 0
	if files != nil {
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".up.sql") {
				migrationCount++
			}
		}
	}

	applied, err := e.db.GetAppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	appliedCount := len(applied)

	if migrationCount > appliedCount {
		pendingCount := migrationCount - appliedCount
		return fmt.Errorf(
			"there are %d pending migration(s) that need to be applied first. "+
				"Please run 'schema-migrate up' before applying seeds. "+
				"Pending migrations: %d, Applied: %d",
			pendingCount, pendingCount, appliedCount)
	}

	return nil
}

func (e *Executor) getUniqueTablesInReverseOrder(seeds []model.SeedRecord) []string {
	tableOrder := make([]string, 0)
	tableSet := make(map[string]bool)

	for i := len(seeds) - 1; i >= 0; i-- {
		seed := seeds[i]
		if seed.Tables == "" {
			continue
		}
		tables := strings.Split(seed.Tables, ",")
		for j := len(tables) - 1; j >= 0; j-- {
			table := strings.TrimSpace(tables[j])
			if table != "" && !tableSet[table] {
				tableSet[table] = true
				tableOrder = append(tableOrder, table)
			}
		}
	}

	return tableOrder
}
