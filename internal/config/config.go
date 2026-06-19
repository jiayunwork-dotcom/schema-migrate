package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/schema-migrate/schema-migrate/internal/model"
	"gopkg.in/yaml.v3"
)

var envVarRegex = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

func Load(path string) (*model.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg model.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	expandEnvVariables(&cfg)
	setDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func expandEnvVariables(cfg *model.Config) {
	cfg.Database.Host = replaceEnv(cfg.Database.Host)
	cfg.Database.User = replaceEnv(cfg.Database.User)
	cfg.Database.Password = replaceEnv(cfg.Database.Password)
	cfg.Database.DBName = replaceEnv(cfg.Database.DBName)
	cfg.Database.DSN = replaceEnv(cfg.Database.DSN)
	cfg.Migrations.Dir = replaceEnv(cfg.Migrations.Dir)
	cfg.Schema.FilePath = replaceEnv(cfg.Schema.FilePath)
}

func replaceEnv(s string) string {
	return envVarRegex.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1]
		if val := os.Getenv(varName); val != "" {
			return val
		}
		return match
	})
}

func setDefaults(cfg *model.Config) {
	if cfg.Database.Type == "" {
		cfg.Database.Type = model.DBPostgreSQL
	}
	if cfg.Database.Port == 0 {
		switch cfg.Database.Type {
		case model.DBPostgreSQL:
			cfg.Database.Port = 5432
		case model.DBMySQL:
			cfg.Database.Port = 3306
		case model.DBSQLite:
			cfg.Database.Port = 0
		}
	}
	if cfg.Database.SSLMode == "" {
		cfg.Database.SSLMode = "disable"
	}
	if cfg.Migrations.Dir == "" {
		cfg.Migrations.Dir = "./migrations"
	}
	if cfg.Schema.FilePath == "" {
		cfg.Schema.FilePath = "./schemas/schema.yaml"
	}
	if cfg.Concurrency.LockTimeout == 0 {
		cfg.Concurrency.LockTimeout = 300
	}
	if cfg.Concurrency.RetryInterval == 0 {
		cfg.Concurrency.RetryInterval = 1000
	}
}

func validate(cfg *model.Config) error {
	validDBTypes := map[model.DBType]bool{
		model.DBPostgreSQL: true,
		model.DBMySQL:      true,
		model.DBSQLite:     true,
	}

	if !validDBTypes[cfg.Database.Type] {
		return fmt.Errorf("unsupported database type: %s, must be one of: postgres, mysql, sqlite", cfg.Database.Type)
	}

	if cfg.Database.Type != model.DBSQLite && cfg.Database.DSN == "" {
		if cfg.Database.Host == "" {
			return fmt.Errorf("database host is required")
		}
		if cfg.Database.User == "" {
			return fmt.Errorf("database user is required")
		}
		if cfg.Database.DBName == "" {
			return fmt.Errorf("database name is required")
		}
	}

	return nil
}

func GetDSN(cfg *model.Config) string {
	if cfg.Database.DSN != "" {
		return cfg.Database.DSN
	}

	switch cfg.Database.Type {
	case model.DBPostgreSQL:
		return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			cfg.Database.Host, cfg.Database.Port, cfg.Database.User,
			cfg.Database.Password, cfg.Database.DBName, cfg.Database.SSLMode)
	case model.DBMySQL:
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True",
			cfg.Database.User, cfg.Database.Password,
			cfg.Database.Host, cfg.Database.Port, cfg.Database.DBName)
	case model.DBSQLite:
		return cfg.Database.DBName
	default:
		return ""
	}
}

func GetDefaultConfigPath() string {
	envPath := os.Getenv("SCHEMA_MIGRATE_CONFIG")
	if envPath != "" {
		return envPath
	}
	return "./schema-migrate.yaml"
}

func GetDBType(cfg *model.Config) model.DBType {
	return cfg.Database.Type
}

func IsMySQL(cfg *model.Config) bool {
	return cfg.Database.Type == model.DBMySQL
}

func IsPostgreSQL(cfg *model.Config) bool {
	return cfg.Database.Type == model.DBPostgreSQL
}

func IsSQLite(cfg *model.Config) bool {
	return cfg.Database.Type == model.DBSQLite
}

func GetQuotedIdentifier(cfg *model.Config, name string) string {
	switch cfg.Database.Type {
	case model.DBPostgreSQL, model.DBSQLite:
		return `"` + name + `"`
	case model.DBMySQL:
		return "`" + name + "`"
	default:
		return name
	}
}

func QuoteString(cfg *model.Config, s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
