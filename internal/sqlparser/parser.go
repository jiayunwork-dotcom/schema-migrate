package sqlparser

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/schema-migrate/schema-migrate/internal/model"
)

type Parser struct {
	tables map[string]*model.Table
}

func NewParser() *Parser {
	return &Parser{
		tables: make(map[string]*model.Table),
	}
}

func (p *Parser) GetSchema() *model.Schema {
	schema := &model.Schema{}
	for _, table := range p.tables {
		schema.Tables = append(schema.Tables, *table)
	}
	return schema
}

func (p *Parser) GetTable(name string) (*model.Table, bool) {
	table, exists := p.tables[name]
	if !exists {
		return nil, false
	}
	return table, true
}

func (p *Parser) Parse(sql string) error {
	statements := splitStatements(sql)
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := p.parseStatement(stmt); err != nil {
			return fmt.Errorf("failed to parse statement: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}

func (p *Parser) ParseStatement(stmt string) error {
	return p.parseStatement(stmt)
}

func (p *Parser) parseStatement(stmt string) error {
	upperStmt := strings.ToUpper(stmt)

	switch {
	case strings.HasPrefix(upperStmt, "CREATE TABLE"):
		return p.parseCreateTable(stmt)
	case strings.HasPrefix(upperStmt, "ALTER TABLE"):
		return p.parseAlterTable(stmt)
	case strings.HasPrefix(upperStmt, "CREATE INDEX") || strings.HasPrefix(upperStmt, "CREATE UNIQUE INDEX"):
		return p.parseCreateIndex(stmt)
	case strings.HasPrefix(upperStmt, "DROP INDEX"):
		return p.parseDropIndex(stmt)
	case strings.HasPrefix(upperStmt, "DROP TABLE"):
		return p.parseDropTable(stmt)
	case strings.HasPrefix(upperStmt, "RENAME TABLE") || strings.HasPrefix(upperStmt, "ALTER TABLE") && strings.Contains(upperStmt, "RENAME TO"):
		return p.parseRenameTable(stmt)
	}

	return nil
}

func (p *Parser) ParseCreateTable(stmt string) error {
	return p.parseCreateTable(stmt)
}

func (p *Parser) parseCreateTable(stmt string) error {
	re := regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?["` + "`" + `]?(\w+)["` + "`" + `]?\s*\(([\s\S]*)\)`)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) < 3 {
		return fmt.Errorf("invalid CREATE TABLE syntax")
	}

	tableName := matches[1]
	body := matches[2]

	table := &model.Table{
		Name: tableName,
	}

	columns, constraints := splitColumnsAndConstraints(body)

	for _, colDef := range columns {
		col := parseColumnDefinition(colDef)
		if col != nil {
			table.Columns = append(table.Columns, *col)
		}
	}

	for _, constraint := range constraints {
		parseTableConstraint(constraint, table)
	}

	p.tables[tableName] = table
	return nil
}

func (p *Parser) parseAlterTable(stmt string) error {
	re := regexp.MustCompile(`(?i)ALTER TABLE ["` + "`" + `]?(\w+)["` + "`" + `]?\s+(.*)`)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) < 3 {
		return fmt.Errorf("invalid ALTER TABLE syntax")
	}

	tableName := matches[1]
	action := strings.TrimSpace(matches[2])

	upperAction := strings.ToUpper(action)

	switch {
	case strings.HasPrefix(upperAction, "ADD COLUMN") || strings.HasPrefix(upperAction, "ADD "):
		return p.parseAddColumn(tableName, action)
	case strings.HasPrefix(upperAction, "DROP COLUMN") || strings.HasPrefix(upperAction, "DROP "):
		return p.parseDropColumn(tableName, action)
	case strings.HasPrefix(upperAction, "MODIFY COLUMN") || strings.HasPrefix(upperAction, "MODIFY "):
		return p.parseModifyColumn(tableName, action)
	case strings.HasPrefix(upperAction, "ALTER COLUMN"):
		return p.parseAlterColumn(tableName, action)
	case strings.HasPrefix(upperAction, "ADD CONSTRAINT") || strings.HasPrefix(upperAction, "ADD "):
		return p.parseAddConstraint(tableName, action)
	case strings.HasPrefix(upperAction, "DROP CONSTRAINT") || strings.HasPrefix(upperAction, "DROP "):
		return p.parseDropConstraint(tableName, action)
	case strings.HasPrefix(upperAction, "ADD FOREIGN KEY"):
		return p.parseAddForeignKey(tableName, action)
	case strings.HasPrefix(upperAction, "DROP FOREIGN KEY"):
		return p.parseDropForeignKey(tableName, action)
	}

	return nil
}

func (p *Parser) parseAddColumn(tableName, action string) error {
	table, exists := p.tables[tableName]
	if !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	re := regexp.MustCompile(`(?i)ADD (?:COLUMN )?["` + "`" + `]?(\w+)["` + "`" + `]?\s+(.*)`)
	matches := re.FindStringSubmatch(action)
	if len(matches) < 3 {
		return fmt.Errorf("invalid ADD COLUMN syntax")
	}

	colName := matches[1]
	colDef := colName + " " + matches[2]
	col := parseColumnDefinition(colDef)
	if col != nil {
		table.Columns = append(table.Columns, *col)
	}

	return nil
}

func (p *Parser) parseDropColumn(tableName, action string) error {
	table, exists := p.tables[tableName]
	if !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	re := regexp.MustCompile(`(?i)DROP (?:COLUMN )?["` + "`" + `]?(\w+)["` + "`" + `]?`)
	matches := re.FindStringSubmatch(action)
	if len(matches) < 2 {
		return fmt.Errorf("invalid DROP COLUMN syntax")
	}

	colName := matches[1]
	for i, col := range table.Columns {
		if strings.EqualFold(col.Name, colName) {
			table.Columns = append(table.Columns[:i], table.Columns[i+1:]...)
			break
		}
	}

	var newIndexes []model.Index
	for _, idx := range table.Indexes {
		hasColumn := false
		for _, c := range idx.Columns {
			if strings.EqualFold(c, colName) {
				hasColumn = true
				break
			}
		}
		if !hasColumn {
			newIndexes = append(newIndexes, idx)
		}
	}
	table.Indexes = newIndexes

	return nil
}

func (p *Parser) parseAlterColumn(tableName, action string) error {
	table, exists := p.tables[tableName]
	if !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	upperAction := strings.ToUpper(action)

	if strings.Contains(upperAction, "TYPE") {
		return p.parseModifyColumn(tableName, action)
	}

	re := regexp.MustCompile(`(?i)ALTER COLUMN ["` + "`" + `]?(\w+)["` + "`" + `]?\s+(.*)`)
	matches := re.FindStringSubmatch(action)
	if len(matches) < 3 {
		return fmt.Errorf("invalid ALTER COLUMN syntax")
	}

	colName := matches[1]
	colAction := strings.TrimSpace(matches[2])
	upperColAction := strings.ToUpper(colAction)

	for i := range table.Columns {
		if strings.EqualFold(table.Columns[i].Name, colName) {
			switch {
			case strings.HasPrefix(upperColAction, "SET DEFAULT"):
				defaultRe := regexp.MustCompile(`(?i)SET DEFAULT\s+(.+)`)
				defaultMatches := defaultRe.FindStringSubmatch(colAction)
				if len(defaultMatches) >= 2 {
					defaultVal := strings.TrimSpace(defaultMatches[1])
					defaultVal = strings.Trim(defaultVal, "'\"`")
					table.Columns[i].DefaultValue = &defaultVal
				}
			case strings.HasPrefix(upperColAction, "DROP DEFAULT"):
				table.Columns[i].DefaultValue = nil
			case strings.HasPrefix(upperColAction, "SET NOT NULL"):
				table.Columns[i].Nullable = false
			case strings.HasPrefix(upperColAction, "DROP NOT NULL"):
				table.Columns[i].Nullable = true
			}
			break
		}
	}

	return nil
}

func (p *Parser) parseModifyColumn(tableName, action string) error {
	table, exists := p.tables[tableName]
	if !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	re := regexp.MustCompile(`(?i)(?:MODIFY|ALTER) (?:COLUMN )?["` + "`" + `]?(\w+)["` + "`" + `]?\s+(.*)`)
	matches := re.FindStringSubmatch(action)
	if len(matches) < 3 {
		return fmt.Errorf("invalid MODIFY COLUMN syntax")
	}

	colName := matches[1]
	colDef := colName + " " + matches[2]
	newCol := parseColumnDefinition(colDef)
	if newCol != nil {
		for i, col := range table.Columns {
			if strings.EqualFold(col.Name, colName) {
				oldAutoIncrement := col.AutoIncrement
				oldIsPrimaryKey := col.IsPrimaryKey
				table.Columns[i] = *newCol
				if oldAutoIncrement {
					table.Columns[i].AutoIncrement = true
				}
				if oldIsPrimaryKey {
					table.Columns[i].IsPrimaryKey = true
					table.Columns[i].Nullable = false
				}
				break
			}
		}
	}

	return nil
}

func (p *Parser) parseAddConstraint(tableName, action string) error {
	table, exists := p.tables[tableName]
	if !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	parseTableConstraint(action, table)
	return nil
}

func (p *Parser) parseDropConstraint(tableName, action string) error {
	table, exists := p.tables[tableName]
	if !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	re := regexp.MustCompile(`(?i)DROP CONSTRAINT ["` + "`" + `]?(\w+)["` + "`" + `]?`)
	matches := re.FindStringSubmatch(action)
	if len(matches) < 2 {
		return nil
	}

	constraintName := matches[1]

	for i, uc := range table.UniqueConstraints {
		if strings.EqualFold(uc.Name, constraintName) {
			table.UniqueConstraints = append(table.UniqueConstraints[:i], table.UniqueConstraints[i+1:]...)
			return nil
		}
	}

	for i, cc := range table.CheckConstraints {
		if strings.EqualFold(cc.Name, constraintName) {
			table.CheckConstraints = append(table.CheckConstraints[:i], table.CheckConstraints[i+1:]...)
			return nil
		}
	}

	return nil
}

func (p *Parser) parseAddForeignKey(tableName, action string) error {
	table, exists := p.tables[tableName]
	if !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	fk := parseForeignKeyConstraint(action)
	if fk != nil {
		table.ForeignKeys = append(table.ForeignKeys, *fk)
	}

	return nil
}

func (p *Parser) parseDropForeignKey(tableName, action string) error {
	table, exists := p.tables[tableName]
	if !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	re := regexp.MustCompile(`(?i)DROP FOREIGN KEY ["` + "`" + `]?(\w+)["` + "`" + `]?`)
	matches := re.FindStringSubmatch(action)
	if len(matches) < 2 {
		return nil
	}

	fkName := matches[1]
	for i, fk := range table.ForeignKeys {
		if strings.EqualFold(fk.Name, fkName) {
			table.ForeignKeys = append(table.ForeignKeys[:i], table.ForeignKeys[i+1:]...)
			break
		}
	}

	return nil
}

func (p *Parser) parseCreateIndex(stmt string) error {
	re := regexp.MustCompile(`(?i)CREATE (UNIQUE )?INDEX (?:IF NOT EXISTS )?["` + "`" + `]?(\w+)["` + "`" + `]?\s+ON\s+["` + "`" + `]?(\w+)["` + "`" + `]?\s*\(([\s\S]*?)\)`)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) < 5 {
		return fmt.Errorf("invalid CREATE INDEX syntax")
	}

	isUnique := matches[1] != ""
	indexName := matches[2]
	tableName := matches[3]
	columnsStr := matches[4]

	columns := parseColumnList(columnsStr)

	table, exists := p.tables[tableName]
	if !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	idx := model.Index{
		Name:    indexName,
		Columns: columns,
		Unique:  isUnique,
	}

	table.Indexes = append(table.Indexes, idx)
	return nil
}

func (p *Parser) parseDropIndex(stmt string) error {
	re := regexp.MustCompile(`(?i)DROP INDEX (?:IF EXISTS )?["` + "`" + `]?(\w+)["` + "`" + `]?(?:\s+ON\s+["` + "`" + `]?(\w+)["` + "`" + `]?)?`)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) < 2 {
		return fmt.Errorf("invalid DROP INDEX syntax")
	}

	indexName := matches[1]
	tableName := matches[2]

	if tableName != "" {
		table, exists := p.tables[tableName]
		if !exists {
			return nil
		}
		for i, idx := range table.Indexes {
			if strings.EqualFold(idx.Name, indexName) {
				table.Indexes = append(table.Indexes[:i], table.Indexes[i+1:]...)
				break
			}
		}
	} else {
		for _, table := range p.tables {
			for i, idx := range table.Indexes {
				if strings.EqualFold(idx.Name, indexName) {
					table.Indexes = append(table.Indexes[:i], table.Indexes[i+1:]...)
					break
				}
			}
		}
	}

	return nil
}

func (p *Parser) parseDropTable(stmt string) error {
	re := regexp.MustCompile(`(?i)DROP TABLE (?:IF EXISTS )?["` + "`" + `]?(\w+)["` + "`" + `]?`)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) < 2 {
		return fmt.Errorf("invalid DROP TABLE syntax")
	}

	tableName := matches[1]
	delete(p.tables, tableName)
	return nil
}

func (p *Parser) parseRenameTable(stmt string) error {
	var oldName, newName string

	if strings.Contains(strings.ToUpper(stmt), "RENAME TO") {
		re := regexp.MustCompile(`(?i)ALTER TABLE ["` + "`" + `]?(\w+)["` + "`" + `]?\s+RENAME TO\s+["` + "`" + `]?(\w+)["` + "`" + `]?`)
		matches := re.FindStringSubmatch(stmt)
		if len(matches) < 3 {
			return fmt.Errorf("invalid RENAME TO syntax")
		}
		oldName = matches[1]
		newName = matches[2]
	} else {
		re := regexp.MustCompile(`(?i)RENAME TABLE\s+["` + "`" + `]?(\w+)["` + "`" + `]?\s+TO\s+["` + "`" + `]?(\w+)["` + "`" + `]?`)
		matches := re.FindStringSubmatch(stmt)
		if len(matches) < 3 {
			return fmt.Errorf("invalid RENAME TABLE syntax")
		}
		oldName = matches[1]
		newName = matches[2]
	}

	table, exists := p.tables[oldName]
	if !exists {
		return nil
	}

	table.Name = newName
	p.tables[newName] = table
	delete(p.tables, oldName)

	return nil
}

func splitStatements(sql string) []string {
	var statements []string
	var current strings.Builder
	inString := false
	stringChar := rune(0)
	parenDepth := 0

	for i, r := range sql {
		switch r {
		case '\'', '"', '`':
			if !inString {
				inString = true
				stringChar = r
			} else if r == stringChar {
				if i+1 < len(sql) && rune(sql[i+1]) == r {
					current.WriteRune(r)
					continue
				}
				inString = false
				stringChar = 0
			}
		case '(':
			if !inString {
				parenDepth++
			}
		case ')':
			if !inString {
				parenDepth--
			}
		case ';':
			if !inString && parenDepth == 0 {
				stmt := strings.TrimSpace(current.String())
				if stmt != "" {
					statements = append(statements, stmt)
				}
				current.Reset()
				continue
			}
		}
		current.WriteRune(r)
	}

	stmt := strings.TrimSpace(current.String())
	if stmt != "" {
		statements = append(statements, stmt)
	}

	return statements
}

func splitColumnsAndConstraints(body string) ([]string, []string) {
	var columns []string
	var constraints []string
	var current strings.Builder
	inString := false
	stringChar := rune(0)
	parenDepth := 0

	for i, r := range body {
		switch r {
		case '\'', '"', '`':
			if !inString {
				inString = true
				stringChar = r
			} else if r == stringChar {
				if i+1 < len(body) && rune(body[i+1]) == r {
					current.WriteRune(r)
					continue
				}
				inString = false
				stringChar = 0
			}
		case '(':
			if !inString {
				parenDepth++
			}
		case ')':
			if !inString {
				parenDepth--
			}
		case ',':
			if !inString && parenDepth == 0 {
				part := strings.TrimSpace(current.String())
				upperPart := strings.ToUpper(part)
				if strings.HasPrefix(upperPart, "PRIMARY KEY") ||
					strings.HasPrefix(upperPart, "FOREIGN KEY") ||
					strings.HasPrefix(upperPart, "UNIQUE") ||
					strings.HasPrefix(upperPart, "CHECK") ||
					strings.HasPrefix(upperPart, "CONSTRAINT") {
					constraints = append(constraints, part)
				} else {
					columns = append(columns, part)
				}
				current.Reset()
				continue
			}
		}
		current.WriteRune(r)
	}

	part := strings.TrimSpace(current.String())
	if part != "" {
		upperPart := strings.ToUpper(part)
		if strings.HasPrefix(upperPart, "PRIMARY KEY") ||
			strings.HasPrefix(upperPart, "FOREIGN KEY") ||
			strings.HasPrefix(upperPart, "UNIQUE") ||
			strings.HasPrefix(upperPart, "CHECK") ||
			strings.HasPrefix(upperPart, "CONSTRAINT") {
			constraints = append(constraints, part)
		} else {
			columns = append(columns, part)
		}
	}

	return columns, constraints
}

func parseColumnDefinition(def string) *model.Column {
	def = strings.TrimSpace(def)
	re := regexp.MustCompile(`^["` + "`" + `]?(\w+)["` + "`" + `]?\s+(\S+(?:\s*\([^)]+\))?)`)
	matches := re.FindStringSubmatch(def)
	if len(matches) < 3 {
		return nil
	}

	col := &model.Column{
		Name: matches[1],
		Type: strings.ToLower(matches[2]),
	}

	upperDef := strings.ToUpper(def)
	upperType := strings.ToUpper(matches[2])

	if strings.Contains(upperDef, "NOT NULL") {
		col.Nullable = false
	} else {
		col.Nullable = true
	}

	if strings.Contains(upperDef, "PRIMARY KEY") {
		col.IsPrimaryKey = true
		col.Nullable = false
	}

	if strings.Contains(upperDef, "UNIQUE") {
		col.IsUnique = true
	}

	if strings.Contains(upperDef, "AUTO_INCREMENT") || strings.Contains(upperDef, "AUTOINCREMENT") ||
		strings.Contains(upperType, "SERIAL") || strings.Contains(upperDef, "IDENTITY") {
		col.AutoIncrement = true
	}

	if strings.Contains(upperType, "SERIAL") {
		if strings.Contains(upperType, "BIGSERIAL") {
			col.Type = "bigint"
		} else if strings.Contains(upperType, "SMALLSERIAL") {
			col.Type = "smallint"
		} else {
			col.Type = "integer"
		}
		col.AutoIncrement = true
	}

	defaultRe := regexp.MustCompile(`(?i)DEFAULT\s+(.+?)(?:\s+|$)`)
	defaultMatches := defaultRe.FindStringSubmatch(def)
	if len(defaultMatches) >= 2 {
		defaultVal := strings.TrimSpace(defaultMatches[1])
		defaultVal = strings.Trim(defaultVal, "'\"`")
		col.DefaultValue = &defaultVal
	}

	return col
}

func parseTableConstraint(constraint string, table *model.Table) {
	upperConstraint := strings.ToUpper(constraint)

	nameRe := regexp.MustCompile(`(?i)CONSTRAINT\s+["` + "`" + `]?(\w+)["` + "`" + `]?`)
	nameMatches := nameRe.FindStringSubmatch(constraint)
	var constraintName string
	if len(nameMatches) >= 2 {
		constraintName = nameMatches[1]
	}

	if strings.Contains(upperConstraint, "PRIMARY KEY") {
		re := regexp.MustCompile(`(?i)PRIMARY KEY\s*\(([\s\S]*?)\)`)
		matches := re.FindStringSubmatch(constraint)
		if len(matches) >= 2 {
			columns := parseColumnList(matches[1])
			for _, colName := range columns {
				for i := range table.Columns {
					if strings.EqualFold(table.Columns[i].Name, colName) {
						table.Columns[i].IsPrimaryKey = true
						table.Columns[i].Nullable = false
					}
				}
			}
		}
	} else if strings.Contains(upperConstraint, "FOREIGN KEY") {
		fk := parseForeignKeyConstraint(constraint)
		if fk != nil {
			if constraintName != "" && fk.Name == "" {
				fk.Name = constraintName
			}
			table.ForeignKeys = append(table.ForeignKeys, *fk)
		}
	} else if strings.Contains(upperConstraint, "UNIQUE") {
		re := regexp.MustCompile(`(?i)UNIQUE\s*\(([\s\S]*?)\)`)
		matches := re.FindStringSubmatch(constraint)
		if len(matches) >= 2 {
			columns := parseColumnList(matches[1])
			uc := model.UniqueConstraint{
				Name:    constraintName,
				Columns: columns,
			}
			if uc.Name == "" {
				uc.Name = fmt.Sprintf("%s_%s_unique", table.Name, strings.Join(columns, "_"))
			}
			table.UniqueConstraints = append(table.UniqueConstraints, uc)
		}
	} else if strings.Contains(upperConstraint, "CHECK") {
		re := regexp.MustCompile(`(?i)CHECK\s*\(([\s\S]*)\)`)
		matches := re.FindStringSubmatch(constraint)
		if len(matches) >= 2 {
			cc := model.CheckConstraint{
				Name:       constraintName,
				Expression: strings.TrimSpace(matches[1]),
			}
			table.CheckConstraints = append(table.CheckConstraints, cc)
		}
	}
}

func parseForeignKeyConstraint(constraint string) *model.ForeignKey {
	fk := &model.ForeignKey{}

	nameRe := regexp.MustCompile(`(?i)CONSTRAINT\s+["` + "`" + `]?(\w+)["` + "`" + `]?`)
	nameMatches := nameRe.FindStringSubmatch(constraint)
	if len(nameMatches) >= 2 {
		fk.Name = nameMatches[1]
	}

	colsRe := regexp.MustCompile(`(?i)FOREIGN KEY\s*\(([\s\S]*?)\)`)
	colsMatches := colsRe.FindStringSubmatch(constraint)
	if len(colsMatches) >= 2 {
		fk.Columns = parseColumnList(colsMatches[1])
	}

	refRe := regexp.MustCompile(`(?i)REFERENCES\s+["` + "`" + `]?(\w+)["` + "`" + `]?\s*\(([\s\S]*?)\)`)
	refMatches := refRe.FindStringSubmatch(constraint)
	if len(refMatches) >= 3 {
		fk.RefTable = refMatches[1]
		fk.RefColumns = parseColumnList(refMatches[2])
	}

	onDeleteRe := regexp.MustCompile(`(?i)ON DELETE\s+(\w+)`)
	onDeleteMatches := onDeleteRe.FindStringSubmatch(constraint)
	if len(onDeleteMatches) >= 2 {
		fk.OnDelete = onDeleteMatches[1]
	}

	onUpdateRe := regexp.MustCompile(`(?i)ON UPDATE\s+(\w+)`)
	onUpdateMatches := onUpdateRe.FindStringSubmatch(constraint)
	if len(onUpdateMatches) >= 2 {
		fk.OnUpdate = onUpdateMatches[1]
	}

	if fk.RefTable != "" && len(fk.Columns) > 0 && len(fk.RefColumns) > 0 {
		return fk
	}

	return nil
}

func parseColumnList(cols string) []string {
	var columns []string
	parts := strings.Split(cols, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "\"`")
		if part != "" {
			columns = append(columns, part)
		}
	}
	return columns
}
