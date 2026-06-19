package security

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/schema-migrate/schema-migrate/internal/database"
	"github.com/schema-migrate/schema-migrate/internal/model"
)

type Checker struct {
	db database.Database
}

func NewChecker(db database.Database) *Checker {
	return &Checker{db: db}
}

func (c *Checker) CheckSQL(ctx context.Context, sql string) (*model.SecurityCheckResult, error) {
	result := &model.SecurityCheckResult{}

	statements := splitStatements(sql)
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		if err := c.checkStatement(ctx, stmt, result); err != nil {
			return nil, err
		}
	}

	result.IsDangerous = len(result.Warnings) > 0
	for _, w := range result.Warnings {
		if w.Level == model.RiskDanger {
			result.IsDangerous = true
			break
		}
	}

	return result, nil
}

func (c *Checker) CheckMigration(ctx context.Context, mig model.Migration) (*model.SecurityCheckResult, error) {
	return c.CheckSQL(ctx, mig.UpSQL)
}

func (c *Checker) checkStatement(ctx context.Context, stmt string, result *model.SecurityCheckResult) error {
	upperStmt := strings.ToUpper(stmt)

	if isDropTable(stmt, upperStmt) {
		tableName := extractTableName(stmt, "DROP TABLE")
		warning := model.SecurityWarning{
			Level:       model.RiskDanger,
			Operation:   "DROP TABLE",
			Description: "Dropping a table will permanently delete all data in the table",
			TableName:   tableName,
		}
		result.Warnings = append(result.Warnings, warning)
		c.addTableImpact(ctx, tableName, "DROP TABLE", result)
	}

	if isDropColumn(stmt, upperStmt) {
		tableName, colName := extractDropColumn(stmt)
		warning := model.SecurityWarning{
			Level:       model.RiskDanger,
			Operation:   "DROP COLUMN",
			Description: fmt.Sprintf("Dropping column %s will permanently delete all data in that column", colName),
			TableName:   tableName,
		}
		result.Warnings = append(result.Warnings, warning)
		c.addTableImpact(ctx, tableName, "DROP COLUMN", result)
	}

	if isDropIndex(stmt, upperStmt) {
		tableName, idxName := extractDropIndex(stmt)
		warning := model.SecurityWarning{
			Level:       model.RiskWarning,
			Operation:   "DROP INDEX",
			Description: fmt.Sprintf("Dropping index %s may impact query performance", idxName),
			TableName:   tableName,
		}
		result.Warnings = append(result.Warnings, warning)
		c.addTableImpact(ctx, tableName, "DROP INDEX", result)
	}

	if isAlterColumnType(stmt, upperStmt) {
		tableName, oldType, newType := extractAlterColumnType(stmt)
		if isTypeNarrowing(oldType, newType) {
			warning := model.SecurityWarning{
				Level:       model.RiskDanger,
				Operation:   "ALTER COLUMN TYPE (NARROWING)",
				Description: fmt.Sprintf("Changing column type from %s to %s is a narrowing conversion that may cause data truncation", oldType, newType),
				TableName:   tableName,
			}
			result.Warnings = append(result.Warnings, warning)
			c.addTableImpact(ctx, tableName, "ALTER COLUMN TYPE (NARROWING)", result)
		} else {
			warning := model.SecurityWarning{
				Level:       model.RiskWarning,
				Operation:   "ALTER COLUMN TYPE",
				Description: fmt.Sprintf("Changing column type from %s to %s requires table rewrite on some databases", oldType, newType),
				TableName:   tableName,
			}
			result.Warnings = append(result.Warnings, warning)
			c.addTableImpact(ctx, tableName, "ALTER COLUMN TYPE", result)
		}
	}

	if isAddNotNullWithoutDefault(stmt, upperStmt) {
		tableName, colName := extractAddNotNullColumn(stmt)
		warning := model.SecurityWarning{
			Level:       model.RiskDanger,
			Operation:   "ADD NOT NULL WITHOUT DEFAULT",
			Description: fmt.Sprintf("Adding NOT NULL constraint to column %s without a DEFAULT value will fail if the table contains existing rows with NULL values", colName),
			TableName:   tableName,
		}
		result.Warnings = append(result.Warnings, warning)
		c.addTableImpact(ctx, tableName, "ADD NOT NULL WITHOUT DEFAULT", result)
	}

	if isSetNotNullWithoutDefault(stmt, upperStmt) {
		tableName, colName := extractSetNotNullColumn(stmt)
		warning := model.SecurityWarning{
			Level:       model.RiskDanger,
			Operation:   "SET NOT NULL WITHOUT DEFAULT",
			Description: fmt.Sprintf("Setting column %s to NOT NULL without a DEFAULT value may fail if existing rows contain NULL values", colName),
			TableName:   tableName,
		}
		result.Warnings = append(result.Warnings, warning)
		c.addTableImpact(ctx, tableName, "SET NOT NULL WITHOUT DEFAULT", result)
	}

	if isCreateTable(upperStmt) {
		tableName := extractTableName(stmt, "CREATE TABLE")
		c.addTableImpact(ctx, tableName, "CREATE TABLE", result)
	}

	if isAlterTable(upperStmt) {
		tableName := extractTableName(stmt, "ALTER TABLE")
		c.addTableImpact(ctx, tableName, "ALTER TABLE", result)
	}

	return nil
}

func (c *Checker) addTableImpact(ctx context.Context, tableName, operation string, result *model.SecurityCheckResult) {
	if tableName == "" {
		return
	}

	rowCount, err := c.db.EstimateRowCount(ctx, tableName)
	if err != nil {
		rowCount = -1
	}

	impact := model.TableImpact{
		TableName:     tableName,
		EstimatedRows: rowCount,
		Operation:     operation,
	}

	for i, existing := range result.AffectedTables {
		if existing.TableName == tableName {
			if rowCount > existing.EstimatedRows {
				result.AffectedTables[i].EstimatedRows = rowCount
			}
			return
		}
	}

	result.AffectedTables = append(result.AffectedTables, impact)
}

func (c *Checker) CheckDiff(ctx context.Context, diff *model.SchemaDiff) (*model.SecurityCheckResult, error) {
	result := &model.SecurityCheckResult{}

	for _, change := range diff.Changes {
		switch change.Risk {
		case model.RiskDanger:
			warning := model.SecurityWarning{
				Level:       model.RiskDanger,
				Operation:   string(change.Type),
				Description: change.Details,
				TableName:   change.ObjectName,
			}
			result.Warnings = append(result.Warnings, warning)
			tableName := extractTableFromObjectName(change.ObjectName)
			c.addTableImpact(ctx, tableName, change.ObjectType, result)
		case model.RiskWarning:
			warning := model.SecurityWarning{
				Level:       model.RiskWarning,
				Operation:   string(change.Type),
				Description: change.Details,
				TableName:   change.ObjectName,
			}
			result.Warnings = append(result.Warnings, warning)
			tableName := extractTableFromObjectName(change.ObjectName)
			c.addTableImpact(ctx, tableName, change.ObjectType, result)
		}
	}

	result.IsDangerous = len(result.Warnings) > 0
	for _, w := range result.Warnings {
		if w.Level == model.RiskDanger {
			result.IsDangerous = true
			break
		}
	}

	return result, nil
}

func splitStatements(sql string) []string {
	var statements []string
	var current strings.Builder
	inString := false
	var stringChar rune

	for _, r := range sql {
		if inString {
			current.WriteRune(r)
			if r == stringChar {
				inString = false
			}
			continue
		}

		if r == '\'' || r == '"' {
			inString = true
			stringChar = r
			current.WriteRune(r)
			continue
		}

		if r == ';' {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
			continue
		}

		current.WriteRune(r)
	}

	stmt := strings.TrimSpace(current.String())
	if stmt != "" {
		statements = append(statements, stmt)
	}

	return statements
}

func isDropTable(stmt, upper string) bool {
	return regexp.MustCompile(`(?i)DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?`).MatchString(upper)
}

func isDropColumn(stmt, upper string) bool {
	return regexp.MustCompile(`(?i)ALTER\s+TABLE\s+.+DROP\s+COLUMN`).MatchString(upper)
}

func isDropIndex(stmt, upper string) bool {
	return regexp.MustCompile(`(?i)DROP\s+INDEX`).MatchString(upper)
}

func isAlterColumnType(stmt, upper string) bool {
	return regexp.MustCompile(`(?i)ALTER\s+(?:TABLE|COLUMN)\s+.+(?:TYPE|MODIFY|ALTER)\s+.+TYPE`).MatchString(upper) ||
		regexp.MustCompile(`(?i)ALTER\s+TABLE\s+.+MODIFY\s+COLUMN`).MatchString(upper)
}

func isAddNotNullWithoutDefault(stmt, upper string) bool {
	if !regexp.MustCompile(`(?i)ALTER\s+TABLE\s+.+ADD\s+(?:COLUMN\s+)?`).MatchString(upper) {
		return false
	}
	hasNotNull := regexp.MustCompile(`(?i)NOT\s+NULL`).MatchString(upper)
	hasDefault := regexp.MustCompile(`(?i)DEFAULT`).MatchString(upper)
	return hasNotNull && !hasDefault
}

func isSetNotNullWithoutDefault(stmt, upper string) bool {
	if !regexp.MustCompile(`(?i)ALTER\s+(?:TABLE|COLUMN)\s+.+SET\s+NOT\s+NULL`).MatchString(upper) &&
		!regexp.MustCompile(`(?i)ALTER\s+TABLE\s+.+MODIFY\s+.+NOT\s+NULL`).MatchString(upper) {
		return false
	}
	hasDefault := regexp.MustCompile(`(?i)DEFAULT`).MatchString(upper)
	return !hasDefault
}

func isCreateTable(upper string) bool {
	return regexp.MustCompile(`(?i)CREATE\s+TABLE`).MatchString(upper)
}

func isAlterTable(upper string) bool {
	return regexp.MustCompile(`(?i)ALTER\s+TABLE`).MatchString(upper)
}

func extractTableName(stmt, keyword string) string {
	re := regexp.MustCompile(fmt.Sprintf(`(?i)%s\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?["`+"`]?([\\w.]+)["+"`]?", keyword))
	matches := re.FindStringSubmatch(stmt)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

func extractDropColumn(stmt string) (string, string) {
	re := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+["` + "`" + `]?([\w.]+)["` + "`" + `]?\s+DROP\s+(?:COLUMN\s+)?(?:IF\s+EXISTS\s+)?["` + "`" + `]?([\w.]+)["` + "`" + `]?`)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) >= 3 {
		return matches[1], matches[2]
	}
	return "", ""
}

func extractDropIndex(stmt string) (string, string) {
	re := regexp.MustCompile(`(?i)DROP\s+INDEX\s+(?:IF\s+EXISTS\s+)?["` + "`" + `]?([\w.]+)["` + "`" + `]?(?:\s+ON\s+["` + "`" + `]?([\w.]+)["` + "`" + `]?)?`)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) >= 3 {
		return matches[2], matches[1]
	}
	return "", ""
}

func extractAlterColumnType(stmt string) (string, string, string) {
	re := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+["` + "`" + `]?([\w.]+)["` + "`" + `]?\s+(?:ALTER|MODIFY)\s+(?:COLUMN\s+)?["` + "`" + `]?(\w+)["` + "`" + `]?\s+(?:TYPE\s+)?([\w()]+)`)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) >= 4 {
		return matches[1], "", matches[3]
	}
	return "", "", ""
}

func extractAddNotNullColumn(stmt string) (string, string) {
	re := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+["` + "`" + `]?([\w.]+)["` + "`" + `]?\s+ADD\s+(?:COLUMN\s+)?["` + "`" + `]?([\w.]+)["` + "`" + `]?`)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) >= 3 {
		return matches[1], matches[2]
	}
	return "", ""
}

func extractSetNotNullColumn(stmt string) (string, string) {
	re := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+["` + "`" + `]?([\w.]+)["` + "`" + `]?\s+(?:ALTER|MODIFY)\s+(?:COLUMN\s+)?["` + "`" + `]?([\w.]+)["` + "`" + `]?`)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) >= 3 {
		return matches[1], matches[2]
	}
	return "", ""
}

func extractTableFromObjectName(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) > 0 {
		return parts[0]
	}
	return name
}

func isTypeNarrowing(oldType, newType string) bool {
	oldType = strings.ToLower(oldType)
	newType = strings.ToLower(newType)

	varcRegex := regexp.MustCompile(`varchar\((\d+)\)`)
	oldMatch := varcRegex.FindStringSubmatch(oldType)
	newMatch := varcRegex.FindStringSubmatch(newType)
	if oldMatch != nil && newMatch != nil {
		oldLen := regexp.MustCompile(`\d+`).FindString(oldMatch[1])
		newLen := regexp.MustCompile(`\d+`).FindString(newMatch[1])
		oldLenInt, _ := atoi(oldLen)
		newLenInt, _ := atoi(newLen)
		return newLenInt < oldLenInt
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
	if oldOk && newOk {
		return newRank < oldRank
	}

	return false
}

func atoi(s string) (int, error) {
	var result int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			result = result*10 + int(c-'0')
		}
	}
	return result, nil
}
