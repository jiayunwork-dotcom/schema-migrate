package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"

	"github.com/schema-migrate/schema-migrate/internal/model"
)

type Formatter struct {
	jsonOutput bool
	writer     io.Writer
}

func NewFormatter(jsonOutput bool) *Formatter {
	return &Formatter{
		jsonOutput: jsonOutput,
		writer:     os.Stdout,
	}
}

func (f *Formatter) SetWriter(w io.Writer) {
	f.writer = w
}

func (f *Formatter) PrintMigrationStatus(migrations []model.Migration) {
	if f.jsonOutput {
		f.printJSON(migrations)
		return
	}

	table := tablewriter.NewWriter(f.writer)
	table.SetHeader([]string{"Status", "Version", "Name", "Applied At", "Checksum"})
	table.SetAutoWrapText(false)
	table.SetBorder(true)

	for _, mig := range migrations {
		var status string
		var statusColor *color.Color

		if mig.IsApplied {
			if strings.Contains(mig.Checksum, "(modified)") {
				status = "MODIFIED"
				statusColor = color.New(color.FgRed, color.Bold)
			} else {
				status = "APPLIED"
				statusColor = color.New(color.FgGreen, color.Bold)
			}
		} else {
			status = "PENDING"
			statusColor = color.New(color.FgYellow, color.Bold)
		}

		appliedAt := "-"
		if mig.AppliedAt != nil {
			appliedAt = mig.AppliedAt.Format("2006-01-02 15:04:05")
		}

		checksum := mig.Checksum
		if len(checksum) > 16 {
			checksum = checksum[:16] + "..."
		}

		row := []string{
			statusColor.Sprint(status),
			mig.Version,
			mig.Name,
			appliedAt,
			checksum,
		}
		table.Append(row)
	}

	fmt.Fprintln(f.writer)
	color.New(color.FgCyan, color.Bold).Fprintln(f.writer, "Migration Status:")
	table.Render()

	applied := 0
	pending := 0
	modified := 0
	for _, mig := range migrations {
		if mig.IsApplied {
			if strings.Contains(mig.Checksum, "(modified)") {
				modified++
			} else {
				applied++
			}
		} else {
			pending++
		}
	}

	fmt.Fprintln(f.writer)
	fmt.Fprintf(f.writer, "Total: %d | ", len(migrations))
	color.New(color.FgGreen).Fprintf(f.writer, "Applied: %d | ", applied)
	color.New(color.FgYellow).Fprintf(f.writer, "Pending: %d | ", pending)
	if modified > 0 {
		color.New(color.FgRed).Fprintf(f.writer, "Modified: %d", modified)
	}
	fmt.Fprintln(f.writer)
}

func (f *Formatter) PrintDiff(diff *model.SchemaDiff) {
	if f.jsonOutput {
		f.printJSON(diff)
		return
	}

	fmt.Fprintln(f.writer)
	color.New(color.FgCyan, color.Bold).Fprintln(f.writer, "Schema Differences:")
	fmt.Fprintln(f.writer)

	for _, change := range diff.Changes {
		f.printDiffChange(change)
	}

	fmt.Fprintln(f.writer)
	fmt.Fprintf(f.writer, "Summary: ")
	color.New(color.FgGreen).Fprintf(f.writer, "%d safe", diff.SafeCount)
	fmt.Fprintf(f.writer, " | ")
	color.New(color.FgYellow).Fprintf(f.writer, "%d warning", diff.WarningCount)
	fmt.Fprintf(f.writer, " | ")
	color.New(color.FgRed).Fprintf(f.writer, "%d danger", diff.DangerCount)
	fmt.Fprintln(f.writer)
}

func (f *Formatter) printDiffChange(change model.DiffChange) {
	var riskColor *color.Color
	var riskLabel string

	switch change.Risk {
	case model.RiskSafe:
		riskColor = color.New(color.FgGreen)
		riskLabel = "[SAFE]"
	case model.RiskWarning:
		riskColor = color.New(color.FgYellow)
		riskLabel = "[WARNING]"
	case model.RiskDanger:
		riskColor = color.New(color.FgRed)
		riskLabel = "[DANGER]"
	}

	var typeSymbol string
	var typeColor *color.Color
	switch change.Type {
	case model.ChangeAdd:
		typeSymbol = "+"
		typeColor = color.New(color.FgGreen)
	case model.ChangeDrop:
		typeSymbol = "-"
		typeColor = color.New(color.FgRed)
	case model.ChangeModify:
		typeSymbol = "~"
		typeColor = color.New(color.FgYellow)
	case model.ChangeRename:
		typeSymbol = ">"
		typeColor = color.New(color.FgBlue)
	}

	riskColor.Fprintf(f.writer, "%s ", riskLabel)
	typeColor.Fprintf(f.writer, "%s ", typeSymbol)
	fmt.Fprintf(f.writer, "%s: %s\n", change.ObjectType, change.Details)

	fmt.Fprintf(f.writer, "  SQL: ")
	f.printSQLWithDiff(change.SQL, change.Type)
	fmt.Fprintln(f.writer)
}

func (f *Formatter) printSQLWithDiff(sql string, changeType model.DiffChangeType) {
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		if i > 0 {
			fmt.Fprint(f.writer, "       ")
		}
		switch changeType {
		case model.ChangeAdd:
			color.New(color.FgGreen).Fprintln(f.writer, line)
		case model.ChangeDrop:
			color.New(color.FgRed).Fprintln(f.writer, line)
		case model.ChangeModify:
			color.New(color.FgYellow).Fprintln(f.writer, line)
		default:
			fmt.Fprintln(f.writer, line)
		}
	}
}

func (f *Formatter) PrintSecurityCheck(result *model.SecurityCheckResult) {
	if f.jsonOutput {
		f.printJSON(result)
		return
	}

	fmt.Fprintln(f.writer)
	color.New(color.FgCyan, color.Bold).Fprintln(f.writer, "Security Check Results:")
	fmt.Fprintln(f.writer)

	if !result.IsDangerous {
		color.New(color.FgGreen).Fprintln(f.writer, "✓ No dangerous operations detected")
	} else {
		color.New(color.FgRed).Fprintln(f.writer, "⚠ Dangerous operations detected!")
	}

	for _, warning := range result.Warnings {
		var levelColor *color.Color
		var levelSymbol string
		switch warning.Level {
		case model.RiskDanger:
			levelColor = color.New(color.FgRed, color.Bold)
			levelSymbol = "✗"
		case model.RiskWarning:
			levelColor = color.New(color.FgYellow, color.Bold)
			levelSymbol = "!"
		default:
			levelColor = color.New(color.FgGreen)
			levelSymbol = "✓"
		}

		levelColor.Fprintf(f.writer, "  %s [%s] %s\n", levelSymbol, warning.Operation, warning.Description)
		if warning.TableName != "" {
			fmt.Fprintf(f.writer, "    Table: %s\n", warning.TableName)
		}
	}

	if len(result.AffectedTables) > 0 {
		fmt.Fprintln(f.writer)
		color.New(color.FgCyan, color.Bold).Fprintln(f.writer, "Affected Tables:")
		for _, impact := range result.AffectedTables {
			rowCount := "unknown"
			if impact.EstimatedRows >= 0 {
				rowCount = fmt.Sprintf("%d rows", impact.EstimatedRows)
			}
			fmt.Fprintf(f.writer, "  - %s: %s (%s)\n", impact.TableName, impact.Operation, rowCount)
		}
	}
}

func (f *Formatter) PrintDependencies(nodes []model.DependencyNode) {
	if f.jsonOutput {
		f.printJSON(nodes)
		return
	}

	fmt.Fprintln(f.writer)
	color.New(color.FgCyan, color.Bold).Fprintln(f.writer, "Migration Dependencies:")
	fmt.Fprintln(f.writer)

	for _, node := range nodes {
		fmt.Fprintf(f.writer, "  %s: %s\n", node.Version, node.TableName)
		if len(node.DependsOn) > 0 {
			fmt.Fprintf(f.writer, "    ↳ Depends on: %s\n", strings.Join(node.DependsOn, ", "))
		}
	}
}

func (f *Formatter) PrintConflicts(conflicts []model.TimestampConflict) {
	if f.jsonOutput {
		f.printJSON(conflicts)
		return
	}

	if len(conflicts) == 0 {
		color.New(color.FgGreen).Fprintln(f.writer, "✓ No timestamp conflicts detected")
		return
	}

	color.New(color.FgRed, color.Bold).Fprintln(f.writer, "⚠ Timestamp Conflicts Detected:")
	for _, conflict := range conflicts {
		fmt.Fprintln(f.writer)
		color.New(color.FgRed).Fprintf(f.writer, "  Conflict: %s\n", conflict.Message)
		fmt.Fprintf(f.writer, "  Versions: %s\n", strings.Join(conflict.ConflictingVersions, ", "))
		fmt.Fprintf(f.writer, "  Suggested order: %s\n", strings.Join(conflict.SuggestedOrder, " → "))
		fmt.Fprintf(f.writer, "  Recommendation: Manually review and re-order these migrations if needed\n")
	}
}

func (f *Formatter) PrintChecksumMismatches(mismatches []string) {
	if f.jsonOutput {
		f.printJSON(mismatches)
		return
	}

	if len(mismatches) == 0 {
		color.New(color.FgGreen).Fprintln(f.writer, "✓ All checksums match")
		return
	}

	color.New(color.FgRed, color.Bold).Fprintln(f.writer, "⚠ Checksum Mismatches Detected:")
	for _, msg := range mismatches {
		color.New(color.FgRed).Fprintf(f.writer, "  - %s\n", msg)
	}
	fmt.Fprintln(f.writer)
	color.New(color.FgYellow).Fprintln(f.writer, "  Warning: Modifying already-applied migrations can cause inconsistencies!")
	color.New(color.FgYellow).Fprintln(f.writer, "  Recommendation: Create a new migration instead of modifying existing ones")
}

func (f *Formatter) PrintExecutionResult(mig model.Migration, direction string, duration time.Duration, err error) {
	if f.jsonOutput {
		result := map[string]interface{}{
			"version":   mig.Version,
			"name":      mig.Name,
			"direction": direction,
			"duration":  duration.Milliseconds(),
			"success":   err == nil,
		}
		if err != nil {
			result["error"] = err.Error()
		}
		f.printJSON(result)
		return
	}

	if err != nil {
		color.New(color.FgRed).Fprintf(f.writer, "  ✗ %s %s: %s (%.2fs)\n",
			strings.ToUpper(direction), mig.Version, err.Error(), duration.Seconds())
	} else {
		color.New(color.FgGreen).Fprintf(f.writer, "  ✓ %s %s_%s (%.2fs)\n",
			strings.ToUpper(direction), mig.Version, mig.Name, duration.Seconds())
	}
}

func (f *Formatter) PrintDryRunSQL(sql string) {
	if f.jsonOutput {
		f.printJSON(map[string]string{"sql": sql})
		return
	}

	fmt.Fprintln(f.writer)
	color.New(color.FgCyan, color.Bold).Fprintln(f.writer, "--- DRY RUN: SQL to be executed ---")
	fmt.Fprintln(f.writer)

	lines := strings.Split(sql, "\n")
	for _, line := range lines {
		color.New(color.FgHiBlue).Fprintln(f.writer, line)
	}
	fmt.Fprintln(f.writer)
	color.New(color.FgCyan, color.Bold).Fprintln(f.writer, "--- END DRY RUN ---")
}

func (f *Formatter) PrintSuccess(message string) {
	if f.jsonOutput {
		f.printJSON(map[string]string{"status": "success", "message": message})
		return
	}
	color.New(color.FgGreen, color.Bold).Fprintf(f.writer, "\n✓ %s\n", message)
}

func (f *Formatter) PrintError(message string, err error) {
	if f.jsonOutput {
		f.printJSON(map[string]interface{}{"status": "error", "message": message, "error": err.Error()})
		return
	}
	color.New(color.FgRed, color.Bold).Fprintf(f.writer, "\n✗ %s: %v\n", message, err)
}

func (f *Formatter) PrintInfo(message string) {
	if f.jsonOutput {
		return
	}
	color.New(color.FgCyan).Fprintf(f.writer, "ℹ %s\n", message)
}

func (f *Formatter) PrintWarning(message string) {
	if f.jsonOutput {
		return
	}
	color.New(color.FgYellow).Fprintf(f.writer, "⚠ %s\n", message)
}

func (f *Formatter) printJSON(data interface{}) {
	enc := json.NewEncoder(f.writer)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
	}
}
