package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/schema-migrate/schema-migrate/internal/config"
	"github.com/schema-migrate/schema-migrate/internal/database"
	"github.com/schema-migrate/schema-migrate/internal/diff"
	"github.com/schema-migrate/schema-migrate/internal/migration"
	"github.com/schema-migrate/schema-migrate/internal/model"
	"github.com/schema-migrate/schema-migrate/internal/output"
	"github.com/schema-migrate/schema-migrate/internal/security"
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
	rootCmd.PersistentFlags().StringVar(&flagSchemaFile, "schema-file", "", "Schema definition file path")

	rootCmd.AddCommand(createCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(upCmd())
	rootCmd.AddCommand(downCmd())
	rootCmd.AddCommand(redoCmd())
	rootCmd.AddCommand(diffCmd())
	rootCmd.AddCommand(initCmd())

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

			dirs := []string{"./migrations", "./schemas"}
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
			fmt.Println("  Schema: ./schemas/schema.yaml")
			return nil
		},
	}
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
	if flagSchemaFile != "" {
		cfg.Schema.FilePath = flagSchemaFile
	}
}
