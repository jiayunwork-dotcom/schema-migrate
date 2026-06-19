package seed

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/schema-migrate/schema-migrate/internal/model"
)

var seedFileRegex = regexp.MustCompile(`^(\d{14})_(.+?)(?:\.([a-zA-Z0-9_]+))?\.sql$`)

type Manager struct {
	seedsDir string
}

func NewManager(seedsDir string) *Manager {
	return &Manager{
		seedsDir: seedsDir,
	}
}

func (m *Manager) Create(name string) (*model.Seed, error) {
	version := time.Now().Format("20060102150405")
	safeName := sanitizeSeedName(name)
	baseName := fmt.Sprintf("%s_%s", version, safeName)

	fileName := baseName + ".sql"
	fullPath := filepath.Join(m.seedsDir, fileName)

	if err := os.MkdirAll(m.seedsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create seeds directory: %w", err)
	}

	template := fmt.Sprintf(`-- Seed: %s
-- Generated: %s
-- Description: %s

-- Add your INSERT statements here
`, baseName, time.Now().Format(time.RFC3339), name)

	if err := os.WriteFile(fullPath, []byte(template), 0644); err != nil {
		return nil, fmt.Errorf("failed to write seed file: %w", err)
	}

	return &model.Seed{
		Version: version,
		Name:    safeName,
		Path:    fullPath,
	}, nil
}

func (m *Manager) LoadAll() ([]model.Seed, error) {
	if _, err := os.Stat(m.seedsDir); os.IsNotExist(err) {
		return []model.Seed{}, nil
	}

	files, err := os.ReadDir(m.seedsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read seeds directory: %w", err)
	}

	var seeds []model.Seed

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		matches := seedFileRegex.FindStringSubmatch(file.Name())
		if len(matches) != 4 {
			continue
		}

		version := matches[1]
		name := matches[2]
		environment := matches[3]

		fullPath := filepath.Join(m.seedsDir, file.Name())
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read seed file %s: %w", file.Name(), err)
		}

		order, _ := strconv.Atoi(version)
		tables := extractTableNames(string(content))

		seed := model.Seed{
			Version:     version,
			Name:        name,
			Environment: environment,
			SQL:         string(content),
			Path:        fullPath,
			Checksum:    CalculateChecksum(string(content)),
			Order:       order,
			Tables:      tables,
		}

		seeds = append(seeds, seed)
	}

	sort.Slice(seeds, func(i, j int) bool {
		return seeds[i].Order < seeds[j].Order
	})

	return seeds, nil
}

func (m *Manager) FilterByEnvironment(seeds []model.Seed, env string) []model.Seed {
	if env == "" {
		return seeds
	}

	var filtered []model.Seed
	for _, s := range seeds {
		if s.Environment == "" || s.Environment == env {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func (m *Manager) GetPending(seeds []model.Seed, appliedVersions map[string]model.SeedRecord) []model.Seed {
	var pending []model.Seed
	for _, s := range seeds {
		if _, applied := appliedVersions[s.Version]; !applied {
			pending = append(pending, s)
		}
	}
	return pending
}

func (m *Manager) GetSeedsWithStatus(seeds []model.Seed, applied map[string]model.SeedRecord) []model.Seed {
	for i := range seeds {
		if rec, ok := applied[seeds[i].Version]; ok {
			seeds[i].IsApplied = true
			seeds[i].AppliedAt = &rec.AppliedAt
		}
	}
	return seeds
}

func (m *Manager) VerifyChecksums(seeds []model.Seed, applied map[string]model.SeedRecord) []string {
	var mismatches []string

	for _, s := range seeds {
		rec, ok := applied[s.Version]
		if !ok {
			continue
		}

		currentChecksum := CalculateChecksum(s.SQL)
		if currentChecksum != rec.Checksum {
			mismatches = append(mismatches,
				fmt.Sprintf("Seed %s has been modified since it was applied. "+
					"Original checksum: %s, Current checksum: %s",
					s.Version, rec.Checksum, currentChecksum))
		}
	}

	return mismatches
}

func CalculateChecksum(content string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(hash[:])
}

func sanitizeSeedName(name string) string {
	name = strings.ToLower(name)
	name = regexp.MustCompile(`[^a-z0-9_]+`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}

func extractTableNames(sql string) []string {
	tableMap := make(map[string]bool)
	insertRegex := regexp.MustCompile(`(?i)INSERT\s+INTO\s+["` + "`" + `]?([a-zA-Z_][a-zA-Z0-9_]*)["` + "`" + `]?`)

	matches := insertRegex.FindAllStringSubmatch(sql, -1)
	for _, match := range matches {
		if len(match) >= 2 {
			tableMap[strings.ToLower(match[1])] = true
		}
	}

	var tables []string
	for t := range tableMap {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	return tables
}
