package dependency

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/schema-migrate/schema-migrate/internal/model"
)

type Analyzer struct{}

func NewAnalyzer() *Analyzer {
	return &Analyzer{}
}

func (a *Analyzer) Analyze(migrations []model.Migration) ([]model.DependencyNode, []string, error) {
	var nodes []model.DependencyNode
	var errors []string

	tableToVersion := make(map[string]string)

	for _, mig := range migrations {
		tables := extractTablesFromSQL(mig.UpSQL)
		dependencies := extractDependencies(mig.UpSQL, tableToVersion)

		node := model.DependencyNode{
			Version:   mig.Version,
			TableName: strings.Join(tables, ", "),
			DependsOn: dependencies,
		}
		nodes = append(nodes, node)

		for _, table := range tables {
			tableToVersion[table] = mig.Version
		}
	}

	if err := a.checkCyclicDependencies(nodes); err != nil {
		errors = append(errors, err.Error())
	}

	sorted, err := a.topologicalSort(nodes)
	if err != nil {
		errors = append(errors, err.Error())
	} else {
		nodes = sorted
	}

	return nodes, errors, nil
}

func (a *Analyzer) checkCyclicDependencies(nodes []model.DependencyNode) error {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var dfs func(version string) bool
	dfs = func(version string) bool {
		visited[version] = true
		recStack[version] = true

		node := findNode(nodes, version)
		if node == nil {
			recStack[version] = false
			return false
		}

		for _, dep := range node.DependsOn {
			if !visited[dep] {
				if dfs(dep) {
					return true
				}
			} else if recStack[dep] {
				return true
			}
		}

		recStack[version] = false
		return false
	}

	for _, node := range nodes {
		if !visited[node.Version] {
			if dfs(node.Version) {
				return fmt.Errorf("cyclic dependency detected in migrations. Suggestion: Consider breaking the cycle by creating tables first without foreign keys, then adding foreign keys in a separate migration")
			}
		}
	}

	return nil
}

func (a *Analyzer) topologicalSort(nodes []model.DependencyNode) ([]model.DependencyNode, error) {
	inDegree := make(map[string]int)
	for _, node := range nodes {
		inDegree[node.Version] = 0
	}

	for _, node := range nodes {
		for _, dep := range node.DependsOn {
			if findNode(nodes, dep) != nil {
				inDegree[node.Version]++
			}
		}
	}

	var result []model.DependencyNode
	var queue []string

	for version, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, version)
		}
	}
	sort.Strings(queue)

	for len(queue) > 0 {
		sort.Strings(queue)
		version := queue[0]
		queue = queue[1:]

		node := findNode(nodes, version)
		if node != nil {
			result = append(result, *node)
		}

		var newlyReady []string
		for _, n := range nodes {
			for _, dep := range n.DependsOn {
				if dep == version {
					inDegree[n.Version]--
					if inDegree[n.Version] == 0 {
						newlyReady = append(newlyReady, n.Version)
					}
				}
			}
		}
		sort.Strings(newlyReady)
		queue = append(queue, newlyReady...)
	}

	if len(result) != len(nodes) {
		return nil, fmt.Errorf("unable to resolve migration dependencies, possible cycle detected")
	}

	return result, nil
}

func findNode(nodes []model.DependencyNode, version string) *model.DependencyNode {
	for i := range nodes {
		if nodes[i].Version == version {
			return &nodes[i]
		}
	}
	return nil
}

func extractTablesFromSQL(sql string) []string {
	var tables []string
	seen := make(map[string]bool)

	createTableRegex := regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["` + "`" + `]?([a-zA-Z_][a-zA-Z0-9_]*)["` + "`" + `]?`)
	matches := createTableRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range matches {
		table := strings.ToLower(match[1])
		if !seen[table] {
			tables = append(tables, table)
			seen[table] = true
		}
	}

	alterTableRegex := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+["` + "`" + `]?([a-zA-Z_][a-zA-Z0-9_]*)["` + "`" + `]?`)
	matches = alterTableRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range matches {
		table := strings.ToLower(match[1])
		if !seen[table] {
			tables = append(tables, table)
			seen[table] = true
		}
	}

	return tables
}

func extractDependencies(sql string, tableToVersion map[string]string) []string {
	var dependencies []string
	seen := make(map[string]bool)

	referencesRegex := regexp.MustCompile(`(?i)REFERENCES\s+["` + "`" + `]?([a-zA-Z_][a-zA-Z0-9_]*)["` + "`" + `]?`)
	matches := referencesRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range matches {
		refTable := strings.ToLower(match[1])
		if version, ok := tableToVersion[refTable]; ok {
			if !seen[version] {
				dependencies = append(dependencies, version)
				seen[version] = true
			}
		}
	}

	foreignKeyRegex := regexp.MustCompile(`(?i)FOREIGN\s+KEY\s*\([^)]+\)\s*REFERENCES\s+["` + "`" + `]?([a-zA-Z_][a-zA-Z0-9_]*)["` + "`" + `]?`)
	matches = foreignKeyRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range matches {
		refTable := strings.ToLower(match[1])
		if version, ok := tableToVersion[refTable]; ok {
			if !seen[version] {
				dependencies = append(dependencies, version)
				seen[version] = true
			}
		}
	}

	return dependencies
}

func (a *Analyzer) GetMigrationOrder(migrations []model.Migration) ([]model.Migration, error) {
	nodes, errors, err := a.Analyze(migrations)
	if err != nil {
		return nil, err
	}

	if len(errors) > 0 {
		return nil, fmt.Errorf("dependency analysis errors: %s", strings.Join(errors, "; "))
	}

	versionToMigration := make(map[string]model.Migration)
	for _, mig := range migrations {
		versionToMigration[mig.Version] = mig
	}

	var ordered []model.Migration
	for _, node := range nodes {
		if mig, ok := versionToMigration[node.Version]; ok {
			ordered = append(ordered, mig)
		}
	}

	return ordered, nil
}
