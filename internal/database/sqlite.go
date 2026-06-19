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

type SQLite struct {
	BaseDatabase
	hasLock bool
}

func (s *SQLite) Connect(ctx context.Context, dsn string) error {
	return s.BaseDatabase.connectWithDriver(ctx, "sqlite3", dsn+"?_fk=1&_journal_mode=WAL")
}

func (s *SQLite) EnsureMigrationsTable(ctx context.Context) error {
	createTableSQL := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			execution_time_ms INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_schema_migrations_applied_at ON schema_migrations(applied_at);
	`
	_, err := s.Exec(ctx, createTableSQL)
	return err
}

func (s *SQLite) GetAppliedMigrations(ctx context.Context) ([]model.MigrationRecord, error) {
	query := `SELECT version, name, checksum, applied_at, execution_time_ms FROM schema_migrations ORDER BY version ASC`
	rows, err := s.Query(ctx, query)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return []model.MigrationRecord{}, nil
		}
		return nil, err
	}
	defer rows.Close()

	var records []model.MigrationRecord
	for rows.Next() {
		var r model.MigrationRecord
		var appliedAtStr string
		if err := rows.Scan(&r.Version, &r.Name, &r.Checksum, &appliedAtStr, &r.ExecutionTime); err != nil {
			return nil, err
		}
		r.AppliedAt = parseSQLiteTime(appliedAtStr)
		records = append(records, r)
	}
	return records, rows.Err()
}

func parseSQLiteTime(s string) time.Time {
	formats := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.ParseInLocation(f, s, time.Local); err == nil {
			return t
		}
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Now()
}

func (s *SQLite) RecordMigration(ctx context.Context, version, name, checksum string, executionTimeMs int64) error {
	query := `INSERT INTO schema_migrations (version, name, checksum, applied_at, execution_time_ms) VALUES (?, ?, ?, datetime('now'), ?)`
	_, err := s.Exec(ctx, query, version, name, checksum, executionTimeMs)
	return err
}

func (s *SQLite) UnrecordMigration(ctx context.Context, version string) error {
	query := `DELETE FROM schema_migrations WHERE version = ?`
	_, err := s.Exec(ctx, query, version)
	return err
}

func (s *SQLite) GetCurrentSchema(ctx context.Context) (*model.Schema, error) {
	schema := &model.Schema{}

	query := `SELECT name FROM sqlite_master WHERE type='table' AND name != 'schema_migrations' AND name NOT LIKE 'sqlite_%' ORDER BY name`
	rows, err := s.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	rows.Close()

	for _, tableName := range tables {
		table := model.Table{Name: tableName}
		if err := s.loadTableColumns(ctx, &table); err != nil {
			return nil, err
		}
		if err := s.loadTableIndexes(ctx, &table); err != nil {
			return nil, err
		}
		if err := s.loadTableForeignKeys(ctx, &table); err != nil {
			return nil, err
		}
		schema.Tables = append(schema.Tables, table)
	}

	return schema, nil
}

func (s *SQLite) loadTableColumns(ctx context.Context, table *model.Table) error {
	query := fmt.Sprintf(`PRAGMA table_info(%s)`, s.QuoteIdentifier(table.Name))
	rows, err := s.Query(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var col model.Column
		var cid int
		var notNull int
		var defaultValue sql.NullString
		var pk int

		if err := rows.Scan(&cid, &col.Name, &col.Type, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		col.Nullable = notNull == 0
		col.IsPrimaryKey = pk == 1
		if defaultValue.Valid && defaultValue.String != "" && defaultValue.String != "NULL" {
			col.DefaultValue = &defaultValue.String
		}
		col.AutoIncrement = strings.Contains(strings.ToLower(col.Type), "integer") && pk == 1
		table.Columns = append(table.Columns, col)
	}
	return rows.Err()
}

func (s *SQLite) loadTableIndexes(ctx context.Context, table *model.Table) error {
	query := fmt.Sprintf(`PRAGMA index_list(%s)`, s.QuoteIdentifier(table.Name))
	rows, err := s.Query(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()

	var indexes []struct {
		Name   string
		Unique int
		Origin string
	}
	for rows.Next() {
		var idx struct {
			Name   string
			Unique int
			Origin string
		}
		var seq, partial int
		if err := rows.Scan(&seq, &idx.Name, &idx.Unique, &idx.Origin, &partial); err != nil {
			return err
		}
		indexes = append(indexes, idx)
	}
	rows.Close()

	for _, idxInfo := range indexes {
		if idxInfo.Origin == "pk" || idxInfo.Origin == "u" {
			continue
		}
		colQuery := fmt.Sprintf(`PRAGMA index_info(%s)`, s.QuoteIdentifier(idxInfo.Name))
		colRows, err := s.Query(ctx, colQuery)
		if err != nil {
			return err
		}
		var columns []string
		for colRows.Next() {
			var seqno, cid int
			var colName string
			if err := colRows.Scan(&seqno, &cid, &colName); err != nil {
				colRows.Close()
				return err
			}
			columns = append(columns, colName)
		}
		colRows.Close()

		table.Indexes = append(table.Indexes, model.Index{
			Name:    idxInfo.Name,
			Columns: columns,
			Unique:  idxInfo.Unique == 1,
			Type:    "btree",
		})
	}
	return nil
}

func (s *SQLite) loadTableForeignKeys(ctx context.Context, table *model.Table) error {
	query := fmt.Sprintf(`PRAGMA foreign_key_list(%s)`, s.QuoteIdentifier(table.Name))
	rows, err := s.Query(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var fk model.ForeignKey
		var id, seq int
		var col, refCol, match string
		if err := rows.Scan(&id, &seq, &fk.RefTable, &col, &refCol, &fk.OnDelete, &fk.OnUpdate, &match); err != nil {
			return err
		}
		fk.Name = fmt.Sprintf("fk_%s_%s_%d", table.Name, fk.RefTable, id)
		fk.Columns = append(fk.Columns, col)
		fk.RefColumns = append(fk.RefColumns, refCol)
		table.ForeignKeys = append(table.ForeignKeys, fk)
	}
	return rows.Err()
}

func (s *SQLite) AcquireAdvisoryLock(ctx context.Context, timeout time.Duration, retryInterval time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := s.Exec(ctx, "BEGIN IMMEDIATE")
		if err == nil {
			s.hasLock = true
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(retryInterval):
		}
	}
	return false, fmt.Errorf("timeout waiting for database lock")
}

func (s *SQLite) ReleaseAdvisoryLock(ctx context.Context) error {
	if !s.hasLock {
		return nil
	}
	_, err := s.Exec(ctx, "COMMIT")
	if err == nil {
		s.hasLock = false
	}
	return err
}

func (s *SQLite) GetAutoIncrementKeyword() string {
	return "AUTOINCREMENT"
}

func (s *SQLite) GetColumnTypeSQL(col model.Column) string {
	return col.Type
}

func (s *SQLite) GetCreateTableSQL(table model.Table) string {
	var colDefs []string
	for _, col := range table.Columns {
		def := s.getColumnDefinition(col, s.GetColumnTypeSQL)
		if col.AutoIncrement {
			def = s.QuoteIdentifier(col.Name) + " INTEGER PRIMARY KEY " + s.GetAutoIncrementKeyword()
			if !col.Nullable {
				def += " NOT NULL"
			}
		} else if col.IsPrimaryKey {
			def += " PRIMARY KEY"
		}
		colDefs = append(colDefs, def)
	}

	for _, uc := range table.UniqueConstraints {
		cols := make([]string, len(uc.Columns))
		for i, c := range uc.Columns {
			cols[i] = s.QuoteIdentifier(c)
		}
		namePart := ""
		if uc.Name != "" {
			namePart = " CONSTRAINT " + s.QuoteIdentifier(uc.Name)
		}
		colDefs = append(colDefs, fmt.Sprintf("%s UNIQUE (%s)", namePart, strings.Join(cols, ", ")))
	}

	for _, cc := range table.CheckConstraints {
		namePart := ""
		if cc.Name != "" {
			namePart = " CONSTRAINT " + s.QuoteIdentifier(cc.Name)
		}
		colDefs = append(colDefs, fmt.Sprintf("%s CHECK (%s)", namePart, cc.Expression))
	}

	for _, fk := range table.ForeignKeys {
		cols := make([]string, len(fk.Columns))
		refCols := make([]string, len(fk.RefColumns))
		for i, c := range fk.Columns {
			cols[i] = s.QuoteIdentifier(c)
		}
		for i, c := range fk.RefColumns {
			refCols[i] = s.QuoteIdentifier(c)
		}
		namePart := ""
		if fk.Name != "" {
			namePart = " CONSTRAINT " + s.QuoteIdentifier(fk.Name)
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
		colDefs = append(colDefs, fmt.Sprintf("%s FOREIGN KEY (%s) REFERENCES %s (%s)%s",
			namePart, strings.Join(cols, ", "), s.QuoteIdentifier(fk.RefTable), strings.Join(refCols, ", "), onClause))
	}

	return fmt.Sprintf("CREATE TABLE %s (\n  %s\n);", s.QuoteIdentifier(table.Name), strings.Join(colDefs, ",\n  "))
}

func (s *SQLite) GetCreateIndexSQL(tableName string, idx model.Index) string {
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		cols[i] = s.QuoteIdentifier(c)
	}
	unique := ""
	if idx.Unique {
		unique = "UNIQUE "
	}
	name := idx.Name
	if name == "" {
		name = fmt.Sprintf("idx_%s_%s", tableName, strings.Join(idx.Columns, "_"))
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s);", 
		unique, s.QuoteIdentifier(name), s.QuoteIdentifier(tableName), strings.Join(cols, ", "))
}

func (s *SQLite) GetDropIndexSQL(tableName string, idx model.Index) string {
	return fmt.Sprintf("DROP INDEX %s;", s.QuoteIdentifier(idx.Name))
}

func (s *SQLite) GetAddColumnSQL(tableName string, col model.Column) string {
	def := s.getColumnDefinition(col, s.GetColumnTypeSQL)
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", s.QuoteIdentifier(tableName), def)
}

func (s *SQLite) GetDropColumnSQL(tableName string, columnName string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", s.QuoteIdentifier(tableName), s.QuoteIdentifier(columnName))
}

func (s *SQLite) GetAlterColumnTypeSQL(tableName string, oldCol, newCol model.Column) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME TO %s_old;\n"+
		"%s\n"+
		"INSERT INTO %s SELECT %s FROM %s_old;\n"+
		"DROP TABLE %s_old;",
		s.QuoteIdentifier(tableName), s.QuoteIdentifier(tableName),
		s.recreateTableSQLWithNewColumn(tableName, oldCol, newCol),
		s.QuoteIdentifier(tableName),
		s.buildColumnListForRecreate(tableName, oldCol, newCol),
		s.QuoteIdentifier(tableName),
		s.QuoteIdentifier(tableName))
}

func (s *SQLite) recreateTableSQLWithNewColumn(tableName string, oldCol, newCol model.Column) string {
	var newColumns []model.Column
	query := fmt.Sprintf(`PRAGMA table_info(%s)`, s.QuoteIdentifier(tableName))
	rows, _ := s.Query(context.Background(), query)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var col model.Column
			var cid int
			var notNull int
			var defaultValue sql.NullString
			var pk int
			var colType string
			rows.Scan(&cid, &col.Name, &colType, &notNull, &defaultValue, &pk)
			if col.Name == oldCol.Name {
				newColumns = append(newColumns, newCol)
			} else {
				col.Type = colType
				col.Nullable = notNull == 0
				col.IsPrimaryKey = pk == 1
				if defaultValue.Valid && defaultValue.String != "" {
					col.DefaultValue = &defaultValue.String
				}
				newColumns = append(newColumns, col)
			}
		}
	}

	var colDefs []string
	for _, col := range newColumns {
		def := s.getColumnDefinition(col, s.GetColumnTypeSQL)
		if col.IsPrimaryKey {
			def += " PRIMARY KEY"
		}
		colDefs = append(colDefs, def)
	}
	return fmt.Sprintf("CREATE TABLE %s (\n  %s\n);", s.QuoteIdentifier(tableName), strings.Join(colDefs, ",\n  "))
}

func (s *SQLite) buildColumnListForRecreate(tableName string, oldCol, newCol model.Column) string {
	query := fmt.Sprintf(`PRAGMA table_info(%s)`, s.QuoteIdentifier(tableName))
	rows, _ := s.Query(context.Background(), query)
	var cols []string
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var colName string
			var cid, notNull, pk int
			var colType string
			var defaultValue sql.NullString
			rows.Scan(&cid, &colName, &colType, &notNull, &defaultValue, &pk)
			cols = append(cols, s.QuoteIdentifier(colName))
		}
	}
	return strings.Join(cols, ", ")
}

func (s *SQLite) GetAlterColumnDefaultSQL(tableName string, col model.Column) string {
	return s.GetAlterColumnTypeSQL(tableName, col, col)
}

func (s *SQLite) GetAlterColumnNullSQL(tableName string, col model.Column) string {
	return s.GetAlterColumnTypeSQL(tableName, col, col)
}

func (s *SQLite) GetAddForeignKeySQL(tableName string, fk model.ForeignKey) string {
	cols := make([]string, len(fk.Columns))
	refCols := make([]string, len(fk.RefColumns))
	for i, c := range fk.Columns {
		cols[i] = s.QuoteIdentifier(c)
	}
	for i, c := range fk.RefColumns {
		refCols[i] = s.QuoteIdentifier(c)
	}
	namePart := ""
	if fk.Name != "" {
		namePart = " CONSTRAINT " + s.QuoteIdentifier(fk.Name)
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
		s.QuoteIdentifier(tableName), namePart, strings.Join(cols, ", "),
		s.QuoteIdentifier(fk.RefTable), strings.Join(refCols, ", "), onClause)
}

func (s *SQLite) GetDropForeignKeySQL(tableName string, fk model.ForeignKey) string {
	return fmt.Sprintf("-- SQLite requires table recreation to drop foreign key\n"+
		"-- The following SQL recreates the table without the foreign key %s\n"+
		"ALTER TABLE %s RENAME TO %s_old;\n"+
		"CREATE TABLE %s AS SELECT * FROM %s_old WHERE 1=0;\n"+
		"INSERT INTO %s SELECT * FROM %s_old;\n"+
		"DROP TABLE %s_old;",
		fk.Name,
		s.QuoteIdentifier(tableName), s.QuoteIdentifier(tableName),
		s.QuoteIdentifier(tableName), s.QuoteIdentifier(tableName),
		s.QuoteIdentifier(tableName), s.QuoteIdentifier(tableName),
		s.QuoteIdentifier(tableName))
}

func (s *SQLite) GetAddUniqueConstraintSQL(tableName string, uc model.UniqueConstraint) string {
	cols := make([]string, len(uc.Columns))
	for i, c := range uc.Columns {
		cols[i] = s.QuoteIdentifier(c)
	}
	name := uc.Name
	if name == "" {
		name = fmt.Sprintf("%s_%s_unique", tableName, strings.Join(uc.Columns, "_"))
	}
	return fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s);",
		s.QuoteIdentifier(name), s.QuoteIdentifier(tableName), strings.Join(cols, ", "))
}

func (s *SQLite) GetDropUniqueConstraintSQL(tableName string, uc model.UniqueConstraint) string {
	return fmt.Sprintf("DROP INDEX %s;", s.QuoteIdentifier(uc.Name))
}

func (s *SQLite) GetAddCheckConstraintSQL(tableName string, cc model.CheckConstraint) string {
	return fmt.Sprintf("-- SQLite requires table recreation to add check constraints\n"+
		"ALTER TABLE %s RENAME TO %s_old;\n"+
		"CREATE TABLE %s (\n  -- original columns plus new check constraint\n  CHECK (%s)\n);\n"+
		"INSERT INTO %s SELECT * FROM %s_old;\n"+
		"DROP TABLE %s_old;",
		s.QuoteIdentifier(tableName), s.QuoteIdentifier(tableName),
		s.QuoteIdentifier(tableName), cc.Expression,
		s.QuoteIdentifier(tableName), s.QuoteIdentifier(tableName),
		s.QuoteIdentifier(tableName))
}

func (s *SQLite) GetDropCheckConstraintSQL(tableName string, cc model.CheckConstraint) string {
	return fmt.Sprintf("-- SQLite requires table recreation to drop check constraints\n"+
		"ALTER TABLE %s RENAME TO %s_old;\n"+
		"CREATE TABLE %s AS SELECT * FROM %s_old WHERE 1=0;\n"+
		"INSERT INTO %s SELECT * FROM %s_old;\n"+
		"DROP TABLE %s_old;",
		s.QuoteIdentifier(tableName), s.QuoteIdentifier(tableName),
		s.QuoteIdentifier(tableName), s.QuoteIdentifier(tableName),
		s.QuoteIdentifier(tableName), s.QuoteIdentifier(tableName),
		s.QuoteIdentifier(tableName))
}

func (s *SQLite) GetDropTableSQL(tableName string) string {
	return fmt.Sprintf("DROP TABLE %s;", s.QuoteIdentifier(tableName))
}

func (s *SQLite) isTypeNarrowing(oldType, newType string) bool {
	varcRegex := regexp.MustCompile(`(?i)varchar\((\d+)\)`)
	oldMatch := varcRegex.FindStringSubmatch(oldType)
	newMatch := varcRegex.FindStringSubmatch(newType)
	if oldMatch != nil && newMatch != nil {
		oldLen, _ := strconv.Atoi(oldMatch[1])
		newLen, _ := strconv.Atoi(newMatch[1])
		return newLen < oldLen
	}
	return false
}

func (s *SQLite) EnsureSeedsTable(ctx context.Context) error {
	createTableSQL := `
		CREATE TABLE IF NOT EXISTS schema_seeds (
			version TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			environment TEXT NOT NULL DEFAULT '',
			tables TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_schema_seeds_applied_at ON schema_seeds(applied_at);
		CREATE INDEX IF NOT EXISTS idx_schema_seeds_environment ON schema_seeds(environment);
	`
	_, err := s.Exec(ctx, createTableSQL)
	return err
}

func (s *SQLite) GetAppliedSeeds(ctx context.Context) ([]model.SeedRecord, error) {
	query := `SELECT version, name, checksum, applied_at, environment, tables FROM schema_seeds ORDER BY version ASC`
	rows, err := s.Query(ctx, query)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return []model.SeedRecord{}, nil
		}
		return nil, err
	}
	defer rows.Close()

	var records []model.SeedRecord
	for rows.Next() {
		var r model.SeedRecord
		var appliedAtStr string
		if err := rows.Scan(&r.Version, &r.Name, &r.Checksum, &appliedAtStr, &r.Environment, &r.Tables); err != nil {
			return nil, err
		}
		r.AppliedAt = parseSQLiteTime(appliedAtStr)
		records = append(records, r)
	}
	return records, rows.Err()
}

func (s *SQLite) RecordSeed(ctx context.Context, version, name, checksum, environment, tables string) error {
	query := `INSERT INTO schema_seeds (version, name, checksum, applied_at, environment, tables) VALUES (?, ?, ?, datetime('now'), ?, ?)`
	_, err := s.Exec(ctx, query, version, name, checksum, environment, tables)
	return err
}

func (s *SQLite) UnrecordSeed(ctx context.Context, version string) error {
	query := `DELETE FROM schema_seeds WHERE version = ?`
	_, err := s.Exec(ctx, query, version)
	return err
}

func (s *SQLite) UnrecordAllSeeds(ctx context.Context) error {
	query := `DELETE FROM schema_seeds`
	_, err := s.Exec(ctx, query)
	return err
}

func (s *SQLite) GetTruncateSQL(tableName string) string {
	return s.GetDeleteFromSQL(tableName)
}
