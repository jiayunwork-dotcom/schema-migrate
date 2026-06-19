package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/schema-migrate/schema-migrate/internal/model"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

type Database interface {
	Connect(ctx context.Context, dsn string) error
	Close() error
	DB() *sql.DB
	Type() model.DBType
	Exec(ctx context.Context, sql string, args ...interface{}) (sql.Result, error)
	Query(ctx context.Context, sql string, args ...interface{}) (*sql.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) *sql.Row

	EnsureMigrationsTable(ctx context.Context) error
	GetAppliedMigrations(ctx context.Context) ([]model.MigrationRecord, error)
	RecordMigration(ctx context.Context, version, name, checksum string, executionTimeMs int64) error
	UnrecordMigration(ctx context.Context, version string) error

	GetCurrentSchema(ctx context.Context) (*model.Schema, error)
	EstimateRowCount(ctx context.Context, tableName string) (int64, error)

	AcquireAdvisoryLock(ctx context.Context, timeout time.Duration, retryInterval time.Duration) (bool, error)
	ReleaseAdvisoryLock(ctx context.Context) error

	QuoteIdentifier(name string) string
	QuoteString(s string) string
	GetAutoIncrementKeyword() string
	GetColumnTypeSQL(col model.Column) string
	GetCreateTableSQL(table model.Table) string
	GetCreateIndexSQL(tableName string, idx model.Index) string
	GetDropIndexSQL(tableName string, idx model.Index) string
	GetAddColumnSQL(tableName string, col model.Column) string
	GetDropColumnSQL(tableName string, columnName string) string
	GetAlterColumnTypeSQL(tableName string, oldCol, newCol model.Column) string
	GetAlterColumnDefaultSQL(tableName string, col model.Column) string
	GetAlterColumnNullSQL(tableName string, col model.Column) string
	GetAddForeignKeySQL(tableName string, fk model.ForeignKey) string
	GetDropForeignKeySQL(tableName string, fk model.ForeignKey) string
	GetAddUniqueConstraintSQL(tableName string, uc model.UniqueConstraint) string
	GetDropUniqueConstraintSQL(tableName string, uc model.UniqueConstraint) string
	GetAddCheckConstraintSQL(tableName string, cc model.CheckConstraint) string
	GetDropCheckConstraintSQL(tableName string, cc model.CheckConstraint) string
	GetDropTableSQL(tableName string) string
}

func New(dbType model.DBType) (Database, error) {
	switch dbType {
	case model.DBPostgreSQL:
		return &PostgreSQL{BaseDatabase: BaseDatabase{dbType: dbType}}, nil
	case model.DBMySQL:
		return &MySQL{BaseDatabase: BaseDatabase{dbType: dbType}}, nil
	case model.DBSQLite:
		return &SQLite{BaseDatabase: BaseDatabase{dbType: dbType}}, nil
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}
}

type BaseDatabase struct {
	db     *sql.DB
	dbType model.DBType
}

func (b *BaseDatabase) connectWithDriver(ctx context.Context, driver, dsn string) error {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	b.db = db
	return db.PingContext(ctx)
}

func (b *BaseDatabase) Close() error {
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

func (b *BaseDatabase) DB() *sql.DB {
	return b.db
}

func (b *BaseDatabase) Type() model.DBType {
	return b.dbType
}

func (b *BaseDatabase) Exec(ctx context.Context, sqlStr string, args ...interface{}) (sql.Result, error) {
	return b.db.ExecContext(ctx, sqlStr, args...)
}

func (b *BaseDatabase) Query(ctx context.Context, sqlStr string, args ...interface{}) (*sql.Rows, error) {
	return b.db.QueryContext(ctx, sqlStr, args...)
}

func (b *BaseDatabase) QueryRow(ctx context.Context, sqlStr string, args ...interface{}) *sql.Row {
	return b.db.QueryRowContext(ctx, sqlStr, args...)
}

func (b *BaseDatabase) QuoteIdentifier(name string) string {
	switch b.dbType {
	case model.DBPostgreSQL, model.DBSQLite:
		return `"` + name + `"`
	case model.DBMySQL:
		return "`" + name + "`"
	default:
		return name
	}
}

func (b *BaseDatabase) QuoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (b *BaseDatabase) EstimateRowCount(ctx context.Context, tableName string) (int64, error) {
	var count int64
	quotedTable := b.QuoteIdentifier(tableName)
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", quotedTable)
	err := b.QueryRow(ctx, query).Scan(&count)
	return count, err
}

func (b *BaseDatabase) getColumnDefinition(col model.Column, getTypeFn func(model.Column) string) string {
	var parts []string
	parts = append(parts, b.QuoteIdentifier(col.Name))
	parts = append(parts, getTypeFn(col))
	if !col.Nullable {
		parts = append(parts, "NOT NULL")
	} else {
		parts = append(parts, "NULL")
	}
	if col.DefaultValue != nil {
		parts = append(parts, "DEFAULT "+*col.DefaultValue)
	}
	return strings.Join(parts, " ")
}
