package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/schema-migrate/schema-migrate/internal/model"
)

const advisoryLockKey = 987654321

type PostgreSQL struct {
	BaseDatabase
	hasLock bool
}

func (p *PostgreSQL) Connect(ctx context.Context, dsn string) error {
	return p.BaseDatabase.connectWithDriver(ctx, "postgres", dsn)
}

func (p *PostgreSQL) EnsureMigrationsTable(ctx context.Context) error {
	createTableSQL := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version VARCHAR(255) PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			checksum VARCHAR(64) NOT NULL,
			applied_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
			execution_time_ms BIGINT NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_schema_migrations_applied_at ON schema_migrations(applied_at);
	`
	_, err := p.Exec(ctx, createTableSQL)
	return err
}

func (p *PostgreSQL) GetAppliedMigrations(ctx context.Context) ([]model.MigrationRecord, error) {
	query := `SELECT version, name, checksum, applied_at, execution_time_ms FROM schema_migrations ORDER BY version ASC`
	rows, err := p.Query(ctx, query)
	if err != nil {
		if strings.Contains(err.Error(), "relation \"schema_migrations\" does not exist") {
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

func (p *PostgreSQL) RecordMigration(ctx context.Context, version, name, checksum string, executionTimeMs int64) error {
	query := `INSERT INTO schema_migrations (version, name, checksum, applied_at, execution_time_ms) VALUES ($1, $2, $3, NOW(), $4)`
	_, err := p.Exec(ctx, query, version, name, checksum, executionTimeMs)
	return err
}

func (p *PostgreSQL) UnrecordMigration(ctx context.Context, version string) error {
	query := `DELETE FROM schema_migrations WHERE version = $1`
	_, err := p.Exec(ctx, query, version)
	return err
}

func (p *PostgreSQL) GetCurrentSchema(ctx context.Context) (*model.Schema, error) {
	schema := &model.Schema{}

	tables, err := p.getTables(ctx)
	if err != nil {
		return nil, err
	}

	for i := range tables {
		table := &tables[i]
		if err := p.loadTableColumns(ctx, table); err != nil {
			return nil, err
		}
		if err := p.loadTableIndexes(ctx, table); err != nil {
			return nil, err
		}
		if err := p.loadTableForeignKeys(ctx, table); err != nil {
			return nil, err
		}
		if err := p.loadTableConstraints(ctx, table); err != nil {
			return nil, err
		}
		schema.Tables = append(schema.Tables, *table)
	}

	return schema, nil
}

func (p *PostgreSQL) getTables(ctx context.Context) ([]model.Table, error) {
	query := `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
		AND table_type = 'BASE TABLE'
		AND table_name != 'schema_migrations'
		ORDER BY table_name
	`
	rows, err := p.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []model.Table
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, model.Table{Name: name})
	}
	return tables, rows.Err()
}

func (p *PostgreSQL) loadTableColumns(ctx context.Context, table *model.Table) error {
	query := `
		SELECT 
			c.column_name,
			c.data_type,
			CASE WHEN c.character_maximum_length IS NOT NULL 
				THEN c.data_type || '(' || c.character_maximum_length || ')'
				WHEN c.numeric_precision IS NOT NULL AND c.numeric_scale IS NOT NULL
				THEN c.data_type || '(' || c.numeric_precision || ',' || c.numeric_scale || ')'
				ELSE c.data_type 
			END as full_type,
			c.is_nullable = 'YES',
			c.column_default,
			EXISTS (
				SELECT 1 FROM information_schema.table_constraints tc
				JOIN information_schema.key_column_usage kcu 
					ON tc.constraint_name = kcu.constraint_name
				WHERE tc.table_name = c.table_name 
				AND kcu.column_name = c.column_name
				AND tc.constraint_type = 'PRIMARY KEY'
			) as is_primary_key,
			EXISTS (
				SELECT 1 FROM information_schema.table_constraints tc
				JOIN information_schema.key_column_usage kcu 
					ON tc.constraint_name = kcu.constraint_name
				WHERE tc.table_name = c.table_name 
				AND kcu.column_name = c.column_name
				AND tc.constraint_type = 'UNIQUE'
			) as is_unique,
			pg_get_expr(d.adbin, d.adrelid) as default_expr,
			col_description(('"' || c.table_name || '"')::regclass::oid, c.ordinal_position) as column_comment
		FROM information_schema.columns c
		LEFT JOIN pg_attrdef d ON c.table_name::regclass::oid = d.adrelid AND c.ordinal_position = d.adnum
		WHERE c.table_name = $1 AND c.table_schema = 'public'
		ORDER BY c.ordinal_position
	`
	rows, err := p.Query(ctx, query, table.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var col model.Column
		var dataType, fullType string
		var defaultValue sql.NullString
		var defaultExpr sql.NullString
		var comment sql.NullString

		if err := rows.Scan(&col.Name, &dataType, &fullType, &col.Nullable, &defaultValue, 
			&col.IsPrimaryKey, &col.IsUnique, &defaultExpr, &comment); err != nil {
			return err
		}

		col.Type = fullType
		if defaultExpr.Valid && defaultExpr.String != "" {
			col.DefaultValue = &defaultExpr.String
		} else if defaultValue.Valid && defaultValue.String != "" {
			col.DefaultValue = &defaultValue.String
		}
		if comment.Valid {
			col.Comment = comment.String
		}

		col.AutoIncrement = strings.Contains(dataType, "serial") || 
			strings.Contains(defaultValue.String, "nextval")

		table.Columns = append(table.Columns, col)
	}
	return rows.Err()
}

func (p *PostgreSQL) loadTableIndexes(ctx context.Context, table *model.Table) error {
	query := `
		SELECT 
			i.relname AS index_name,
			array_agg(a.attname ORDER BY array_position(idx.indkey, a.attnum)) AS columns,
			idx.indisunique AS is_unique,
			am.amname AS index_type
		FROM pg_index idx
		JOIN pg_class i ON idx.indexrelid = i.oid
		JOIN pg_class t ON idx.indrelid = t.oid
		JOIN pg_am am ON i.relam = am.oid
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(idx.indkey)
		WHERE t.relname = $1 
		AND idx.indisprimary = false
		AND NOT EXISTS (
			SELECT 1 FROM pg_constraint c
			WHERE c.conrelid = t.oid AND c.conindid = i.oid AND c.contype IN ('u', 'p')
		)
		GROUP BY i.relname, idx.indisunique, am.amname, idx.indkey
	`
	rows, err := p.Query(ctx, query, table.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var idx model.Index
		var columns []string

		if err := rows.Scan(&idx.Name, &columns, &idx.Unique, &idx.Type); err != nil {
			return err
		}
		idx.Columns = columns
		table.Indexes = append(table.Indexes, idx)
	}
	return rows.Err()
}

func (p *PostgreSQL) loadTableForeignKeys(ctx context.Context, table *model.Table) error {
	query := `
		SELECT
			tc.constraint_name,
			kcu.column_name,
			ccu.table_name AS foreign_table_name,
			ccu.column_name AS foreign_column_name,
			rc.update_rule,
			rc.delete_rule
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name
		JOIN information_schema.constraint_column_usage ccu
			ON ccu.constraint_name = tc.constraint_name
		JOIN information_schema.referential_constraints rc
			ON tc.constraint_name = rc.constraint_name
		WHERE tc.constraint_type = 'FOREIGN KEY'
		AND tc.table_name = $1
	`
	rows, err := p.Query(ctx, query, table.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	fkMap := make(map[string]*model.ForeignKey)
	for rows.Next() {
		var name, col, refTable, refCol, onUpdate, onDelete string
		if err := rows.Scan(&name, &col, &refTable, &refCol, &onUpdate, &onDelete); err != nil {
			return err
		}
		if _, ok := fkMap[name]; !ok {
			fkMap[name] = &model.ForeignKey{
				Name:     name,
				RefTable: refTable,
				OnDelete: onDelete,
				OnUpdate: onUpdate,
			}
		}
		fkMap[name].Columns = append(fkMap[name].Columns, col)
		fkMap[name].RefColumns = append(fkMap[name].RefColumns, refCol)
	}

	for _, fk := range fkMap {
		table.ForeignKeys = append(table.ForeignKeys, *fk)
	}
	return rows.Err()
}

func (p *PostgreSQL) loadTableConstraints(ctx context.Context, table *model.Table) error {
	ucQuery := `
		SELECT tc.constraint_name, array_agg(kcu.column_name ORDER BY kcu.ordinal_position)
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu ON tc.constraint_name = kcu.constraint_name
		WHERE tc.table_name = $1 AND tc.constraint_type = 'UNIQUE'
		AND NOT EXISTS (
			SELECT 1 FROM pg_index i
			WHERE i.indexrelid = tc.constraint_name::regclass
			AND i.indisprimary = true
		)
		GROUP BY tc.constraint_name
	`
	rows, err := p.Query(ctx, ucQuery, table.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var uc model.UniqueConstraint
		var columns []string
		if err := rows.Scan(&uc.Name, &columns); err != nil {
			return err
		}
		uc.Columns = columns
		table.UniqueConstraints = append(table.UniqueConstraints, uc)
	}
	rows.Close()

	ccQuery := `
		SELECT cc.constraint_name, pg_get_constraintdef(c.oid)
		FROM information_schema.check_constraints cc
		JOIN pg_constraint c ON cc.constraint_name = c.conname::text
		WHERE cc.table_name = $1
	`
	rows, err = p.Query(ctx, ccQuery, table.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cc model.CheckConstraint
		var def string
		if err := rows.Scan(&cc.Name, &def); err != nil {
			return err
		}
		if idx := strings.Index(def, "CHECK ("); idx >= 0 {
			cc.Expression = def[idx+7 : len(def)-1]
		} else {
			cc.Expression = def
		}
		table.CheckConstraints = append(table.CheckConstraints, cc)
	}

	return rows.Err()
}

func (p *PostgreSQL) AcquireAdvisoryLock(ctx context.Context, timeout time.Duration, retryInterval time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var acquired bool
		query := `SELECT pg_try_advisory_lock($1)`
		err := p.QueryRow(ctx, query, advisoryLockKey).Scan(&acquired)
		if err != nil {
			return false, err
		}
		if acquired {
			p.hasLock = true
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

func (p *PostgreSQL) ReleaseAdvisoryLock(ctx context.Context) error {
	if !p.hasLock {
		return nil
	}
	query := `SELECT pg_advisory_unlock($1)`
	_, err := p.Exec(ctx, query, advisoryLockKey)
	if err == nil {
		p.hasLock = false
	}
	return err
}

func (p *PostgreSQL) GetAutoIncrementKeyword() string {
	return "GENERATED ALWAYS AS IDENTITY"
}

func (p *PostgreSQL) GetColumnTypeSQL(col model.Column) string {
	if col.AutoIncrement {
		return col.Type
	}
	return col.Type
}

func (p *PostgreSQL) GetCreateTableSQL(table model.Table) string {
	var colDefs []string
	for _, col := range table.Columns {
		def := p.getColumnDefinition(col, p.GetColumnTypeSQL)
		if col.AutoIncrement {
			def = p.QuoteIdentifier(col.Name) + " " + p.GetAutoIncrementKeyword()
			if !col.Nullable {
				def += " NOT NULL"
			}
			if col.IsPrimaryKey {
				def += " PRIMARY KEY"
			}
		} else if col.IsPrimaryKey {
			def += " PRIMARY KEY"
		}
		colDefs = append(colDefs, def)
	}

	for _, uc := range table.UniqueConstraints {
		cols := make([]string, len(uc.Columns))
		for i, c := range uc.Columns {
			cols[i] = p.QuoteIdentifier(c)
		}
		namePart := ""
		if uc.Name != "" {
			namePart = " CONSTRAINT " + p.QuoteIdentifier(uc.Name)
		}
		colDefs = append(colDefs, fmt.Sprintf("%s UNIQUE (%s)", namePart, strings.Join(cols, ", ")))
	}

	for _, cc := range table.CheckConstraints {
		namePart := ""
		if cc.Name != "" {
			namePart = " CONSTRAINT " + p.QuoteIdentifier(cc.Name)
		}
		colDefs = append(colDefs, fmt.Sprintf("%s CHECK (%s)", namePart, cc.Expression))
	}

	return fmt.Sprintf("CREATE TABLE %s (\n  %s\n);", p.QuoteIdentifier(table.Name), strings.Join(colDefs, ",\n  "))
}

func (p *PostgreSQL) GetCreateIndexSQL(tableName string, idx model.Index) string {
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		cols[i] = p.QuoteIdentifier(c)
	}
	unique := ""
	if idx.Unique {
		unique = "UNIQUE "
	}
	indexType := ""
	if idx.Type != "" && idx.Type != "btree" {
		indexType = " USING " + idx.Type
	}
	name := idx.Name
	if name == "" {
		name = fmt.Sprintf("idx_%s_%s", tableName, strings.Join(idx.Columns, "_"))
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s%s (%s);", 
		unique, p.QuoteIdentifier(name), p.QuoteIdentifier(tableName), indexType, strings.Join(cols, ", "))
}

func (p *PostgreSQL) GetDropIndexSQL(tableName string, idx model.Index) string {
	return fmt.Sprintf("DROP INDEX %s;", p.QuoteIdentifier(idx.Name))
}

func (p *PostgreSQL) GetAddColumnSQL(tableName string, col model.Column) string {
	def := p.getColumnDefinition(col, p.GetColumnTypeSQL)
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", p.QuoteIdentifier(tableName), def)
}

func (p *PostgreSQL) GetDropColumnSQL(tableName string, columnName string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", p.QuoteIdentifier(tableName), p.QuoteIdentifier(columnName))
}

func (p *PostgreSQL) GetAlterColumnTypeSQL(tableName string, oldCol, newCol model.Column) string {
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", 
		p.QuoteIdentifier(tableName), p.QuoteIdentifier(newCol.Name), newCol.Type)
}

func (p *PostgreSQL) GetAlterColumnDefaultSQL(tableName string, col model.Column) string {
	if col.DefaultValue != nil {
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;",
			p.QuoteIdentifier(tableName), p.QuoteIdentifier(col.Name), *col.DefaultValue)
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;",
		p.QuoteIdentifier(tableName), p.QuoteIdentifier(col.Name))
}

func (p *PostgreSQL) GetAlterColumnNullSQL(tableName string, col model.Column) string {
	if col.Nullable {
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;",
			p.QuoteIdentifier(tableName), p.QuoteIdentifier(col.Name))
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;",
		p.QuoteIdentifier(tableName), p.QuoteIdentifier(col.Name))
}

func (p *PostgreSQL) GetAddForeignKeySQL(tableName string, fk model.ForeignKey) string {
	cols := make([]string, len(fk.Columns))
	refCols := make([]string, len(fk.RefColumns))
	for i, c := range fk.Columns {
		cols[i] = p.QuoteIdentifier(c)
	}
	for i, c := range fk.RefColumns {
		refCols[i] = p.QuoteIdentifier(c)
	}
	namePart := ""
	if fk.Name != "" {
		namePart = " CONSTRAINT " + p.QuoteIdentifier(fk.Name)
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
		p.QuoteIdentifier(tableName), namePart, strings.Join(cols, ", "),
		p.QuoteIdentifier(fk.RefTable), strings.Join(refCols, ", "), onClause)
}

func (p *PostgreSQL) GetDropForeignKeySQL(tableName string, fk model.ForeignKey) string {
	return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;",
		p.QuoteIdentifier(tableName), p.QuoteIdentifier(fk.Name))
}

func (p *PostgreSQL) GetAddUniqueConstraintSQL(tableName string, uc model.UniqueConstraint) string {
	cols := make([]string, len(uc.Columns))
	for i, c := range uc.Columns {
		cols[i] = p.QuoteIdentifier(c)
	}
	name := uc.Name
	if name == "" {
		name = fmt.Sprintf("%s_%s_unique", tableName, strings.Join(uc.Columns, "_"))
	}
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s);",
		p.QuoteIdentifier(tableName), p.QuoteIdentifier(name), strings.Join(cols, ", "))
}

func (p *PostgreSQL) GetDropUniqueConstraintSQL(tableName string, uc model.UniqueConstraint) string {
	return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;",
		p.QuoteIdentifier(tableName), p.QuoteIdentifier(uc.Name))
}

func (p *PostgreSQL) GetAddCheckConstraintSQL(tableName string, cc model.CheckConstraint) string {
	name := cc.Name
	if name == "" {
		name = fmt.Sprintf("%s_check_%d", tableName, time.Now().Unix())
	}
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s);",
		p.QuoteIdentifier(tableName), p.QuoteIdentifier(name), cc.Expression)
}

func (p *PostgreSQL) GetDropCheckConstraintSQL(tableName string, cc model.CheckConstraint) string {
	return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;",
		p.QuoteIdentifier(tableName), p.QuoteIdentifier(cc.Name))
}

func (p *PostgreSQL) GetDropTableSQL(tableName string) string {
	return fmt.Sprintf("DROP TABLE %s;", p.QuoteIdentifier(tableName))
}
