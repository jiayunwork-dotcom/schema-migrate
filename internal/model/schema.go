package model

import "time"

type DBType string

const (
	DBPostgreSQL DBType = "postgres"
	DBMySQL      DBType = "mysql"
	DBSQLite     DBType = "sqlite"
)

type Column struct {
	Name         string  `yaml:"name"`
	Type         string  `yaml:"type"`
	Nullable     bool    `yaml:"nullable"`
	DefaultValue *string `yaml:"default_value"`
	IsPrimaryKey bool    `yaml:"is_primary_key"`
	IsUnique     bool    `yaml:"is_unique"`
	AutoIncrement bool   `yaml:"autoincrement"`
	Comment      string  `yaml:"comment"`
}

type Index struct {
	Name    string   `yaml:"name"`
	Columns []string `yaml:"columns"`
	Unique  bool     `yaml:"unique"`
	Type    string   `yaml:"type"`
}

type ForeignKey struct {
	Name       string   `yaml:"name"`
	Columns    []string `yaml:"columns"`
	RefTable   string   `yaml:"ref_table"`
	RefColumns []string `yaml:"ref_columns"`
	OnDelete   string   `yaml:"on_delete"`
	OnUpdate   string   `yaml:"on_update"`
}

type CheckConstraint struct {
	Name       string `yaml:"name"`
	Expression string `yaml:"expression"`
}

type UniqueConstraint struct {
	Name    string   `yaml:"name"`
	Columns []string `yaml:"columns"`
}

type Table struct {
	Name              string             `yaml:"name"`
	Columns           []Column           `yaml:"columns"`
	Indexes           []Index            `yaml:"indexes"`
	ForeignKeys       []ForeignKey       `yaml:"foreign_keys"`
	CheckConstraints  []CheckConstraint  `yaml:"check_constraints"`
	UniqueConstraints []UniqueConstraint `yaml:"unique_constraints"`
	Comment           string             `yaml:"comment"`
}

type Schema struct {
	Tables []Table `yaml:"tables"`
}

type Migration struct {
	Version     string
	Name        string
	UpSQL       string
	DownSQL     string
	UpPath      string
	DownPath    string
	Checksum    string
	AppliedAt   *time.Time
	IsApplied   bool
	Order       int
}

type MigrationRecord struct {
	Version    string    `db:"version"`
	Name       string    `db:"name"`
	Checksum   string    `db:"checksum"`
	AppliedAt  time.Time `db:"applied_at"`
	ExecutionTime int64  `db:"execution_time_ms"`
}

type DiffChangeType string

const (
	ChangeAdd    DiffChangeType = "add"
	ChangeDrop   DiffChangeType = "drop"
	ChangeModify DiffChangeType = "modify"
	ChangeRename DiffChangeType = "rename"
)

type DiffRisk string

const (
	RiskSafe    DiffRisk = "safe"
	RiskWarning DiffRisk = "warning"
	RiskDanger  DiffRisk = "danger"
)

type DiffChange struct {
	Type      DiffChangeType
	Risk      DiffRisk
	SQL       string
	ObjectType string
	ObjectName string
	Details   string
}

type SchemaDiff struct {
	Changes []DiffChange
	SafeCount   int
	WarningCount int
	DangerCount int
}

type SecurityCheckResult struct {
	IsDangerous    bool
	Warnings       []SecurityWarning
	AffectedTables []TableImpact
}

type SecurityWarning struct {
	Level       DiffRisk
	Operation   string
	Description string
	TableName   string
}

type TableImpact struct {
	TableName    string
	EstimatedRows int64
	Operation    string
}

type DependencyNode struct {
	Version     string
	TableName   string
	DependsOn   []string
}

type TimestampConflict struct {
	ConflictingVersions []string
	SuggestedOrder      []string
	Message             string
}

type Seed struct {
	Version     string
	Name        string
	Environment string
	SQL         string
	Path        string
	Checksum    string
	AppliedAt   *time.Time
	IsApplied   bool
	Order       int
	Tables      []string
}

type SeedRecord struct {
	Version    string    `db:"version"`
	Name       string    `db:"name"`
	Checksum   string    `db:"checksum"`
	AppliedAt  time.Time `db:"applied_at"`
	Environment string   `db:"environment"`
	Tables     string    `db:"tables"`
}

type Config struct {
	Database struct {
		Type     DBType `yaml:"type"`
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
		DBName   string `yaml:"dbname"`
		SSLMode  string `yaml:"sslmode"`
		DSN      string `yaml:"dsn"`
	} `yaml:"database"`
	Migrations struct {
		Dir string `yaml:"dir"`
	} `yaml:"migrations"`
	Seeds struct {
		Dir        string `yaml:"dir"`
		DefaultEnv string `yaml:"default_env"`
	} `yaml:"seeds"`
	Schema struct {
		FilePath string `yaml:"file"`
	} `yaml:"schema"`
	Concurrency struct {
		LockTimeout   int `yaml:"lock_timeout_seconds"`
		RetryInterval int `yaml:"retry_interval_ms"`
	} `yaml:"concurrency"`
}
