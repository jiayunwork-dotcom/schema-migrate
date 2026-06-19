package migration

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

var migrationFileRegex = regexp.MustCompile(`^(\d{14})_(.+)\.(up|down)\.sql$`)

type Manager struct {
	migrationsDir string
}

func NewManager(migrationsDir string) *Manager {
	return &Manager{
		migrationsDir: migrationsDir,
	}
}

func (m *Manager) Create(name string) (*model.Migration, error) {
	version := time.Now().Format("20060102150405")
	safeName := sanitizeMigrationName(name)
	baseName := fmt.Sprintf("%s_%s", version, safeName)

	upPath := filepath.Join(m.migrationsDir, baseName+".up.sql")
	downPath := filepath.Join(m.migrationsDir, baseName+".down.sql")

	if err := os.MkdirAll(m.migrationsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create migrations directory: %w", err)
	}

	upTemplate := fmt.Sprintf(`-- Migration: %s
-- Generated: %s
-- Description: %s

-- Add your SQL here
`, baseName, time.Now().Format(time.RFC3339), name)

	downTemplate := fmt.Sprintf(`-- Rollback: %s
-- Generated: %s

-- Add your rollback SQL here
`, baseName, time.Now().Format(time.RFC3339))

	if err := os.WriteFile(upPath, []byte(upTemplate), 0644); err != nil {
		return nil, fmt.Errorf("failed to write up migration: %w", err)
	}

	if err := os.WriteFile(downPath, []byte(downTemplate), 0644); err != nil {
		os.Remove(upPath)
		return nil, fmt.Errorf("failed to write down migration: %w", err)
	}

	return &model.Migration{
		Version:  version,
		Name:     safeName,
		UpPath:   upPath,
		DownPath: downPath,
	}, nil
}

func (m *Manager) LoadAll() ([]model.Migration, error) {
	if _, err := os.Stat(m.migrationsDir); os.IsNotExist(err) {
		return []model.Migration{}, nil
	}

	files, err := os.ReadDir(m.migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

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

		fullPath := filepath.Join(m.migrationsDir, file.Name())
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
		mig.Checksum = CalculateChecksum(mig.UpSQL + mig.DownSQL)
		migrations = append(migrations, *mig)
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Order < migrations[j].Order
	})

	return migrations, nil
}

func (m *Manager) GetPending(appliedVersions map[string]model.MigrationRecord) ([]model.Migration, error) {
	all, err := m.LoadAll()
	if err != nil {
		return nil, err
	}

	var pending []model.Migration
	for _, mig := range all {
		if _, applied := appliedVersions[mig.Version]; !applied {
			pending = append(pending, mig)
		}
	}

	return pending, nil
}

func (m *Manager) GetLastApplied(n int, applied map[string]model.MigrationRecord) ([]model.Migration, error) {
	all, err := m.LoadAll()
	if err != nil {
		return nil, err
	}

	var appliedList []model.Migration
	for _, mig := range all {
		if _, ok := applied[mig.Version]; ok {
			mig.IsApplied = true
			rec := applied[mig.Version]
			mig.AppliedAt = &rec.AppliedAt
			appliedList = append(appliedList, mig)
		}
	}

	sort.Slice(appliedList, func(i, j int) bool {
		return appliedList[i].Order > appliedList[j].Order
	})

	if n > len(appliedList) {
		n = len(appliedList)
	}

	return appliedList[:n], nil
}

func (m *Manager) GetMigrationsWithStatus(applied map[string]model.MigrationRecord) ([]model.Migration, error) {
	all, err := m.LoadAll()
	if err != nil {
		return nil, err
	}

	for i := range all {
		if rec, ok := applied[all[i].Version]; ok {
			all[i].IsApplied = true
			all[i].AppliedAt = &rec.AppliedAt
			if rec.Checksum != all[i].Checksum {
				all[i].Checksum = rec.Checksum + "(modified)"
			}
		}
	}

	return all, nil
}

func CalculateChecksum(content string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(hash[:])
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

func (m *Manager) DetectTimestampConflicts(migrations []model.Migration) []model.TimestampConflict {
	var conflicts []model.TimestampConflict

	for i := 0; i < len(migrations)-1; i++ {
		for j := i + 1; j < len(migrations); j++ {
			ts1 := migrations[i].Version[:12]
			ts2 := migrations[j].Version[:12]

			if ts1 == ts2 {
				versions := []string{migrations[i].Version, migrations[j].Version}
				conflict := model.TimestampConflict{
					ConflictingVersions: versions,
					SuggestedOrder:      []string{migrations[i].Version, migrations[j].Version},
					Message: fmt.Sprintf("Migration timestamps %s and %s are too close (same minute). "+
						"This may cause execution order ambiguity when merged from different branches.",
						migrations[i].Version, migrations[j].Version),
				}
				conflicts = append(conflicts, conflict)
			}
		}
	}

	return conflicts
}

func (m *Manager) VerifyChecksums(migrations []model.Migration, applied map[string]model.MigrationRecord) []string {
	var mismatches []string

	for _, mig := range migrations {
		rec, ok := applied[mig.Version]
		if !ok {
			continue
		}

		currentChecksum := CalculateChecksum(mig.UpSQL + mig.DownSQL)
		if currentChecksum != rec.Checksum {
			mismatches = append(mismatches,
				fmt.Sprintf("Migration %s has been modified since it was applied. "+
					"Original checksum: %s, Current checksum: %s",
					mig.Version, rec.Checksum, currentChecksum))
		}
	}

	return mismatches
}
