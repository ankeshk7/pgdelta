package cmd

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Export audit log of all pgDelta activity",
	RunE:  runAudit,
}

func init() {
	rootCmd.AddCommand(auditCmd)
	auditCmd.Flags().String("format", "table", "Output format: table | csv | json")
	auditCmd.Flags().String("out", "", "Output file (default: stdout)")
	auditCmd.Flags().String("branch", "", "Filter by branch name")
	auditCmd.Flags().Int("limit", 100, "Max records to export")
}

type auditRecord struct {
	Timestamp  string `json:"timestamp"`
	EventType  string `json:"event_type"`
	Branch     string `json:"branch"`
	Detail     string `json:"detail"`
	MigType    string `json:"migration_type,omitempty"`
	RowsLoaded int64  `json:"rows_loaded,omitempty"`
}

func runAudit(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	format, _ := cmd.Flags().GetString("format")
	outFile, _ := cmd.Flags().GetString("out")
	branchFilter, _ := cmd.Flags().GetString("branch")
	limit, _ := cmd.Flags().GetInt("limit")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	var records []auditRecord

	// Branch creation events
	branchQuery := `
		SELECT created_at, name, parent_branch, status, merged_at
		FROM pgdelta.branches
		WHERE status != 'deleted'
	`
	if branchFilter != "" {
		branchQuery += fmt.Sprintf(" AND name = '%s'", branchFilter)
	}
	branchQuery += fmt.Sprintf(" ORDER BY created_at DESC LIMIT %d", limit)

	rows, err := dbManager.Branch.Query(ctx, branchQuery)
	if err == nil {
		for rows.Next() {
			var name, parent, status string
			var createdAt time.Time
			var mergedAt *time.Time
			rows.Scan(&createdAt, &name, &parent, &status, &mergedAt)

			records = append(records, auditRecord{
				Timestamp: createdAt.Local().Format("2006-01-02 15:04:05"),
				EventType: "branch_created",
				Branch:    name,
				Detail:    fmt.Sprintf("branched from %s", parent),
			})

			if mergedAt != nil {
				records = append(records, auditRecord{
					Timestamp: mergedAt.Local().Format("2006-01-02 15:04:05"),
					EventType: "branch_merged",
					Branch:    name,
					Detail:    fmt.Sprintf("merged into %s", parent),
				})
			}
		}
		rows.Close()
	}

	// Migration events
	migQuery := `
		SELECT m.applied_at, b.name, m.type,
			COALESCE(m.description,''), m.sql
		FROM pgdelta.branch_migrations m
		JOIN pgdelta.branches b ON b.id = m.branch_id
	`
	if branchFilter != "" {
		migQuery += fmt.Sprintf(" WHERE b.name = '%s'", branchFilter)
	}
	migQuery += fmt.Sprintf(" ORDER BY m.applied_at DESC LIMIT %d", limit)

	rows, err = dbManager.Branch.Query(ctx, migQuery)
	if err == nil {
		for rows.Next() {
			var branchName, migType, desc, sql string
			var appliedAt time.Time
			rows.Scan(&appliedAt, &branchName, &migType, &desc, &sql)

			records = append(records, auditRecord{
				Timestamp: appliedAt.Local().Format("2006-01-02 15:04:05"),
				EventType: "migration_applied",
				Branch:    branchName,
				Detail:    desc,
				MigType:   migType,
			})
		}
		rows.Close()
	}

	// Snapshot events
	snapQuery := `
		SELECT s.completed_at, b.name, s.table_name, s.rows_loaded, s.status
		FROM pgdelta.branch_data_snapshots s
		JOIN pgdelta.branches b ON b.id = s.branch_id
		WHERE s.completed_at IS NOT NULL
	`
	if branchFilter != "" {
		snapQuery += fmt.Sprintf(" AND b.name = '%s'", branchFilter)
	}
	snapQuery += fmt.Sprintf(" ORDER BY s.completed_at DESC LIMIT %d", limit)

	rows, err = dbManager.Branch.Query(ctx, snapQuery)
	if err == nil {
		for rows.Next() {
			var branchName, tableName, status string
			var completedAt *time.Time
			var rowsLoaded int64
			rows.Scan(&completedAt, &branchName, &tableName, &rowsLoaded, &status)
			if completedAt == nil {
				continue
			}
			records = append(records, auditRecord{
				Timestamp:  completedAt.Local().Format("2006-01-02 15:04:05"),
				EventType:  "snapshot_completed",
				Branch:     branchName,
				Detail:     fmt.Sprintf("table: %s", tableName),
				RowsLoaded: rowsLoaded,
			})
		}
		rows.Close()
	}

	// Sort by timestamp descending (simple approach)
	// Already ordered per query, combined result has mixed order
	// For v1.0 this is acceptable

	// Output
	var out *os.File
	if outFile != "" {
		out, err = os.Create(outFile)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer out.Close()
	} else {
		out = os.Stdout
	}

	switch format {
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		encoder.Encode(records)

	case "csv":
		w := csv.NewWriter(out)
		w.Write([]string{"timestamp", "event_type", "branch", "detail", "migration_type", "rows_loaded"})
		for _, r := range records {
			w.Write([]string{
				r.Timestamp, r.EventType, r.Branch, r.Detail,
				r.MigType, fmt.Sprintf("%d", r.RowsLoaded),
			})
		}
		w.Flush()

	default:
		// Table format
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  pgDelta — audit log")
		fmt.Fprintln(out, "  ─────────────────────────────────────────────────────────────")
		fmt.Fprintf(out, "  %-20s  %-22s  %-20s  %s\n",
			"TIMESTAMP", "EVENT", "BRANCH", "DETAIL")
		fmt.Fprintln(out, "  ─────────────────────────────────────────────────────────────")

		for _, r := range records {
			fmt.Fprintf(out, "  %-20s  %-22s  %-20s  %s\n",
				r.Timestamp, r.EventType,
				truncate(r.Branch, 20), truncate(r.Detail, 40))
		}

		fmt.Fprintln(out)
		fmt.Fprintf(out, "  %d event(s) exported\n", len(records))
		if outFile != "" {
			fmt.Fprintf(os.Stdout, "\n  Written to: %s\n\n", outFile)
		}
		fmt.Fprintln(out)
	}

	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n-3] + "..."
	}
	return s
}
