package database

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/schema-migrate/schema-migrate/internal/model"
)

const mySQLAdvisoryLockKey = 987654321

type MySQL struct {
	BaseDatabase
	hasLock bool
}

func (m *MySQL) Connect(ctx context.Context, dsn string) error {
	return m.BaseDatabase.connectWithDriver(ctx, "mysql", dsn)
}

func (m *MySQL) EnsureMigrationsTable(ctx context.Context) error {
	createTableSQL := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version VARCHAR(255) PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			checksum VARCHAR(64) NOT NULL,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			execution_time_ms BIGINT NOT NULL DEFAULT 0
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
		CREATE INDEX IF NOT EXISTS idx_schema_migrations_applied_at ON schema_migrations(applied_at);
	`
	_, err := m.Exec(ctx, createTableSQL)
	return err
}

func (m *MySQL) GetAppliedMigrations(ctx context.Context) ([]model.MigrationRecord, error) {
	query := `SELECT version, name, checksum, applied_at, execution_time_ms FROM schema_migrations ORDER BY version ASC`
	rows, err := m.Query(ctx, query)
	if err != nil {
		if strings.Contains(err.Error(), "doesn't exist") {
			return []model.MigrationRecord{}, nil
		}
		return nil, err
	}
	defer rows.Close()

	var records []model.MigrationRecord
	for rows.Next() {
		var r model.MigrationRecord
		if err := rows.Scan(&r.Version, &r.Name, &r.Checksum, &r.AppliedAt, &r.ExecutionTime); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func (m *MySQL) RecordMigration(ctx context.Context, version, name, checksum string, executionTimeMs int64) error {
	query := `INSERT INTO schema_migrations (version, name, checksum, applied_at, execution_time_ms) VALUES (?, ?, ?, NOW(), ?)`
	_, err := m.Exec(ctx, query, version, name, checksum, executionTimeMs)
	return err
}

func (m *MySQL) UnrecordMigration(ctx context.Context, version string) error {
	query := `DELETE FROM schema_migrations WHERE version = ?`
	_, err := m.Exec(ctx, query, version)
	return err
}

func (m *MySQL) GetCurrentSchema(ctx context.Context) (*model.Schema, error) {
	schema := &model.Schema{}

	tables, err := m.getTables(ctx)
	if err != nil {
		return nil, err
	}

	for i := range tables {
		table := &tables[i]
		if err := m.loadTableColumns(ctx, table); err != nil {
			return nil, err
		}
		if err := m.loadTableIndexes(ctx, table); err != nil {
			return nil, err
		}
		if err := m.loadTableForeignKeys(ctx, table); err != nil {
			return nil, err
		}
		if err := m.loadTableConstraints(ctx, table); err != nil {
			return nil, err
		}
		schema.Tables = append(schema.Tables, *table)
	}

	return schema, nil
}

func (m *MySQL) getTables(ctx context.Context) ([]model.Table, error) {
	var dbName string
	err := m.QueryRow(ctx, "SELECT DATABASE()").Scan(&dbName)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT table_name, table_comment
		FROM information_schema.tables
		WHERE table_schema = ?
		AND table_type = 'BASE TABLE'
		AND table_name != 'schema_migrations'
		ORDER BY table_name
	`
	rows, err := m.Query(ctx, query, dbName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []model.Table
	for rows.Next() {
		var name, comment string
		if err := rows.Scan(&name, &comment); err != nil {
			return nil, err
		}
		tables = append(tables, model.Table{Name: name, Comment: comment})
	}
	return tables, rows.Err()
}

func (m *MySQL) loadTableColumns(ctx context.Context, table *model.Table) error {
	var dbName string
	err := m.QueryRow(ctx, "SELECT DATABASE()").Scan(&dbName)
	if err != nil {
		return err
	}

	query := `
		SELECT 
			c.column_name,
			c.column_type,
			c.data_type,
			c.is_nullable = 'YES',
			c.column_default,
			c.extra,
			c.column_comment,
			EXISTS (
				SELECT 1 FROM information_schema.table_constraints tc
				JOIN information_schema.key_column_usage kcu 
					ON tc.constraint_name = kcu.constraint_name
				WHERE tc.table_schema = c.table_schema
				AND tc.table_name = c.table_name 
				AND kcu.column_name = c.column_name
				AND tc.constraint_type = 'PRIMARY KEY'
			) as is_primary_key,
			EXISTS (
				SELECT 1 FROM information_schema.table_constraints tc
				JOIN information_schema.key_column_usage kcu 
					ON tc.constraint_name = kcu.constraint_name
				WHERE tc.table_schema = c.table_schema
				AND tc.table_name = c.table_name 
				AND kcu.column_name = c.column_name
				AND tc.constraint_type = 'UNIQUE'
			) as is_unique
		FROM information_schema.columns c
		WHERE c.table_schema = ? AND c.table_name = ?
		ORDER BY c.ordinal_position
	`
	rows, err := m.Query(ctx, query, dbName, table.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var col model.Column
		var columnType, dataType, extra, comment string
		var defaultValue sql.NullString

		if err := rows.Scan(&col.Name, &columnType, &dataType, &col.Nullable, &defaultValue, 
			&extra, &comment, &col.IsPrimaryKey, &col.IsUnique); err != nil {
			return err
		}

		col.Type = columnType
		col.Comment = comment
		col.AutoIncrement = strings.Contains(extra, "auto_increment")
		if defaultValue.Valid && defaultValue.String != "" {
			col.DefaultValue = &defaultValue.String
		}

		table.Columns = append(table.Columns, col)
	}
	return rows.Err()
}

func (m *MySQL) loadTableIndexes(ctx context.Context, table *model.Table) error {
	var dbName string
	err := m.QueryRow(ctx, "SELECT DATABASE()").Scan(&dbName)
	if err != nil {
		return err
	}

	query := `
		SELECT 
			index_name,
			GROUP_CONCAT(column_name ORDER BY seq_in_index) as columns,
			non_unique = 0 as is_unique,
			index_type
		FROM information_schema.statistics
		WHERE table_schema = ? AND table_name = ?
		AND index_name != 'PRIMARY'
		AND NOT EXISTS (
			SELECT 1 FROM information_schema.table_constraints tc
			WHERE tc.table_schema = ? 
			AND tc.table_name = ? 
			AND tc.constraint_name = statistics.index_name
			AND tc.constraint_type = 'FOREIGN KEY'
		)
		GROUP BY index_name, is_unique, index_type
	`
	rows, err := m.Query(ctx, query, dbName, table.Name, dbName, table.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var idx model.Index
		var columns string

		if err := rows.Scan(&idx.Name, &columns, &idx.Unique, &idx.Type); err != nil {
			return err
		}
		idx.Columns = strings.Split(columns, ",")
		table.Indexes = append(table.Indexes, idx)
	}
	return rows.Err()
}

func (m *MySQL) loadTableForeignKeys(ctx context.Context, table *model.Table) error {
	var dbName string
	err := m.QueryRow(ctx, "SELECT DATABASE()").Scan(&dbName)
	if err != nil {
		return err
	}

	query := `
		SELECT
			tc.constraint_name,
			GROUP_CONCAT(kcu.column_name ORDER BY kcu.ordinal_position) as columns,
			ccu.table_name AS foreign_table_name,
			GROUP_CONCAT(ccu.column_name ORDER BY kcu.ordinal_position) as foreign_column_names,
			rc.update_rule,
			rc.delete_rule
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name
			AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage ccu
			ON ccu.constraint_name = tc.constraint_name
			AND ccu.table_schema = tc.table_schema
		JOIN information_schema.referential_constraints rc
			ON tc.constraint_name = rc.constraint_name
			AND tc.table_schema = rc.constraint_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		AND tc.table_schema = ?
		AND tc.table_name = ?
		GROUP BY tc.constraint_name, ccu.table_name, rc.update_rule, rc.delete_rule
	`
	rows, err := m.Query(ctx, query, dbName, table.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var fk model.ForeignKey
		var columns, refColumns string
		if err := rows.Scan(&fk.Name, &columns, &fk.RefTable, &refColumns, &fk.OnUpdate, &fk.OnDelete); err != nil {
			return err
		}
		fk.Columns = strings.Split(columns, ",")
		fk.RefColumns = strings.Split(refColumns, ",")
		table.ForeignKeys = append(table.ForeignKeys, fk)
	}
	return rows.Err()
}

func (m *MySQL) loadTableConstraints(ctx context.Context, table *model.Table) error {
	var dbName string
	err := m.QueryRow(ctx, "SELECT DATABASE()").Scan(&dbName)
	if err != nil {
		return err
	}

	ucQuery := `
		SELECT tc.constraint_name, 
		       GROUP_CONCAT(kcu.column_name ORDER BY kcu.ordinal_position)
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu 
			ON tc.constraint_name = kcu.constraint_name
			AND tc.table_schema = kcu.table_schema
		WHERE tc.table_schema = ? AND tc.table_name = ? 
		AND tc.constraint_type = 'UNIQUE'
		AND tc.constraint_name != 'PRIMARY'
		GROUP BY tc.constraint_name
	`
	rows, err := m.Query(ctx, ucQuery, dbName, table.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var uc model.UniqueConstraint
		var columns string
		if err := rows.Scan(&uc.Name, &columns); err != nil {
			return err
		}
		uc.Columns = strings.Split(columns, ",")
		table.UniqueConstraints = append(table.UniqueConstraints, uc)
	}
	rows.Close()

	ccQuery := `
		SELECT tc.constraint_name, cc.check_clause
		FROM information_schema.table_constraints tc
		JOIN information_schema.check_constraints cc
			ON tc.constraint_name = cc.constraint_name
			AND tc.constraint_schema = cc.constraint_schema
		WHERE tc.table_schema = ? AND tc.table_name = ?
		AND tc.constraint_type = 'CHECK'
	`
	rows, err = m.Query(ctx, ccQuery, dbName, table.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cc model.CheckConstraint
		var expr string
		if err := rows.Scan(&cc.Name, &expr); err != nil {
			return err
		}
		cc.Expression = expr
		table.CheckConstraints = append(table.CheckConstraints, cc)
	}

	return rows.Err()
}

func (m *MySQL) AcquireAdvisoryLock(ctx context.Context, timeout time.Duration, retryInterval time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var result int
		query := `SELECT GET_LOCK(?, ?)`
		lockName := fmt.Sprintf("schema_migrate_%d", mySQLAdvisoryLockKey)
		err := m.QueryRow(ctx, query, lockName, 1).Scan(&result)
		if err != nil {
			return false, err
		}
		if result == 1 {
			m.hasLock = true
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(retryInterval):
		}
	}
	return false, fmt.Errorf("timeout waiting for advisory lock")
}

func (m *MySQL) ReleaseAdvisoryLock(ctx context.Context) error {
	if !m.hasLock {
		return nil
	}
	var result int
	lockName := fmt.Sprintf("schema_migrate_%d", mySQLAdvisoryLockKey)
	query := `SELECT RELEASE_LOCK(?)`
	err := m.QueryRow(ctx, query, lockName).Scan(&result)
	if err == nil && result == 1 {
		m.hasLock = false
	}
	return err
}

func (m *MySQL) GetAutoIncrementKeyword() string {
	return "AUTO_INCREMENT"
}

func (m *MySQL) GetColumnTypeSQL(col model.Column) string {
	return col.Type
}

func (m *MySQL) GetCreateTableSQL(table model.Table) string {
	var colDefs []string
	var primaryKeys []string

	for _, col := range table.Columns {
		def := m.getColumnDefinition(col, m.GetColumnTypeSQL)
		if col.AutoIncrement {
			def += " " + m.GetAutoIncrementKeyword()
		}
		if col.IsPrimaryKey {
			primaryKeys = append(primaryKeys, m.QuoteIdentifier(col.Name))
		}
		colDefs = append(colDefs, def)
	}

	if len(primaryKeys) > 0 {
		colDefs = append(colDefs, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(primaryKeys, ", ")))
	}

	for _, uc := range table.UniqueConstraints {
		cols := make([]string, len(uc.Columns))
		for i, c := range uc.Columns {
			cols[i] = m.QuoteIdentifier(c)
		}
		namePart := ""
		if uc.Name != "" {
			namePart = " " + m.QuoteIdentifier(uc.Name)
		}
		colDefs = append(colDefs, fmt.Sprintf("UNIQUE%s (%s)", namePart, strings.Join(cols, ", ")))
	}

	for _, cc := range table.CheckConstraints {
		namePart := ""
		if cc.Name != "" {
			namePart = " CONSTRAINT " + m.QuoteIdentifier(cc.Name)
		}
		colDefs = append(colDefs, fmt.Sprintf("%s CHECK (%s)", namePart, cc.Expression))
	}

	tableOptions := " ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"
	if table.Comment != "" {
		tableOptions += fmt.Sprintf(" COMMENT=%s", m.QuoteString(table.Comment))
	}

	return fmt.Sprintf("CREATE TABLE %s (\n  %s\n)%s;", m.QuoteIdentifier(table.Name), strings.Join(colDefs, ",\n  "), tableOptions)
}

func (m *MySQL) GetCreateIndexSQL(tableName string, idx model.Index) string {
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		cols[i] = m.QuoteIdentifier(c)
	}
	unique := ""
	if idx.Unique {
		unique = "UNIQUE "
	}
	indexType := ""
	if idx.Type != "" && idx.Type != "BTREE" {
		indexType = " USING " + idx.Type
	}
	name := idx.Name
	if name == "" {
		name = fmt.Sprintf("idx_%s_%s", tableName, strings.Join(idx.Columns, "_"))
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s%s (%s);", 
		unique, m.QuoteIdentifier(name), m.QuoteIdentifier(tableName), indexType, strings.Join(cols, ", "))
}

func (m *MySQL) GetDropIndexSQL(tableName string, idx model.Index) string {
	return fmt.Sprintf("DROP INDEX %s ON %s;", m.QuoteIdentifier(idx.Name), m.QuoteIdentifier(tableName))
}

func (m *MySQL) GetAddColumnSQL(tableName string, col model.Column) string {
	def := m.getColumnDefinition(col, m.GetColumnTypeSQL)
	if col.AutoIncrement {
		def += " " + m.GetAutoIncrementKeyword()
	}
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", m.QuoteIdentifier(tableName), def)
}

func (m *MySQL) GetDropColumnSQL(tableName string, columnName string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", m.QuoteIdentifier(tableName), m.QuoteIdentifier(columnName))
}

func (m *MySQL) GetAlterColumnTypeSQL(tableName string, oldCol, newCol model.Column) string {
	def := m.getColumnDefinition(newCol, m.GetColumnTypeSQL)
	return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;", 
		m.QuoteIdentifier(tableName), def)
}

func (m *MySQL) GetAlterColumnDefaultSQL(tableName string, col model.Column) string {
	if col.DefaultValue != nil {
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;",
			m.QuoteIdentifier(tableName), m.QuoteIdentifier(col.Name), *col.DefaultValue)
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;",
		m.QuoteIdentifier(tableName), m.QuoteIdentifier(col.Name))
}

func (m *MySQL) GetAlterColumnNullSQL(tableName string, col model.Column) string {
	def := m.getColumnDefinition(col, m.GetColumnTypeSQL)
	return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;",
		m.QuoteIdentifier(tableName), def)
}

func (m *MySQL) GetAddForeignKeySQL(tableName string, fk model.ForeignKey) string {
	cols := make([]string, len(fk.Columns))
	refCols := make([]string, len(fk.RefColumns))
	for i, c := range fk.Columns {
		cols[i] = m.QuoteIdentifier(c)
	}
	for i, c := range fk.RefColumns {
		refCols[i] = m.QuoteIdentifier(c)
	}
	namePart := ""
	if fk.Name != "" {
		namePart = " CONSTRAINT " + m.QuoteIdentifier(fk.Name)
	}
	onParts := []string{}
	if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
		onParts = append(onParts, "ON DELETE "+fk.OnDelete)
	}
	if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
		onParts = append(onParts, "ON UPDATE "+fk.OnUpdate)
	}
	onClause := ""
	if len(onParts) > 0 {
		onClause = " " + strings.Join(onParts, " ")
	}
	return fmt.Sprintf("ALTER TABLE %s ADD%s FOREIGN KEY (%s) REFERENCES %s (%s)%s;",
		m.QuoteIdentifier(tableName), namePart, strings.Join(cols, ", "),
		m.QuoteIdentifier(fk.RefTable), strings.Join(refCols, ", "), onClause)
}

func (m *MySQL) GetDropForeignKeySQL(tableName string, fk model.ForeignKey) string {
	return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s;",
		m.QuoteIdentifier(tableName), m.QuoteIdentifier(fk.Name))
}

func (m *MySQL) GetAddUniqueConstraintSQL(tableName string, uc model.UniqueConstraint) string {
	cols := make([]string, len(uc.Columns))
	for i, c := range uc.Columns {
		cols[i] = m.QuoteIdentifier(c)
	}
	name := uc.Name
	if name == "" {
		name = fmt.Sprintf("%s_%s_unique", tableName, strings.Join(uc.Columns, "_"))
	}
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s);",
		m.QuoteIdentifier(tableName), m.QuoteIdentifier(name), strings.Join(cols, ", "))
}

func (m *MySQL) GetDropUniqueConstraintSQL(tableName string, uc model.UniqueConstraint) string {
	return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;",
		m.QuoteIdentifier(tableName), m.QuoteIdentifier(uc.Name))
}

func (m *MySQL) GetAddCheckConstraintSQL(tableName string, cc model.CheckConstraint) string {
	name := cc.Name
	if name == "" {
		name = fmt.Sprintf("%s_check_%d", tableName, time.Now().Unix())
	}
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s);",
		m.QuoteIdentifier(tableName), m.QuoteIdentifier(name), cc.Expression)
}

func (m *MySQL) GetDropCheckConstraintSQL(tableName string, cc model.CheckConstraint) string {
	return fmt.Sprintf("ALTER TABLE %s DROP CHECK %s;",
		m.QuoteIdentifier(tableName), m.QuoteIdentifier(cc.Name))
}

func (m *MySQL) GetDropTableSQL(tableName string) string {
	return fmt.Sprintf("DROP TABLE %s;", m.QuoteIdentifier(tableName))
}

func (m *MySQL) isTypeNarrowing(oldType, newType string) bool {
	varcRegex := regexp.MustCompile(`(?i)varchar\((\d+)\)`)
	oldMatch := varcRegex.FindStringSubmatch(oldType)
	newMatch := varcRegex.FindStringSubmatch(newType)
	if oldMatch != nil && newMatch != nil {
		oldLen, _ := strconv.Atoi(oldMatch[1])
		newLen, _ := strconv.Atoi(newMatch[1])
		return newLen < oldLen
	}

	charRegex := regexp.MustCompile(`(?i)char\((\d+)\)`)
	oldMatch = charRegex.FindStringSubmatch(oldType)
	newMatch = charRegex.FindStringSubmatch(newType)
	if oldMatch != nil && newMatch != nil {
		oldLen, _ := strconv.Atoi(oldMatch[1])
		newLen, _ := strconv.Atoi(newMatch[1])
		return newLen < oldLen
	}

	decimalRegex := regexp.MustCompile(`(?i)decimal\((\d+),(\d+)\)`)
	oldMatch = decimalRegex.FindStringSubmatch(oldType)
	newMatch = decimalRegex.FindStringSubmatch(newType)
	if oldMatch != nil && newMatch != nil {
		oldPrec, _ := strconv.Atoi(oldMatch[1])
		newPrec, _ := strconv.Atoi(newMatch[1])
		return newPrec < oldPrec
	}

	intTypes := map[string]int{
		"tinyint":   1,
		"smallint":  2,
		"mediumint": 3,
		"int":       4,
		"integer":   4,
		"bigint":    5,
	}
	oldLower := strings.ToLower(oldType)
	newLower := strings.ToLower(newType)
	if oldRank, ok := intTypes[oldLower]; ok {
		if newRank, ok := intTypes[newLower]; ok {
			return newRank < oldRank
		}
	}

	return false
}

func (m *MySQL) EnsureSeedsTable(ctx context.Context) error {
	createTableSQL := `
		CREATE TABLE IF NOT EXISTS schema_seeds (
			version VARCHAR(255) PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			checksum VARCHAR(64) NOT NULL,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			environment VARCHAR(50) NOT NULL DEFAULT '',
			tables TEXT NOT NULL DEFAULT ''
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
		CREATE INDEX IF NOT EXISTS idx_schema_seeds_applied_at ON schema_seeds(applied_at);
		CREATE INDEX IF NOT EXISTS idx_schema_seeds_environment ON schema_seeds(environment);
	`
	_, err := m.Exec(ctx, createTableSQL)
	return err
}

func (m *MySQL) GetAppliedSeeds(ctx context.Context) ([]model.SeedRecord, error) {
	query := `SELECT version, name, checksum, applied_at, environment, tables FROM schema_seeds ORDER BY version ASC`
	rows, err := m.Query(ctx, query)
	if err != nil {
		if strings.Contains(err.Error(), "doesn't exist") {
			return []model.SeedRecord{}, nil
		}
		return nil, err
	}
	defer rows.Close()

	var records []model.SeedRecord
	for rows.Next() {
		var r model.SeedRecord
		if err := rows.Scan(&r.Version, &r.Name, &r.Checksum, &r.AppliedAt, &r.Environment, &r.Tables); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func (m *MySQL) RecordSeed(ctx context.Context, version, name, checksum, environment, tables string) error {
	query := `INSERT INTO schema_seeds (version, name, checksum, applied_at, environment, tables) VALUES (?, ?, ?, NOW(), ?, ?)`
	_, err := m.Exec(ctx, query, version, name, checksum, environment, tables)
	return err
}

func (m *MySQL) UnrecordSeed(ctx context.Context, version string) error {
	query := `DELETE FROM schema_seeds WHERE version = ?`
	_, err := m.Exec(ctx, query, version)
	return err
}

func (m *MySQL) UnrecordAllSeeds(ctx context.Context) error {
	query := `DELETE FROM schema_seeds`
	_, err := m.Exec(ctx, query)
	return err
}

func (m *MySQL) GetTruncateSQL(tableName string) string {
	return fmt.Sprintf("TRUNCATE TABLE %s;", m.QuoteIdentifier(tableName))
}
