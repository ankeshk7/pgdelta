package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all database branches",
	RunE:  runList,
}

func init() {
	rootCmd.AddCommand(listCmd)
	listCmd.Flags().Bool("all", false, "Show merged and deleted branches too")
}

func runList(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	showAll, _ := cmd.Flags().GetBool("all")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	statusFilter := "b.status IN ('active', 'merged')"
	if showAll {
		statusFilter = "b.status != 'deleted'"
	}

	rows, err := dbManager.Branch.Query(ctx, fmt.Sprintf(`
		SELECT
			b.name,
			b.parent_branch,
			b.status,
			b.created_at,
			COUNT(m.id) AS migration_count,
			COUNT(s.id) AS snapshot_count
		FROM pgdelta.branches b
		LEFT JOIN pgdelta.branch_migrations m ON m.branch_id = b.id
		LEFT JOIN pgdelta.branch_data_snapshots s
			ON s.branch_id = b.id AND s.status = 'ready'
		WHERE %s
		GROUP BY b.id, b.name, b.parent_branch, b.status, b.created_at
		ORDER BY b.status ASC, b.created_at DESC
	`, statusFilter))
	if err != nil {
		return fmt.Errorf("failed to query branches: %w", err)
	}
	defer rows.Close()

	type BranchRow struct {
		Name           string
		ParentBranch   string
		Status         string
		CreatedAt      time.Time
		MigrationCount int
		SnapshotCount  int
	}

	var branches []BranchRow
	for rows.Next() {
		var b BranchRow
		if err := rows.Scan(
			&b.Name,
			&b.ParentBranch,
			&b.Status,
			&b.CreatedAt,
			&b.MigrationCount,
			&b.SnapshotCount,
		); err != nil {
			return fmt.Errorf("failed to scan branch: %w", err)
		}
		branches = append(branches, b)
	}

	fmt.Println()
	fmt.Println("  pgDelta — branches")
	fmt.Println("  ──────────────────────────────────────────────────────────────")
	fmt.Printf("  %-3s  %-25s  %-15s  %-10s  %-8s  %s\n",
		"", "BRANCH", "PARENT", "MIGRATIONS", "SNAPSHOTS", "CREATED")
	fmt.Println("  ──────────────────────────────────────────────────────────────")

	if len(branches) == 0 {
		fmt.Println("  No branches found.")
		fmt.Println("  Run: git checkout -b <branch-name>")
	}

	for _, b := range branches {
		age := formatAge(time.Since(b.CreatedAt))
		icon := "⑂"
		switch b.Status {
		case "merged":
			icon = "✓"
		case "deleted":
			icon = "✗"
		}
		fmt.Printf("  %-3s  %-25s  %-15s  %-10d  %-8d  %s\n",
			icon,
			b.Name,
			b.ParentBranch,
			b.MigrationCount,
			b.SnapshotCount,
			age,
		)
	}

	fmt.Println()
	fmt.Println("  Legend:  ⑂ active   ✓ merged   ✗ deleted")
	fmt.Println()
	return nil
}

func formatAge(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
