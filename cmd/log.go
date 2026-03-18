package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var logCmd = &cobra.Command{
	Use:   "log [branch-name]",
	Short: "Show migration history for a branch",
	RunE:  runLog,
}

func init() {
	rootCmd.AddCommand(logCmd)
	logCmd.Flags().Int("limit", 20, "Max number of migrations to show")
	logCmd.Flags().String("type", "", "Filter by type: ddl, seed, test")
}

func runLog(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	limit, _ := cmd.Flags().GetInt("limit")
	typeFilter, _ := cmd.Flags().GetString("type")

	branchName := ""
	if len(args) > 0 {
		branchName = args[0]
	} else {
		branchName = currentGitBranch()
	}
	if branchName == "" {
		return fmt.Errorf("could not detect branch — specify branch name")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	// Get branch ID
	var branchID, parentBranch string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, parent_branch FROM pgdelta.branches
		WHERE name = $1 AND status != 'deleted'
	`, branchName).Scan(&branchID, &parentBranch)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	// Build query
	query := `
		SELECT sequence, sql, type, COALESCE(description,''),
			checksum, applied_at
		FROM pgdelta.branch_migrations
		WHERE branch_id = $1
	`
	queryArgs := []interface{}{branchID}

	if typeFilter != "" {
		query += " AND type = $2"
		queryArgs = append(queryArgs, typeFilter)
		query += fmt.Sprintf(" ORDER BY sequence DESC LIMIT %d", limit)
	} else {
		query += fmt.Sprintf(" ORDER BY sequence DESC LIMIT %d", limit)
	}

	rows, err := dbManager.Branch.Query(ctx, query, queryArgs...)
	if err != nil {
		return fmt.Errorf("failed to query migrations: %w", err)
	}
	defer rows.Close()

	fmt.Println()
	fmt.Printf("  pgDelta log — %s\n", branchName)
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Printf("  Parent: %s\n\n", parentBranch)

	count := 0
	for rows.Next() {
		var seq int
		var sql, migType, desc, checksum string
		var appliedAt time.Time

		if err := rows.Scan(&seq, &sql, &migType, &desc, &checksum, &appliedAt); err != nil {
			continue
		}

		// Type icon and color indicator
		typeIcon := "⬆"
		switch migType {
		case "seed":
			typeIcon = "🌱"
		case "test":
			typeIcon = "🧪"
		}

		mergeLabel := ""
		if migType == "ddl" || migType == "seed" {
			mergeLabel = "  → travels to parent on merge"
		} else {
			mergeLabel = "  → discarded on merge"
		}

		fmt.Printf("  %s  commit %s\n", typeIcon, checksum)
		fmt.Printf("  │  sequence: #%d\n", seq)
		fmt.Printf("  │  type:     %s%s\n", migType, mergeLabel)
		fmt.Printf("  │  time:     %s\n", appliedAt.Local().Format("2006-01-02 15:04:05"))
		if desc != "" {
			fmt.Printf("  │  message:  %s\n", desc)
		}
		fmt.Printf("  │\n")
		fmt.Printf("  │  %s\n", truncateSQL(sql, 70))
		fmt.Println()

		count++
	}

	if count == 0 {
		fmt.Println("  No migrations recorded on this branch.")
		fmt.Println()
		fmt.Println("  Run:  pgdelta migrate \"<sql>\"")
	} else {
		fmt.Printf("  Showing %d migration(s)\n", count)
	}
	fmt.Println()

	return nil
}
