package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/schema-migrate/schema-migrate/internal/config"
	"github.com/schema-migrate/schema-migrate/internal/database"
	"github.com/schema-migrate/schema-migrate/internal/diff"
	"github.com/schema-migrate/schema-migrate/internal/generator"
	"github.com/schema-migrate/schema-migrate/internal/migration"
	"github.com/schema-migrate/schema-migrate/internal/model"
	"github.com/schema-migrate/schema-migrate/internal/output"
	"github.com/schema-migrate/schema-migrate/internal/security"
	"github.com/schema-migrate/schema-migrate/internal/seed"
	"github.com/schema-migrate/schema-migrate/internal/sqlparser"
)

var (
	cfgPath            string
	jsonOutput         bool
	flagDBType         string
	flagDBHost         string
	flagDBPort         string
	flagDBUser         string
	flagDBPassword     string
	flagDBName         string
	flagMigrationsDir  string
	flagSeedsDir       string
	flagSchemaFile     string
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nReceived interrupt signal, shutting down...")
		cancel()
	}()

	rootCmd := &cobra.Command{
		Use:   "schema-migrate",
		Short: "Multi-database schema migration and management tool",
		Long: `schema-migrate is a command-line tool for managing database schema changes
across PostgreSQL, MySQL, and SQLite databases. It provides versioned migration
tracking, schema diffing, safety checks, and dependency analysis.`,
		SilenceUsage: true,
	}

	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", config.GetDefaultConfigPath(), "Path to configuration file")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.PersistentFlags().StringVar(&flagDBType, "db-type", "", "Database type (postgres, mysql, sqlite)")
	rootCmd.PersistentFlags().StringVar(&flagDBHost, "db-host", "", "Database host")
	rootCmd.PersistentFlags().StringVar(&flagDBPort, "db-port", "", "Database port")
	rootCmd.PersistentFlags().StringVar(&flagDBUser, "db-user", "", "Database user")
	rootCmd.PersistentFlags().StringVar(&flagDBPassword, "db-password", "", "Database password")
	rootCmd.PersistentFlags().StringVar(&flagDBName, "db-name", "", "Database name")
	rootCmd.PersistentFlags().StringVar(&flagMigrationsDir, "migrations-dir", "", "Migrations directory")
	rootCmd.PersistentFlags().StringVar(&flagSeedsDir, "seeds-dir", "", "Seeds directory")
	rootCmd.PersistentFlags().StringVar(&flagSchemaFile, "schema-file", "", "Schema definition file path")

	rootCmd.AddCommand(createCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(upCmd())
	rootCmd.AddCommand(downCmd())
	rootCmd.AddCommand(redoCmd())
	rootCmd.AddCommand(diffCmd())
	rootCmd.AddCommand(generateCmd())
	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(seedCmd())

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func createCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create [migration-name]",
		Short: "Create a new migration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			formatter := output.NewFormatter(jsonOutput)
			cfg, err := loadConfig()
			if err != nil {
				formatter.PrintError("Failed to load config", err)
				return err
			}

			db, err := database.New(config.GetDBType(cfg))
			if err != nil {
				formatter.PrintError("Failed to initialize database", err)
				return err
			}

			executor := migration.NewExecutor(db, cfg, formatter)
			return executor.Create(args[0])
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show migration status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			formatter := output.NewFormatter(jsonOutput)
			cfg, err := loadConfig()
			if err != nil {
				formatter.PrintError("Failed to load config", err)
				return err
			}

			db, err := database.New(config.GetDBType(cfg))
			if err != nil {
				formatter.PrintError("Failed to initialize database", err)
				return err
			}
			defer db.Close()

			if err := db.Connect(ctx, config.GetDSN(cfg)); err != nil {
				formatter.PrintError("Failed to connect to database", err)
				return err
			}

			executor := migration.NewExecutor(db, cfg, formatter)
			return executor.Status(ctx)
		},
	}
}

func upCmd() *cobra.Command {
	var dryRun, force bool
	var step int

	cmd := &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			formatter := output.NewFormatter(jsonOutput)
			cfg, err := loadConfig()
			if err != nil {
				formatter.PrintError("Failed to load config", err)
				return err
			}

			db, err := database.New(config.GetDBType(cfg))
			if err != nil {
				formatter.PrintError("Failed to initialize database", err)
				return err
			}
			defer db.Close()

			if err := db.Connect(ctx, config.GetDSN(cfg)); err != nil {
				formatter.PrintError("Failed to connect to database", err)
				return err
			}

			executor := migration.NewExecutor(db, cfg, formatter)
			return executor.Up(ctx, dryRun, force, step)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show SQL without executing")
	cmd.Flags().BoolVar(&force, "force", false, "Force execution despite warnings")
	cmd.Flags().IntVar(&step, "step", 0, "Number of migrations to apply (0 = all)")

	return cmd
}

func downCmd() *cobra.Command {
	var dryRun, force bool
	var step int

	cmd := &cobra.Command{
		Use:   "down",
		Short: "Rollback migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			formatter := output.NewFormatter(jsonOutput)
			cfg, err := loadConfig()
			if err != nil {
				formatter.PrintError("Failed to load config", err)
				return err
			}

			db, err := database.New(config.GetDBType(cfg))
			if err != nil {
				formatter.PrintError("Failed to initialize database", err)
				return err
			}
			defer db.Close()

			if err := db.Connect(ctx, config.GetDSN(cfg)); err != nil {
				formatter.PrintError("Failed to connect to database", err)
				return err
			}

			if step == 0 {
				step = 1
			}

			executor := migration.NewExecutor(db, cfg, formatter)
			return executor.Down(ctx, dryRun, force, step)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show SQL without executing")
	cmd.Flags().BoolVar(&force, "force", false, "Force rollback without confirmation")
	cmd.Flags().IntVar(&step, "step", 1, "Number of migrations to rollback")

	return cmd
}

func redoCmd() *cobra.Command {
	var dryRun, force bool

	cmd := &cobra.Command{
		Use:   "redo",
		Short: "Rollback and re-apply the last migration",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			formatter := output.NewFormatter(jsonOutput)
			cfg, err := loadConfig()
			if err != nil {
				formatter.PrintError("Failed to load config", err)
				return err
			}

			db, err := database.New(config.GetDBType(cfg))
			if err != nil {
				formatter.PrintError("Failed to initialize database", err)
				return err
			}
			defer db.Close()

			if err := db.Connect(ctx, config.GetDSN(cfg)); err != nil {
				formatter.PrintError("Failed to connect to database", err)
				return err
			}

			executor := migration.NewExecutor(db, cfg, formatter)
			return executor.Redo(ctx, dryRun, force)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show SQL without executing")
	cmd.Flags().BoolVar(&force, "force", false, "Force execution despite warnings")

	return cmd
}

func diffCmd() *cobra.Command {
	var apply, dryRun, force bool

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Compare current database schema with target schema",
		Long:  `Compare the current database schema with the target schema defined in the schema file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			formatter := output.NewFormatter(jsonOutput)
			cfg, err := loadConfig()
			if err != nil {
				formatter.PrintError("Failed to load config", err)
				return err
			}

			db, err := database.New(config.GetDBType(cfg))
			if err != nil {
				formatter.PrintError("Failed to initialize database", err)
				return err
			}
			defer db.Close()

			if err := db.Connect(ctx, config.GetDSN(cfg)); err != nil {
				formatter.PrintError("Failed to connect to database", err)
				return err
			}

			if err := db.EnsureMigrationsTable(ctx); err != nil {
				formatter.PrintError("Failed to ensure migrations table", err)
				return err
			}

			differ := diff.NewDiffer(db)
			currentSchema, err := db.GetCurrentSchema(ctx)
			if err != nil {
				formatter.PrintError("Failed to get current schema", err)
				return err
			}

			targetSchema, err := differ.LoadTargetSchema(cfg.Schema.FilePath)
			if err != nil {
				formatter.PrintError("Failed to load target schema", err)
				return err
			}

			schemaDiff := differ.Compare(currentSchema, targetSchema)

			if len(schemaDiff.Changes) == 0 {
				formatter.PrintSuccess("No schema differences found")
				return nil
			}

			formatter.PrintDiff(schemaDiff)

			checker := security.NewChecker(db)
			checkResult, err := checker.CheckDiff(ctx, schemaDiff)
			if err != nil {
				formatter.PrintError("Security check failed", err)
				return err
			}
			formatter.PrintSecurityCheck(checkResult)

			if apply {
				if checkResult.IsDangerous && !force {
					return fmt.Errorf("dangerous operations detected. Use --force to apply anyway")
				}

				var allSQL strings.Builder
				for _, change := range schemaDiff.Changes {
					allSQL.WriteString(change.SQL)
					allSQL.WriteString("\n\n")
				}

				if dryRun {
					formatter.PrintDryRunSQL(allSQL.String())
					return nil
				}

				formatter.PrintInfo("Applying schema changes...")
				for i, change := range schemaDiff.Changes {
					formatter.PrintInfo(fmt.Sprintf("[%d/%d] %s", i+1, len(schemaDiff.Changes), change.Details))
					if _, err := db.Exec(ctx, change.SQL); err != nil {
						return fmt.Errorf("failed to apply change %d: %w", i+1, err)
					}
				}
				formatter.PrintSuccess(fmt.Sprintf("Successfully applied %d schema change(s)", len(schemaDiff.Changes)))
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&apply, "apply", false, "Apply the generated changes")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show SQL without executing (with --apply)")
	cmd.Flags().BoolVar(&force, "force", false, "Force apply despite dangerous operations")

	return cmd
}

func generateCmd() *cobra.Command {
	var targetFile string
	var migrationName string
	var dryRun bool
	var split bool
	var fromMigrations bool

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate migration files from schema diff",
		Long: `Compare current database schema with target schema and auto-generate
migration files (up and down SQL) based on the differences.

Use --from-migrations to run in offline mode, parsing existing migration files
instead of connecting to a database to determine the current schema.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			formatter := output.NewFormatter(jsonOutput)
			cfg, err := loadConfig()
			if err != nil {
				formatter.PrintError("Failed to load config", err)
				return err
			}

			var currentSchema *model.Schema
			var gen *generator.Generator
			var db database.Database

			db, err = database.New(config.GetDBType(cfg))
			if err != nil {
				formatter.PrintError("Failed to initialize database", err)
				return err
			}

			if fromMigrations {
				formatter.PrintInfo("Using offline mode: parsing migration files to determine current schema")

				rebuilder := sqlparser.NewSchemaRebuilder(cfg.Migrations.Dir)
				currentSchema, err = rebuilder.RebuildSchema()
				if err != nil {
					formatter.PrintError("Failed to rebuild schema from migrations", err)
					return err
				}

				if sqliteDB, ok := db.(*database.SQLite); ok {
					sqliteDB.SetOfflineSchema(currentSchema)
				}

				gen = generator.NewGenerator(db, cfg.Migrations.Dir)

				if len(currentSchema.Tables) == 0 {
					formatter.PrintInfo("No existing migrations found - starting with empty schema")
				} else {
					formatter.PrintInfo(fmt.Sprintf("Parsed %d migration(s), reconstructed schema with %d table(s)",
						countMigrationFiles(cfg.Migrations.Dir), len(currentSchema.Tables)))
				}
			} else {
				defer db.Close()

				if err := db.Connect(ctx, config.GetDSN(cfg)); err != nil {
					formatter.PrintError("Failed to connect to database", err)
					return err
				}

				if err := db.EnsureMigrationsTable(ctx); err != nil {
					formatter.PrintError("Failed to ensure migrations table", err)
					return err
				}

				gen = generator.NewGenerator(db, cfg.Migrations.Dir)

				hasPending, pendingMigrations, err := gen.CheckPendingMigrations(ctx)
				if err != nil {
					formatter.PrintWarning(fmt.Sprintf("Warning: Could not check pending migrations: %v", err))
				} else if hasPending {
					formatter.PrintWarning(fmt.Sprintf("Found %d pending migration(s):", len(pendingMigrations)))
					for _, mig := range pendingMigrations {
						fmt.Printf("  - %s_%s\n", mig.Version, mig.Name)
					}
					formatter.PrintWarning("Applying these first is recommended before generating new migrations to avoid timestamp ordering issues.")
				}

				currentSchema, err = db.GetCurrentSchema(ctx)
				if err != nil {
					formatter.PrintError("Failed to get current schema", err)
					return err
				}
			}

			schemaPath := cfg.Schema.FilePath
			if targetFile != "" {
				schemaPath = targetFile
			}

			differ := diff.NewDiffer(db)

			targetSchema, err := differ.LoadTargetSchema(schemaPath)
			if err != nil {
				formatter.PrintError("Failed to load target schema", err)
				return err
			}

			if migrationName == "" {
				migrationName = "auto_generated"
			}

			if split {
				tableMigrations, err := gen.GenerateSplit(currentSchema, targetSchema, migrationName)
				if err != nil {
					formatter.PrintError("Failed to generate migrations", err)
					return err
				}

				if len(tableMigrations) == 0 {
					formatter.PrintSuccess("No schema differences found - nothing to generate")
					return nil
				}

				if dryRun {
					fmt.Println()
					formatter.PrintInfo(fmt.Sprintf("Would generate %d migration(s) (split mode):", len(tableMigrations)))
					for i, tm := range tableMigrations {
						fmt.Printf("\n--- Migration %d: %s ---\n", i+1, tm.TableName)
						fmt.Println("\nUP:")
						fmt.Println(tm.UpSQL)
						fmt.Println("\nDOWN:")
						fmt.Println(tm.DownSQL)
					}
					return nil
				}

				migrations, err := gen.WriteSplitMigrations(migrationName, tableMigrations)
				if err != nil {
					formatter.PrintError("Failed to write migration files", err)
					return err
				}

				formatter.PrintSuccess(fmt.Sprintf("Successfully generated %d migration(s):", len(migrations)))
				for _, mig := range migrations {
					fmt.Printf("  Up:   %s\n", mig.UpPath)
					fmt.Printf("  Down: %s\n", mig.DownPath)
				}
			} else {
				upSQL, downSQL, err := gen.Generate(currentSchema, targetSchema, migrationName)
				if err != nil {
					formatter.PrintError("Failed to generate migrations", err)
					return err
				}

				if upSQL == "" && downSQL == "" {
					formatter.PrintSuccess("No schema differences found - nothing to generate")
					return nil
				}

				if dryRun {
					fmt.Println()
					formatter.PrintInfo("Generated migration (dry run):")
					fmt.Println("\n--- UP SQL ---")
					fmt.Println(upSQL)
					fmt.Println("\n--- DOWN SQL ---")
					fmt.Println(downSQL)
					return nil
				}

				mig, err := gen.WriteMigration(migrationName, upSQL, downSQL)
				if err != nil {
					formatter.PrintError("Failed to write migration files", err)
					return err
				}

				formatter.PrintSuccess(fmt.Sprintf("Generated migration %s_%s", mig.Version, mig.Name))
				fmt.Printf("  Up:   %s\n", mig.UpPath)
				fmt.Printf("  Down: %s\n", mig.DownPath)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&targetFile, "target", "", "Target schema YAML file path (overrides schema.file config)")
	cmd.Flags().StringVar(&migrationName, "name", "auto_generated", "Custom migration name")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Only output SQL without creating files")
	cmd.Flags().BoolVar(&split, "split", false, "Split changes into per-table migration files")
	cmd.Flags().BoolVar(&fromMigrations, "from-migrations", false, "Offline mode: parse migration files instead of connecting to database")

	return cmd
}

func countMigrationFiles(dir string) int {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return 0
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	migrationFileRegex := regexp.MustCompile(`^(\d{14})_(.+)\.up\.sql$`)
	count := 0
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		if migrationFileRegex.MatchString(file.Name()) {
			count++
		}
	}
	return count
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize configuration and directories",
		RunE: func(cmd *cobra.Command, args []string) error {
			formatter := output.NewFormatter(jsonOutput)

			if _, err := os.Stat(cfgPath); err == nil {
				return fmt.Errorf("config file already exists at %s", cfgPath)
			}

			defaultConfig := `database:
  type: postgres
  host: localhost
  port: 5432
  user: ${DB_USER:-postgres}
  password: ${DB_PASSWORD:-password}
  dbname: ${DB_NAME:-mydb}
  sslmode: disable

migrations:
  dir: ./migrations

seeds:
  dir: ./seeds
  default_env: development

schema:
  file: ./schemas/schema.yaml

concurrency:
  lock_timeout_seconds: 300
  retry_interval_ms: 1000
`

			if err := os.WriteFile(cfgPath, []byte(defaultConfig), 0644); err != nil {
				formatter.PrintError("Failed to create config file", err)
				return err
			}

			dirs := []string{"./migrations", "./schemas", "./seeds"}
			for _, dir := range dirs {
				if err := os.MkdirAll(dir, 0755); err != nil {
					formatter.PrintError("Failed to create directory", err)
					return err
				}
			}

			defaultSchema := `tables:
  - name: users
    columns:
      - name: id
        type: bigint
        nullable: false
        autoincrement: true
        is_primary_key: true
      - name: name
        type: varchar(255)
        nullable: false
      - name: email
        type: varchar(255)
        nullable: false
      - name: created_at
        type: timestamp
        nullable: false
        default_value: NOW()
    indexes:
      - name: idx_users_email
        columns: [email]
        unique: true
`
			if err := os.WriteFile("./schemas/schema.yaml", []byte(defaultSchema), 0644); err != nil {
				formatter.PrintError("Failed to create default schema", err)
				return err
			}

			formatter.PrintSuccess("Initialized schema-migrate project")
			fmt.Printf("  Config: %s\n", cfgPath)
			fmt.Println("  Migrations: ./migrations")
			fmt.Println("  Seeds: ./seeds")
			fmt.Println("  Schema: ./schemas/schema.yaml")
			return nil
		},
	}
}

func seedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Manage seed data",
		Long:  `Create, apply, and reset seed data for testing and initialization.`,
	}

	cmd.AddCommand(seedCreateCmd())
	cmd.AddCommand(seedApplyCmd())
	cmd.AddCommand(seedResetCmd())

	return cmd
}

func seedCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create [seed-name]",
		Short: "Create a new seed file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			formatter := output.NewFormatter(jsonOutput)
			cfg, err := loadConfig()
			if err != nil {
				formatter.PrintError("Failed to load config", err)
				return err
			}

			db, err := database.New(config.GetDBType(cfg))
			if err != nil {
				formatter.PrintError("Failed to initialize database", err)
				return err
			}

			executor := seed.NewExecutor(db, cfg, formatter)
			return executor.Create(args[0])
		},
	}
}

func seedApplyCmd() *cobra.Command {
	var env string
	var force bool

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply all pending seeds",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			formatter := output.NewFormatter(jsonOutput)
			cfg, err := loadConfig()
			if err != nil {
				formatter.PrintError("Failed to load config", err)
				return err
			}

			db, err := database.New(config.GetDBType(cfg))
			if err != nil {
				formatter.PrintError("Failed to initialize database", err)
				return err
			}
			defer db.Close()

			if err := db.Connect(ctx, config.GetDSN(cfg)); err != nil {
				formatter.PrintError("Failed to connect to database", err)
				return err
			}

			executor := seed.NewExecutor(db, cfg, formatter)
			return executor.Apply(ctx, env, force)
		},
	}

	cmd.Flags().StringVar(&env, "env", "", "Environment tag (development/test/production)")
	cmd.Flags().BoolVar(&force, "force", false, "Force execution despite checksum warnings")

	return cmd
}

func seedResetCmd() *cobra.Command {
	var env string
	var force bool

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset all seed data and re-apply",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			formatter := output.NewFormatter(jsonOutput)
			cfg, err := loadConfig()
			if err != nil {
				formatter.PrintError("Failed to load config", err)
				return err
			}

			db, err := database.New(config.GetDBType(cfg))
			if err != nil {
				formatter.PrintError("Failed to initialize database", err)
				return err
			}
			defer db.Close()

			if err := db.Connect(ctx, config.GetDSN(cfg)); err != nil {
				formatter.PrintError("Failed to connect to database", err)
				return err
			}

			executor := seed.NewExecutor(db, cfg, formatter)
			return executor.Reset(ctx, env, force)
		},
	}

	cmd.Flags().StringVar(&env, "env", "", "Environment tag (development/test/production)")
	cmd.Flags().BoolVar(&force, "force", false, "Force reset without confirmation")

	return cmd
}

func loadConfig() (*model.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}

	applyConfigOverrides(cfg)
	return cfg, nil
}

func applyConfigOverrides(cfg *model.Config) {
	if flagDBType != "" {
		cfg.Database.Type = model.DBType(flagDBType)
	}
	if flagDBHost != "" {
		cfg.Database.Host = flagDBHost
	}
	if flagDBPort != "" {
		if port, err := strconv.Atoi(flagDBPort); err == nil {
			cfg.Database.Port = port
		}
	}
	if flagDBUser != "" {
		cfg.Database.User = flagDBUser
	}
	if flagDBPassword != "" {
		cfg.Database.Password = flagDBPassword
	}
	if flagDBName != "" {
		cfg.Database.DBName = flagDBName
	}
	if flagMigrationsDir != "" {
		cfg.Migrations.Dir = flagMigrationsDir
	}
	if flagSeedsDir != "" {
		cfg.Seeds.Dir = flagSeedsDir
	}
	if flagSchemaFile != "" {
		cfg.Schema.FilePath = flagSchemaFile
	}
}
