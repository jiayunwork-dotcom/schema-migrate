package diff

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/schema-migrate/schema-migrate/internal/database"
	"github.com/schema-migrate/schema-migrate/internal/model"
	"gopkg.in/yaml.v3"
)

type Differ struct {
	db database.Database
}

func NewDiffer(db database.Database) *Differ {
	return &Differ{db: db}
}

func (d *Differ) Compare(current *model.Schema, target *model.Schema) *model.SchemaDiff {
	diff := &model.SchemaDiff{}

	currentTables := make(map[string]model.Table)
	for _, t := range current.Tables {
		currentTables[t.Name] = t
	}

	targetTables := make(map[string]model.Table)
	for _, t := range target.Tables {
		targetTables[t.Name] = t
	}

	for name, targetTable := range targetTables {
		if currentTable, exists := currentTables[name]; exists {
			d.compareTables(currentTable, targetTable, diff)
		} else {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeAdd,
				Risk:       model.RiskSafe,
				SQL:        d.db.GetCreateTableSQL(targetTable),
				ObjectType: "table",
				ObjectName: name,
				Details:    fmt.Sprintf("Create new table %s", name),
			})

			for _, idx := range targetTable.Indexes {
				diff.Changes = append(diff.Changes, model.DiffChange{
					Type:       model.ChangeAdd,
					Risk:       model.RiskSafe,
					SQL:        d.db.GetCreateIndexSQL(name, idx),
					ObjectType: "index",
					ObjectName: idx.Name,
					Details:    fmt.Sprintf("Create index %s on table %s", idx.Name, name),
				})
			}
			diff.SafeCount++
		}
	}

	for name := range currentTables {
		if _, exists := targetTables[name]; !exists {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeDrop,
				Risk:       model.RiskDanger,
				SQL:        d.db.GetDropTableSQL(name),
				ObjectType: "table",
				ObjectName: name,
				Details:    fmt.Sprintf("Drop table %s (WARNING: This will delete all data!)", name),
			})
			diff.DangerCount++
		}
	}

	for i := range diff.Changes {
		switch diff.Changes[i].Risk {
		case model.RiskSafe:
			diff.SafeCount++
		case model.RiskWarning:
			diff.WarningCount++
		case model.RiskDanger:
			diff.DangerCount++
		}
	}

	return diff
}

func (d *Differ) compareTables(current model.Table, target model.Table, diff *model.SchemaDiff) {
	d.compareColumns(current, target, diff)
	d.compareIndexes(current, target, diff)
	d.compareForeignKeys(current, target, diff)
	d.compareUniqueConstraints(current, target, diff)
	d.compareCheckConstraints(current, target, diff)
}

func (d *Differ) compareColumns(current model.Table, target model.Table, diff *model.SchemaDiff) {
	currentCols := make(map[string]model.Column)
	for _, c := range current.Columns {
		currentCols[c.Name] = c
	}

	targetCols := make(map[string]model.Column)
	for _, c := range target.Columns {
		targetCols[c.Name] = c
	}

	for name, targetCol := range targetCols {
		if currentCol, exists := currentCols[name]; exists {
			d.compareColumn(current, currentCol, targetCol, diff)
		} else {
			risk := model.RiskSafe
			if !targetCol.Nullable && targetCol.DefaultValue == nil {
				risk = model.RiskWarning
			}
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeAdd,
				Risk:       risk,
				SQL:        d.db.GetAddColumnSQL(current.Name, targetCol),
				ObjectType: "column",
				ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
				Details:    fmt.Sprintf("Add column %s to table %s", name, current.Name),
			})
		}
	}

	for name := range currentCols {
		if _, exists := targetCols[name]; !exists {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeDrop,
				Risk:       model.RiskDanger,
				SQL:        d.db.GetDropColumnSQL(current.Name, name),
				ObjectType: "column",
				ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
				Details:    fmt.Sprintf("Drop column %s from table %s (WARNING: This will delete all data!)", name, current.Name),
			})
		}
	}
}

func (d *Differ) compareColumn(table model.Table, current, target model.Column, diff *model.SchemaDiff) {
	if normalizeType(current.Type) != normalizeType(target.Type) {
		risk := model.RiskWarning
		if isTypeNarrowing(current.Type, target.Type) {
			risk = model.RiskDanger
		}
		diff.Changes = append(diff.Changes, model.DiffChange{
			Type:       model.ChangeModify,
			Risk:       risk,
			SQL:        d.db.GetAlterColumnTypeSQL(table.Name, current, target),
			ObjectType: "column_type",
			ObjectName: fmt.Sprintf("%s.%s", table.Name, current.Name),
			Details:    fmt.Sprintf("Change column %s type from %s to %s", current.Name, current.Type, target.Type),
		})
	}

	if !defaultValuesEqual(current.DefaultValue, target.DefaultValue) {
		diff.Changes = append(diff.Changes, model.DiffChange{
			Type:       model.ChangeModify,
			Risk:       model.RiskSafe,
			SQL:        d.db.GetAlterColumnDefaultSQL(table.Name, target),
			ObjectType: "column_default",
			ObjectName: fmt.Sprintf("%s.%s", table.Name, current.Name),
			Details:    fmt.Sprintf("Change column %s default value", current.Name),
		})
	}

	if current.Nullable != target.Nullable {
		risk := model.RiskWarning
		if !target.Nullable {
			risk = model.RiskDanger
		}
		diff.Changes = append(diff.Changes, model.DiffChange{
			Type:       model.ChangeModify,
			Risk:       risk,
			SQL:        d.db.GetAlterColumnNullSQL(table.Name, target),
			ObjectType: "column_nullable",
			ObjectName: fmt.Sprintf("%s.%s", table.Name, current.Name),
			Details:    fmt.Sprintf("Change column %s nullable from %v to %v", current.Name, current.Nullable, target.Nullable),
		})
	}
}

func (d *Differ) compareIndexes(current model.Table, target model.Table, diff *model.SchemaDiff) {
	currentIndexes := make(map[string]model.Index)
	for _, idx := range current.Indexes {
		currentIndexes[idx.Name] = idx
	}

	targetIndexes := make(map[string]model.Index)
	for _, idx := range target.Indexes {
		targetIndexes[idx.Name] = idx
	}

	for name, targetIdx := range targetIndexes {
		if currentIdx, exists := currentIndexes[name]; exists {
			if !indexesEqual(currentIdx, targetIdx) {
				diff.Changes = append(diff.Changes, model.DiffChange{
					Type:       model.ChangeDrop,
					Risk:       model.RiskWarning,
					SQL:        d.db.GetDropIndexSQL(current.Name, currentIdx),
					ObjectType: "index",
					ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
					Details:    fmt.Sprintf("Drop and recreate index %s on table %s", name, current.Name),
				})
				diff.Changes = append(diff.Changes, model.DiffChange{
					Type:       model.ChangeAdd,
					Risk:       model.RiskSafe,
					SQL:        d.db.GetCreateIndexSQL(current.Name, targetIdx),
					ObjectType: "index",
					ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
					Details:    fmt.Sprintf("Recreate index %s on table %s", name, current.Name),
				})
			}
		} else {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeAdd,
				Risk:       model.RiskSafe,
				SQL:        d.db.GetCreateIndexSQL(current.Name, targetIdx),
				ObjectType: "index",
				ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
				Details:    fmt.Sprintf("Create index %s on table %s", name, current.Name),
			})
		}
	}

	for name, currentIdx := range currentIndexes {
		if _, exists := targetIndexes[name]; !exists {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeDrop,
				Risk:       model.RiskWarning,
				SQL:        d.db.GetDropIndexSQL(current.Name, currentIdx),
				ObjectType: "index",
				ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
				Details:    fmt.Sprintf("Drop index %s from table %s", name, current.Name),
			})
		}
	}
}

func (d *Differ) compareForeignKeys(current model.Table, target model.Table, diff *model.SchemaDiff) {
	currentFKs := make(map[string]model.ForeignKey)
	for _, fk := range current.ForeignKeys {
		currentFKs[fk.Name] = fk
	}

	targetFKs := make(map[string]model.ForeignKey)
	for _, fk := range target.ForeignKeys {
		targetFKs[fk.Name] = fk
	}

	for name, targetFK := range targetFKs {
		if _, exists := currentFKs[name]; !exists {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeAdd,
				Risk:       model.RiskSafe,
				SQL:        d.db.GetAddForeignKeySQL(current.Name, targetFK),
				ObjectType: "foreign_key",
				ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
				Details:    fmt.Sprintf("Add foreign key %s to table %s", name, current.Name),
			})
		}
	}

	for name, currentFK := range currentFKs {
		if _, exists := targetFKs[name]; !exists {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeDrop,
				Risk:       model.RiskWarning,
				SQL:        d.db.GetDropForeignKeySQL(current.Name, currentFK),
				ObjectType: "foreign_key",
				ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
				Details:    fmt.Sprintf("Drop foreign key %s from table %s", name, current.Name),
			})
		}
	}
}

func (d *Differ) compareUniqueConstraints(current model.Table, target model.Table, diff *model.SchemaDiff) {
	currentUCs := make(map[string]model.UniqueConstraint)
	for _, uc := range current.UniqueConstraints {
		currentUCs[uc.Name] = uc
	}

	targetUCs := make(map[string]model.UniqueConstraint)
	for _, uc := range target.UniqueConstraints {
		targetUCs[uc.Name] = uc
	}

	for name, targetUC := range targetUCs {
		if _, exists := currentUCs[name]; !exists {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeAdd,
				Risk:       model.RiskSafe,
				SQL:        d.db.GetAddUniqueConstraintSQL(current.Name, targetUC),
				ObjectType: "unique_constraint",
				ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
				Details:    fmt.Sprintf("Add unique constraint %s to table %s", name, current.Name),
			})
		}
	}

	for name, currentUC := range currentUCs {
		if _, exists := targetUCs[name]; !exists {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeDrop,
				Risk:       model.RiskWarning,
				SQL:        d.db.GetDropUniqueConstraintSQL(current.Name, currentUC),
				ObjectType: "unique_constraint",
				ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
				Details:    fmt.Sprintf("Drop unique constraint %s from table %s", name, current.Name),
			})
		}
	}
}

func (d *Differ) compareCheckConstraints(current model.Table, target model.Table, diff *model.SchemaDiff) {
	currentCCs := make(map[string]model.CheckConstraint)
	for _, cc := range current.CheckConstraints {
		currentCCs[cc.Name] = cc
	}

	targetCCs := make(map[string]model.CheckConstraint)
	for _, cc := range target.CheckConstraints {
		targetCCs[cc.Name] = cc
	}

	for name, targetCC := range targetCCs {
		if _, exists := currentCCs[name]; !exists {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeAdd,
				Risk:       model.RiskSafe,
				SQL:        d.db.GetAddCheckConstraintSQL(current.Name, targetCC),
				ObjectType: "check_constraint",
				ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
				Details:    fmt.Sprintf("Add check constraint %s to table %s", name, current.Name),
			})
		}
	}

	for name, currentCC := range currentCCs {
		if _, exists := targetCCs[name]; !exists {
			diff.Changes = append(diff.Changes, model.DiffChange{
				Type:       model.ChangeDrop,
				Risk:       model.RiskWarning,
				SQL:        d.db.GetDropCheckConstraintSQL(current.Name, currentCC),
				ObjectType: "check_constraint",
				ObjectName: fmt.Sprintf("%s.%s", current.Name, name),
				Details:    fmt.Sprintf("Drop check constraint %s from table %s", name, current.Name),
			})
		}
	}
}

func (d *Differ) LoadTargetSchema(filePath string) (*model.Schema, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read schema file: %w", err)
	}

	var schema model.Schema
	if err := yaml.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse schema YAML: %w", err)
	}

	return &schema, nil
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

func indexesEqual(a, b model.Index) bool {
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

func isTypeNarrowing(oldType, newType string) bool {
	oldType = strings.ToLower(oldType)
	newType = strings.ToLower(newType)

	varcRegex := regexp.MustCompile(`varchar\((\d+)\)`)
	oldMatch := varcRegex.FindStringSubmatch(oldType)
	newMatch := varcRegex.FindStringSubmatch(newType)
	if oldMatch != nil && newMatch != nil {
		oldLen, _ := strconv.Atoi(oldMatch[1])
		newLen, _ := strconv.Atoi(newMatch[1])
		return newLen < oldLen
	}

	charRegex := regexp.MustCompile(`char\((\d+)\)`)
	oldMatch = charRegex.FindStringSubmatch(oldType)
	newMatch = charRegex.FindStringSubmatch(newType)
	if oldMatch != nil && newMatch != nil {
		oldLen, _ := strconv.Atoi(oldMatch[1])
		newLen, _ := strconv.Atoi(newMatch[1])
		return newLen < oldLen
	}

	decimalRegex := regexp.MustCompile(`(?:decimal|numeric)\((\d+),(\d+)\)`)
	oldMatch = decimalRegex.FindStringSubmatch(oldType)
	newMatch = decimalRegex.FindStringSubmatch(newType)
	if oldMatch != nil && newMatch != nil {
		oldPrec, _ := strconv.Atoi(oldMatch[1])
		newPrec, _ := strconv.Atoi(newMatch[1])
		return newPrec < oldPrec
	}

	intTypes := map[string]int{
		"tinyint":            1,
		"smallint":           2,
		"mediumint":          3,
		"int":                4,
		"integer":            4,
		"bigint":             5,
		"smallserial":        2,
		"serial":             4,
		"bigserial":          5,
	}

	oldRank, oldOk := intTypes[oldType]
	newRank, newOk := intTypes[newType]
	if oldOk && newOk {
		return newRank < oldRank
	}

	return false
}
