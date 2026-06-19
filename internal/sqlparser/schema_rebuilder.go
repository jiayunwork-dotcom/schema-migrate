package sqlparser

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/schema-migrate/schema-migrate/internal/model"
)

type SchemaRebuilder struct {
	migrationsDir string
}

func NewSchemaRebuilder(migrationsDir string) *SchemaRebuilder {
	return &SchemaRebuilder{
		migrationsDir: migrationsDir,
	}
}

func (r *SchemaRebuilder) RebuildSchema() (*model.Schema, error) {
	migrations, err := r.loadMigrations()
	if err != nil {
		return nil, err
	}

	if len(migrations) == 0 {
		return &model.Schema{}, nil
	}

	parser := NewParser()

	for _, mig := range migrations {
		upSQL := mig.UpSQL

		statements := splitStatements(upSQL)

		for i := 0; i < len(statements); i++ {
			stmt := strings.TrimSpace(statements[i])
			if stmt == "" {
				continue
			}

			if isSQLiteReconstructionPattern(statements, i) {
				tableName, err := detectSQLiteReconstruction(statements, i)
				if err == nil && tableName != "" {
					if err := applySQLiteReconstruction(parser, statements, i, tableName); err != nil {
						return nil, fmt.Errorf("failed to apply SQLite reconstruction for table %s: %w", tableName, err)
					}
					i += 3
					continue
				}
			}

			if err := parser.ParseStatement(stmt); err != nil {
				return nil, fmt.Errorf("failed to parse migration %s_%s: %w", mig.Version, mig.Name, err)
			}
		}
	}

	return parser.GetSchema(), nil
}

func (r *SchemaRebuilder) loadMigrations() ([]model.Migration, error) {
	if _, err := os.Stat(r.migrationsDir); os.IsNotExist(err) {
		return []model.Migration{}, nil
	}

	files, err := os.ReadDir(r.migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
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

		fullPath := filepath.Join(r.migrationsDir, file.Name())
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read migration file %s: %w", file.Name(), err)
		}

		if direction == "up" {
			migrationMap[key].UpSQL = string(content)
			migrationMap[key].UpPath = fullPath
		} else {
			migrationMap[key].DownSQL = string(content)
			migrationMap[key].DownPath = fullPath
		}
	}

	var migrations []model.Migration
	for _, mig := range migrationMap {
		migrations = append(migrations, *mig)
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Order < migrations[j].Order
	})

	return migrations, nil
}

func isSQLiteReconstructionPattern(statements []string, index int) bool {
	if index+3 >= len(statements) {
		return false
	}

	stmt1 := strings.ToUpper(strings.TrimSpace(statements[index]))
	stmt2 := strings.ToUpper(strings.TrimSpace(statements[index+1]))
	stmt3 := strings.ToUpper(strings.TrimSpace(statements[index+2]))
	stmt4 := strings.ToUpper(strings.TrimSpace(statements[index+3]))

	hasRename := strings.Contains(stmt1, "RENAME TO") && strings.Contains(stmt1, "_OLD")
	hasCreate := strings.HasPrefix(stmt2, "CREATE TABLE")
	hasInsert := strings.HasPrefix(stmt3, "INSERT INTO") && strings.Contains(stmt3, "SELECT") && strings.Contains(stmt3, "_OLD")
	hasDrop := strings.HasPrefix(stmt4, "DROP TABLE") && strings.Contains(stmt4, "_OLD")

	return hasRename && hasCreate && hasInsert && hasDrop
}

func detectSQLiteReconstruction(statements []string, index int) (string, error) {
	stmt1 := strings.TrimSpace(statements[index])

	re := regexp.MustCompile(`(?i)ALTER TABLE ["` + "`" + `]?(\w+)["` + "`" + `]?\s+RENAME TO\s+["` + "`" + `]?(\w+)_old["` + "`" + `]?`)
	matches := re.FindStringSubmatch(stmt1)
	if len(matches) < 3 {
		return "", fmt.Errorf("could not parse RENAME TO statement")
	}

	tableName := matches[1]
	oldName := matches[2]

	if strings.EqualFold(tableName, oldName) {
		return tableName, nil
	}

	return "", fmt.Errorf("table name mismatch in RENAME TO")
}

func applySQLiteReconstruction(parser *Parser, statements []string, index int, tableName string) error {
	createStmt := strings.TrimSpace(statements[index+1])

	oldTable, exists := parser.GetTable(tableName)
	if !exists {
		return fmt.Errorf("table %s does not exist for reconstruction", tableName)
	}

	if err := parser.ParseCreateTable(createStmt); err != nil {
		return fmt.Errorf("failed to parse CREATE TABLE in reconstruction: %w", err)
	}

	newTable, exists := parser.GetTable(tableName)
	if !exists {
		return fmt.Errorf("new table %s was not created", tableName)
	}

	for _, oldIdx := range oldTable.Indexes {
		exists := false
		for _, newIdx := range newTable.Indexes {
			if strings.EqualFold(oldIdx.Name, newIdx.Name) {
				exists = true
				break
			}
		}
		if !exists {
			newTable.Indexes = append(newTable.Indexes, oldIdx)
		}
	}

	for _, oldFK := range oldTable.ForeignKeys {
		exists := false
		for _, newFK := range newTable.ForeignKeys {
			if strings.EqualFold(oldFK.Name, newFK.Name) {
				exists = true
				break
			}
		}
		if !exists {
			newTable.ForeignKeys = append(newTable.ForeignKeys, oldFK)
		}
	}

	for _, oldUC := range oldTable.UniqueConstraints {
		exists := false
		for _, newUC := range newTable.UniqueConstraints {
			if strings.EqualFold(oldUC.Name, newUC.Name) {
				exists = true
				break
			}
		}
		if !exists {
			newTable.UniqueConstraints = append(newTable.UniqueConstraints, oldUC)
		}
	}

	for _, oldCC := range oldTable.CheckConstraints {
		exists := false
		for _, newCC := range newTable.CheckConstraints {
			if strings.EqualFold(oldCC.Name, newCC.Name) {
				exists = true
				break
			}
		}
		if !exists {
			newTable.CheckConstraints = append(newTable.CheckConstraints, oldCC)
		}
	}

	return nil
}
