package model

import "time"

type DBType string

const (
	DBPostgreSQL DBType = "postgres"
	DBMySQL      DBType = "mysql"
	DBSQLite     DBType = "sqlite"
)

type Column struct {
	Name         string
	Type         string
	Nullable     bool
	DefaultValue *string
	IsPrimaryKey bool
	IsUnique     bool
	AutoIncrement bool
	Comment      string
}

type Index struct {
	Name    string
	Columns []string
	Unique  bool
	Type    string
}

type ForeignKey struct {
	Name       string
	Columns    []string
	RefTable   string
	RefColumns []string
	OnDelete   string
	OnUpdate   string
}

type CheckConstraint struct {
	Name       string
	Expression string
}

type UniqueConstraint struct {
	Name    string
	Columns []string
}

type Table struct {
	Name              string
	Columns           []Column
	Indexes           []Index
	ForeignKeys       []ForeignKey
	CheckConstraints  []CheckConstraint
	UniqueConstraints []UniqueConstraint
	Comment           string
}

type Schema struct {
	Tables []Table
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
	Schema struct {
		FilePath string `yaml:"file"`
	} `yaml:"schema"`
	Concurrency struct {
		LockTimeout  int `yaml:"lock_timeout_seconds"`
		RetryInterval int `yaml:"retry_interval_ms"`
	} `yaml:"concurrency"`
}
