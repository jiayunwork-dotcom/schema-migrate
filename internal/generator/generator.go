package generator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/schema-migrate/schema-migrate/internal/database"
	"github.com/schema-migrate/schema-migrate/internal/model"
)

type TableMigration struct {
	TableName string
	UpSQL     string
	DownSQL   string
}

type Generator struct {
	db            database.Database
	migrationsDir string
}

func NewGenerator(db database.Database, migrationsDir string) *Generator {
	return &Generator{
		db:            db,
		migrationsDir: migrationsDir,
	}
}

func (g *Generator) Generate(current, target *model.Schema, name string) (string, string, error) {
	tableMigrations, err := g.generateTableMigrations(current, target)
	if err != nil {
		return "", "", err
	}

	if len(tableMigrations) == 0 {
		return "", "", nil
	}

	orderedUp := g.orderTablesForUp(tableMigrations)
	orderedDown := g.orderTablesForDown(tableMigrations)

	var upSQL, downSQL strings.Builder
	for _, tm := range orderedUp {
		if tm.UpSQL != "" {
			upSQL.WriteString(tm.UpSQL)
			upSQL.WriteString("\n\n")
		}
	}
	for _, tm := range orderedDown {
		if tm.DownSQL != "" {
			downSQL.WriteString(tm.DownSQL)
			downSQL.WriteString("\n\n")
		}
	}

	return strings.TrimSpace(upSQL.String()), strings.TrimSpace(downSQL.String()), nil
}

func (g *Generator) GenerateSplit(current, target *model.Schema, name string) ([]TableMigration, error) {
	tableMigrations, err := g.generateTableMigrations(current, target)
	if err != nil {
		return nil, err
	}

	if len(tableMigrations) == 0 {
		return nil, nil
	}

	ordered := g.orderTablesForUp(tableMigrations)
	return ordered, nil
}

func (g *Generator) generateTableMigrations(current, target *model.Schema) ([]TableMigration, error) {
	currentTables := make(map[string]model.Table)
	for _, t := range current.Tables {
		currentTables[t.Name] = t
	}

	targetTables := make(map[string]model.Table)
	for _, t := range target.Tables {
		targetTables[t.Name] = t
	}

	tableMap := make(map[string]*TableMigration)

	for name, targetTable := range targetTables {
		if currentTable, exists := currentTables[name]; exists {
			tm := g.generateAlterTableMigration(currentTable, targetTable)
			tableMap[name] = &tm
		} else {
			tm := g.generateCreateTableMigration(targetTable)
			tableMap[name] = &tm
		}
	}

	for name, currentTable := range currentTables {
		if _, exists := targetTables[name]; !exists {
			tm := g.generateDropTableMigration(currentTable)
			tableMap[name] = &tm
		}
	}

	var result []TableMigration
	for _, tm := range tableMap {
		if tm.UpSQL != "" || tm.DownSQL != "" {
			result = append(result, *tm)
		}
	}

	return result, nil
}

func (g *Generator) generateCreateTableMigration(table model.Table) TableMigration {
	var upParts, downParts []string

	upCreateSQL := g.db.GetCreateTableSQL(table)
	upParts = append(upParts, upCreateSQL)

	for _, idx := range table.Indexes {
		sql := g.getCreateIndexSQL(table.Name, idx)
		upParts = append(upParts, sql)
	}

	downDropSQL := g.db.GetDropTableSQL(table.Name)
	downParts = append(downParts, downDropSQL)

	return TableMigration{
		TableName: table.Name,
		UpSQL:     strings.Join(upParts, "\n\n"),
		DownSQL:   strings.Join(downParts, "\n\n"),
	}
}

func (g *Generator) generateDropTableMigration(table model.Table) TableMigration {
	var upParts, downParts []string

	upDropSQL := fmt.Sprintf("-- DESTRUCTIVE: Dropping table %s will delete all data", table.Name)
	upDropSQL += "\n" + g.db.GetDropTableSQL(table.Name)
	upParts = append(upParts, upDropSQL)

	downCreateSQL := g.db.GetCreateTableSQL(table)
	downParts = append(downParts, downCreateSQL)

	for _, idx := range table.Indexes {
		sql := g.getCreateIndexSQL(table.Name, idx)
		downParts = append(downParts, sql)
	}

	for _, fk := range table.ForeignKeys {
		sql := g.db.GetAddForeignKeySQL(table.Name, fk)
		downParts = append(downParts, sql)
	}

	for _, uc := range table.UniqueConstraints {
		sql := g.db.GetAddUniqueConstraintSQL(table.Name, uc)
		downParts = append(downParts, sql)
	}

	for _, cc := range table.CheckConstraints {
		sql := g.db.GetAddCheckConstraintSQL(table.Name, cc)
		downParts = append(downParts, sql)
	}

	return TableMigration{
		TableName: table.Name,
		UpSQL:     strings.Join(upParts, "\n\n"),
		DownSQL:   strings.Join(downParts, "\n\n"),
	}
}

func (g *Generator) generateAlterTableMigration(current, target model.Table) TableMigration {
	var upParts, downParts []string

	currentCols := make(map[string]model.Column)
	for _, c := range current.Columns {
		currentCols[c.Name] = c
	}

	targetCols := make(map[string]model.Column)
	for _, c := range target.Columns {
		targetCols[c.Name] = c
	}

	currentIdxs := make(map[string]model.Index)
	for _, idx := range current.Indexes {
		currentIdxs[idx.Name] = idx
	}

	targetIdxs := make(map[string]model.Index)
	for _, idx := range target.Indexes {
		targetIdxs[idx.Name] = idx
	}

	currentFKs := make(map[string]model.ForeignKey)
	for _, fk := range current.ForeignKeys {
		currentFKs[fk.Name] = fk
	}

	targetFKs := make(map[string]model.ForeignKey)
	for _, fk := range target.ForeignKeys {
		targetFKs[fk.Name] = fk
	}

	currentUCs := make(map[string]model.UniqueConstraint)
	for _, uc := range current.UniqueConstraints {
		currentUCs[uc.Name] = uc
	}

	targetUCs := make(map[string]model.UniqueConstraint)
	for _, uc := range target.UniqueConstraints {
		targetUCs[uc.Name] = uc
	}

	currentCCs := make(map[string]model.CheckConstraint)
	for _, cc := range current.CheckConstraints {
		currentCCs[cc.Name] = cc
	}

	targetCCs := make(map[string]model.CheckConstraint)
	for _, cc := range target.CheckConstraints {
		targetCCs[cc.Name] = cc
	}

	var colAdds, colDrops, colTypeChanges, colDefaultChanges, colNullChanges []string
	var colAddsDown, colDropsDown, colTypeChangesDown, colDefaultChangesDown, colNullChangesDown []string

	for name, targetCol := range targetCols {
		if currentCol, exists := currentCols[name]; exists {
			if normalizeType(currentCol.Type) != normalizeType(targetCol.Type) {
				upSQL := g.db.GetAlterColumnTypeSQL(current.Name, currentCol, targetCol)
				if isDestructiveColumnChange(currentCol, targetCol) {
					upSQL = fmt.Sprintf("-- DESTRUCTIVE: Changing column %s type from %s to %s may cause data loss",
						name, currentCol.Type, targetCol.Type) + "\n" + upSQL
				}
				colTypeChanges = append(colTypeChanges, upSQL)

				downSQL := g.db.GetAlterColumnTypeSQL(current.Name, targetCol, currentCol)
				if isDestructiveColumnChange(targetCol, currentCol) {
					downSQL = fmt.Sprintf("-- DESTRUCTIVE: Changing column %s type from %s to %s may cause data loss",
						name, targetCol.Type, currentCol.Type) + "\n" + downSQL
				}
				colTypeChangesDown = append([]string{downSQL}, colTypeChangesDown...)
			}

			if !defaultValuesEqual(currentCol.DefaultValue, targetCol.DefaultValue) {
				upSQL := g.db.GetAlterColumnDefaultSQL(current.Name, targetCol)
				colDefaultChanges = append(colDefaultChanges, upSQL)

				downSQL := g.db.GetAlterColumnDefaultSQL(current.Name, currentCol)
				colDefaultChangesDown = append([]string{downSQL}, colDefaultChangesDown...)
			}

			if currentCol.Nullable != targetCol.Nullable {
				upSQL := g.db.GetAlterColumnNullSQL(current.Name, targetCol)
				if !targetCol.Nullable && currentCol.Nullable {
					upSQL = fmt.Sprintf("-- DESTRUCTIVE: Setting column %s to NOT NULL may fail if existing rows have NULL values", name) + "\n" + upSQL
				}
				colNullChanges = append(colNullChanges, upSQL)

				downSQL := g.db.GetAlterColumnNullSQL(current.Name, currentCol)
				if !currentCol.Nullable && targetCol.Nullable {
					downSQL = fmt.Sprintf("-- DESTRUCTIVE: Setting column %s to NOT NULL may fail if existing rows have NULL values", name) + "\n" + downSQL
				}
				colNullChangesDown = append([]string{downSQL}, colNullChangesDown...)
			}
		} else {
			upSQL := g.db.GetAddColumnSQL(current.Name, targetCol)
			if !targetCol.Nullable && targetCol.DefaultValue == nil {
				upSQL = fmt.Sprintf("-- WARNING: Adding NOT NULL column %s without default may fail on existing data", name) + "\n" + upSQL
			}
			colAdds = append(colAdds, upSQL)

			downSQL := g.db.GetDropColumnSQL(current.Name, name)
			colAddsDown = append([]string{downSQL}, colAddsDown...)
		}
	}

	for name, currentCol := range currentCols {
		if _, exists := targetCols[name]; !exists {
			upSQL := fmt.Sprintf("-- DESTRUCTIVE: Dropping column %s will delete all data in that column", name)
			upSQL += "\n" + g.db.GetDropColumnSQL(current.Name, name)
			colDrops = append(colDrops, upSQL)

			downSQL := g.db.GetAddColumnSQL(current.Name, currentCol)
			colDropsDown = append([]string{downSQL}, colDropsDown...)
		}
	}

	var idxAdds, idxDrops []string
	var idxAddsDown, idxDropsDown []string

	for name, targetIdx := range targetIdxs {
		if currentIdx, exists := currentIdxs[name]; exists {
			if !g.indexesEqual(currentIdx, targetIdx) {
				idxDrops = append(idxDrops, g.db.GetDropIndexSQL(current.Name, currentIdx))
				idxAdds = append(idxAdds, g.getCreateIndexSQL(current.Name, targetIdx))

				idxAddsDown = append(idxAddsDown, g.db.GetDropIndexSQL(current.Name, targetIdx))
				idxDropsDown = append([]string{g.getCreateIndexSQL(current.Name, currentIdx)}, idxDropsDown...)
			}
		} else {
			idxAdds = append(idxAdds, g.getCreateIndexSQL(current.Name, targetIdx))
			idxAddsDown = append([]string{g.db.GetDropIndexSQL(current.Name, targetIdx)}, idxAddsDown...)
		}
	}

	for name, currentIdx := range currentIdxs {
		if _, exists := targetIdxs[name]; !exists {
			idxDrops = append(idxDrops, g.db.GetDropIndexSQL(current.Name, currentIdx))
			idxDropsDown = append([]string{g.getCreateIndexSQL(current.Name, currentIdx)}, idxDropsDown...)
		}
	}

	var fkAdds, fkDrops []string
	var fkAddsDown, fkDropsDown []string

	for name, targetFK := range targetFKs {
		if _, exists := currentFKs[name]; !exists {
			fkAdds = append(fkAdds, g.db.GetAddForeignKeySQL(current.Name, targetFK))
			fkAddsDown = append([]string{g.db.GetDropForeignKeySQL(current.Name, targetFK)}, fkAddsDown...)
		}
	}

	for name, currentFK := range currentFKs {
		if _, exists := targetFKs[name]; !exists {
			fkDrops = append(fkDrops, g.db.GetDropForeignKeySQL(current.Name, currentFK))
			fkDropsDown = append([]string{g.db.GetAddForeignKeySQL(current.Name, currentFK)}, fkDropsDown...)
		}
	}

	var ucAdds, ucDrops []string
	var ucAddsDown, ucDropsDown []string

	for name, targetUC := range targetUCs {
		if _, exists := currentUCs[name]; !exists {
			ucAdds = append(ucAdds, g.db.GetAddUniqueConstraintSQL(current.Name, targetUC))
			ucAddsDown = append([]string{g.db.GetDropUniqueConstraintSQL(current.Name, targetUC)}, ucAddsDown...)
		}
	}

	for name, currentUC := range currentUCs {
		if _, exists := targetUCs[name]; !exists {
			ucDrops = append(ucDrops, g.db.GetDropUniqueConstraintSQL(current.Name, currentUC))
			ucDropsDown = append([]string{g.db.GetAddUniqueConstraintSQL(current.Name, currentUC)}, ucDropsDown...)
		}
	}

	var ccAdds, ccDrops []string
	var ccAddsDown, ccDropsDown []string

	for name, targetCC := range targetCCs {
		if _, exists := currentCCs[name]; !exists {
			ccAdds = append(ccAdds, g.db.GetAddCheckConstraintSQL(current.Name, targetCC))
			ccAddsDown = append([]string{g.db.GetDropCheckConstraintSQL(current.Name, targetCC)}, ccAddsDown...)
		}
	}

	for name, currentCC := range currentCCs {
		if _, exists := targetCCs[name]; !exists {
			ccDrops = append(ccDrops, g.db.GetDropCheckConstraintSQL(current.Name, currentCC))
			ccDropsDown = append([]string{g.db.GetAddCheckConstraintSQL(current.Name, currentCC)}, ccDropsDown...)
		}
	}

	upParts = append(upParts, colAdds...)
	upParts = append(upParts, colTypeChanges...)
	upParts = append(upParts, colDefaultChanges...)
	upParts = append(upParts, colNullChanges...)
	upParts = append(upParts, colDrops...)
	upParts = append(upParts, idxAdds...)
	upParts = append(upParts, idxDrops...)
	upParts = append(upParts, fkAdds...)
	upParts = append(upParts, fkDrops...)
	upParts = append(upParts, ucAdds...)
	upParts = append(upParts, ucDrops...)
	upParts = append(upParts, ccAdds...)
	upParts = append(upParts, ccDrops...)

	downParts = append(downParts, ccDropsDown...)
	downParts = append(downParts, ccAddsDown...)
	downParts = append(downParts, ucDropsDown...)
	downParts = append(downParts, ucAddsDown...)
	downParts = append(downParts, fkDropsDown...)
	downParts = append(downParts, fkAddsDown...)
	downParts = append(downParts, idxDropsDown...)
	downParts = append(downParts, idxAddsDown...)
	downParts = append(downParts, colDropsDown...)
	downParts = append(downParts, colNullChangesDown...)
	downParts = append(downParts, colDefaultChangesDown...)
	downParts = append(downParts, colTypeChangesDown...)
	downParts = append(downParts, colAddsDown...)

	return TableMigration{
		TableName: current.Name,
		UpSQL:     strings.Join(upParts, "\n\n"),
		DownSQL:   strings.Join(downParts, "\n\n"),
	}
}

func (g *Generator) getCreateIndexSQL(tableName string, idx model.Index) string {
	sql := g.db.GetCreateIndexSQL(tableName, idx)
	if g.db.Type() == model.DBPostgreSQL {
		sql = strings.Replace(sql, "CREATE ", "CREATE CONCURRENTLY ", 1)
	}
	return sql
}

func (g *Generator) indexesEqual(a, b model.Index) bool {
	if a.Name != b.Name || a.Unique != b.Unique || a.Type != b.Type {
		return false
	}
	if len(a.Columns) != len(b.Columns) {
		return false
	}
	for i := range a.Columns {
		if a.Columns[i] != b.Columns[i] {
			return false
		}
	}
	return true
}

func isDestructiveColumnChange(oldCol, newCol model.Column) bool {
	oldType := strings.ToLower(oldCol.Type)
	newType := strings.ToLower(newCol.Type)

	varcRegex := regexp.MustCompile(`varchar\((\d+)\)`)
	oldMatch := varcRegex.FindStringSubmatch(oldType)
	newMatch := varcRegex.FindStringSubmatch(newType)
	if oldMatch != nil && newMatch != nil {
		oldLen, _ := strconv.Atoi(oldMatch[1])
		newLen, _ := strconv.Atoi(newMatch[1])
		if newLen < oldLen {
			return true
		}
	}

	intTypes := map[string]int{
		"tinyint":   1,
		"smallint":  2,
		"mediumint": 3,
		"int":       4,
		"integer":   4,
		"bigint":    5,
	}

	oldRank, oldOk := intTypes[oldType]
	newRank, newOk := intTypes[newType]
	if oldOk && newOk && newRank < oldRank {
		return true
	}

	if !newCol.Nullable && oldCol.Nullable {
		return true
	}

	return false
}

func (g *Generator) orderTablesForUp(migrations []TableMigration) []TableMigration {
	tableDeps := make(map[string][]string)
	tableMap := make(map[string]TableMigration)

	for _, tm := range migrations {
		tableMap[tm.TableName] = tm
		tableDeps[tm.TableName] = []string{}
	}

	for _, tm := range migrations {
		deps := g.extractTableDependencies(tm.UpSQL, tableMap)
		tableDeps[tm.TableName] = deps
	}

	return g.topologicalSort(migrations, tableDeps, true)
}

func (g *Generator) orderTablesForDown(migrations []TableMigration) []TableMigration {
	tableDeps := make(map[string][]string)
	tableMap := make(map[string]TableMigration)

	for _, tm := range migrations {
		tableMap[tm.TableName] = tm
		tableDeps[tm.TableName] = []string{}
	}

	for _, tm := range migrations {
		deps := g.extractTableDependencies(tm.DownSQL, tableMap)
		tableDeps[tm.TableName] = deps
	}

	return g.topologicalSort(migrations, tableDeps, false)
}

func (g *Generator) extractTableDependencies(sql string, tableMap map[string]TableMigration) []string {
	var deps []string
	seen := make(map[string]bool)

	referencesRegex := regexp.MustCompile(`(?i)REFERENCES\s+["` + "`" + `]?([a-zA-Z_][a-zA-Z0-9_]*)["` + "`" + `]?`)
	matches := referencesRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range matches {
		refTable := strings.ToLower(match[1])
		if _, exists := tableMap[refTable]; exists {
			if !seen[refTable] {
				deps = append(deps, refTable)
				seen[refTable] = true
			}
		}
	}

	return deps
}

func (g *Generator) topologicalSort(migrations []TableMigration, deps map[string][]string, forward bool) []TableMigration {
	inDegree := make(map[string]int)
	for _, tm := range migrations {
		inDegree[tm.TableName] = 0
	}

	adj := make(map[string][]string)
	for table, tableDeps := range deps {
		for _, dep := range tableDeps {
			if forward {
				adj[dep] = append(adj[dep], table)
				inDegree[table]++
			} else {
				adj[table] = append(adj[table], dep)
				inDegree[dep]++
			}
		}
	}

	var queue []string
	for table, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, table)
		}
	}
	sort.Strings(queue)

	tableMap := make(map[string]TableMigration)
	for _, tm := range migrations {
		tableMap[tm.TableName] = tm
	}

	var result []TableMigration
	for len(queue) > 0 {
		sort.Strings(queue)
		table := queue[0]
		queue = queue[1:]

		if tm, ok := tableMap[table]; ok {
			result = append(result, tm)
		}

		for _, next := range adj[table] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(result) != len(migrations) {
		return migrations
	}

	return result
}

func (g *Generator) WriteMigration(name string, upSQL, downSQL string) (*model.Migration, error) {
	version := time.Now().Format("20060102150405")
	safeName := sanitizeMigrationName(name)
	baseName := fmt.Sprintf("%s_%s", version, safeName)

	upPath := filepath.Join(g.migrationsDir, baseName+".up.sql")
	downPath := filepath.Join(g.migrationsDir, baseName+".down.sql")

	if err := os.MkdirAll(g.migrationsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create migrations directory: %w", err)
	}

	upContent := fmt.Sprintf("-- Migration: %s\n-- Generated: %s\n-- Auto-generated by schema-migrate generate\n\n%s\n",
		baseName, time.Now().Format(time.RFC3339), upSQL)

	downContent := fmt.Sprintf("-- Rollback: %s\n-- Generated: %s\n-- Auto-generated by schema-migrate generate\n\n%s\n",
		baseName, time.Now().Format(time.RFC3339), downSQL)

	if err := os.WriteFile(upPath, []byte(upContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write up migration: %w", err)
	}

	if err := os.WriteFile(downPath, []byte(downContent), 0644); err != nil {
		os.Remove(upPath)
		return nil, fmt.Errorf("failed to write down migration: %w", err)
	}

	return &model.Migration{
		Version:  version,
		Name:     safeName,
		UpSQL:    upContent,
		DownSQL:  downContent,
		UpPath:   upPath,
		DownPath: downPath,
	}, nil
}

func (g *Generator) WriteSplitMigrations(name string, tableMigrations []TableMigration) ([]*model.Migration, error) {
	var migrations []*model.Migration
	baseVersion := time.Now()
	safeName := sanitizeMigrationName(name)

	for i, tm := range tableMigrations {
		if tm.UpSQL == "" && tm.DownSQL == "" {
			continue
		}

		version := baseVersion.Add(time.Duration(i) * time.Second).Format("20060102150405")
		tableSafeName := sanitizeMigrationName(tm.TableName)
		baseName := fmt.Sprintf("%s_%s_%s", version, safeName, tableSafeName)

		upPath := filepath.Join(g.migrationsDir, baseName+".up.sql")
		downPath := filepath.Join(g.migrationsDir, baseName+".down.sql")

		if err := os.MkdirAll(g.migrationsDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create migrations directory: %w", err)
		}

		upContent := fmt.Sprintf("-- Migration: %s\n-- Generated: %s\n-- Table: %s\n-- Auto-generated by schema-migrate generate\n\n%s\n",
			baseName, time.Now().Format(time.RFC3339), tm.TableName, tm.UpSQL)

		downContent := fmt.Sprintf("-- Rollback: %s\n-- Generated: %s\n-- Table: %s\n-- Auto-generated by schema-migrate generate\n\n%s\n",
			baseName, time.Now().Format(time.RFC3339), tm.TableName, tm.DownSQL)

		if err := os.WriteFile(upPath, []byte(upContent), 0644); err != nil {
			return nil, fmt.Errorf("failed to write up migration for %s: %w", tm.TableName, err)
		}

		if err := os.WriteFile(downPath, []byte(downContent), 0644); err != nil {
			os.Remove(upPath)
			return nil, fmt.Errorf("failed to write down migration for %s: %w", tm.TableName, err)
		}

		migrations = append(migrations, &model.Migration{
			Version:  version,
			Name:     fmt.Sprintf("%s_%s", safeName, tableSafeName),
			UpSQL:    upContent,
			DownSQL:  downContent,
			UpPath:   upPath,
			DownPath: downPath,
		})
	}

	return migrations, nil
}

func normalizeType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = regexp.MustCompile(`\s+`).ReplaceAllString(t, " ")
	return t
}

func defaultValuesEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return strings.TrimSpace(*a) == strings.TrimSpace(*b)
}

func sanitizeMigrationName(name string) string {
	name = strings.ToLower(name)
	name = regexp.MustCompile(`[^a-z0-9_]+`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}

func (g *Generator) CheckPendingMigrations(ctx context.Context) (bool, []model.Migration, error) {
	if _, err := os.Stat(g.migrationsDir); os.IsNotExist(err) {
		return false, nil, nil
	}

	files, err := os.ReadDir(g.migrationsDir)
	if err != nil {
		return false, nil, err
	}

	migrationFileRegex := regexp.MustCompile(`^(\d{14})_(.+)\.(up|down)\.sql$`)
	migrationMap := make(map[string]*model.Migration)

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		matches := migrationFileRegex.FindStringSubmatch(file.Name())
		if len(matches) != 4 {
			continue
		}

		version := matches[1]
		name := matches[2]
		direction := matches[3]

		key := version + "_" + name
		if _, ok := migrationMap[key]; !ok {
			order, _ := strconv.Atoi(version)
			migrationMap[key] = &model.Migration{
				Version: version,
				Name:    name,
				Order:   order,
			}
		}

		fullPath := filepath.Join(g.migrationsDir, file.Name())
		if direction == "up" {
			migrationMap[key].UpPath = fullPath
		} else {
			migrationMap[key].DownPath = fullPath
		}
	}

	appliedRecords, err := g.db.GetAppliedMigrations(ctx)
	if err != nil {
		return false, nil, err
	}

	appliedMap := make(map[string]bool)
	for _, r := range appliedRecords {
		appliedMap[r.Version] = true
	}

	var pending []model.Migration
	for _, mig := range migrationMap {
		if !appliedMap[mig.Version] {
			pending = append(pending, *mig)
		}
	}

	sort.Slice(pending, func(i, j int) bool {
		return pending[i].Order < pending[j].Order
	})

	return len(pending) > 0, pending, nil
}
