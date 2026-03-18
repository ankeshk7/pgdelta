package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current branch status",
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	gitBranch := currentGitBranch()

	fmt.Println()
	fmt.Println("  pgDelta — status")
	fmt.Println("  ─────────────────────────────────────────")

	if gitBranch != "" {
		fmt.Printf("  Git branch:   %s\n", gitBranch)
	}

	// Look up current branch in metadata
	var (
		branchID     string
		name         string
		parentBranch string
		schemaName   string
		createdAt    time.Time
	)

	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, name, parent_branch, schema_name, created_at
		FROM pgdelta.branches
		WHERE git_branch = $1 AND status = 'active'
	`, gitBranch).Scan(&branchID, &name, &parentBranch, &schemaName, &createdAt)

	if err != nil {
		fmt.Printf("  DB branch:    not found for '%s'\n", gitBranch)
		fmt.Println()
		fmt.Printf("  Run: pgdelta create %s\n", gitBranch)
		fmt.Println()
		return nil
	}

	fmt.Printf("  DB branch:    %s\n", name)
	fmt.Printf("  Parent:       %s\n", parentBranch)
	fmt.Printf("  Schema:       %s\n", schemaName)
	fmt.Printf("  Created:      %s\n", formatAge(time.Since(createdAt)))

	// Count migrations on this branch
	var migrationCount int
	_ = dbManager.Branch.QueryRow(ctx,
		"SELECT COUNT(*) FROM pgdelta.branch_migrations WHERE branch_id = $1",
		branchID,
	).Scan(&migrationCount)

	fmt.Printf("  Migrations:   %d ahead of %s\n", migrationCount, parentBranch)

	// Check if parent has new migrations since we branched
	var parentMigCount int
	_ = dbManager.Branch.QueryRow(ctx, `
		SELECT COUNT(*) FROM pgdelta.branch_migrations
		WHERE branch_id = (
			SELECT id FROM pgdelta.branches
			WHERE name = $1 AND status = 'active'
		)
	`, parentBranch).Scan(&parentMigCount)

	if parentMigCount > 0 {
		fmt.Println()
		fmt.Printf("  ⚠  Parent '%s' has %d migration(s) you don't have\n",
			parentBranch, parentMigCount)
		fmt.Printf("     Run: pgdelta rebase %s --onto %s\n", name, parentBranch)
	}

	// List snapshots
	snapRows, err := dbManager.Branch.Query(ctx, `
		SELECT table_name, status, rows_loaded, row_count
		FROM pgdelta.branch_data_snapshots
		WHERE branch_id = $1
		ORDER BY table_name
	`, branchID)
	if err == nil {
		defer snapRows.Close()

		type SnapRow struct {
			Table   string
			Status  string
			Loaded  int64
			Total   int64
		}

		var snaps []SnapRow
		for snapRows.Next() {
			var s SnapRow
			_ = snapRows.Scan(&s.Table, &s.Status, &s.Loaded, &s.Total)
			snaps = append(snaps, s)
		}

		fmt.Println()
		if len(snaps) == 0 {
			fmt.Println("  Snapshots:    none yet — tables load on first query")
		} else {
			fmt.Printf("  %-5s  %-20s  %-10s  %s\n", "", "TABLE", "STATUS", "ROWS")
			fmt.Println("  ──────────────────────────────────────────")
			for _, s := range snaps {
				icon := "◌"
				switch s.Status {
				case "ready":
					icon = "✓"
				case "loading":
					icon = "⟳"
				case "paused":
					icon = "⏸"
				case "failed":
					icon = "✗"
				}
				fmt.Printf("  %-5s  %-20s  %-10s  %d\n",
					icon, s.Table, s.Status, s.Loaded)
			}
		}
	}

	fmt.Println()
	return nil
}

func currentGitBranch() string {
	out, err := exec.Command("git", "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
