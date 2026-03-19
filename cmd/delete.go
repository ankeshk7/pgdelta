package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:   "delete <branch-name>",
	Short: "Delete a database branch",
	Args:  cobra.ExactArgs(1),
	RunE:  runDelete,
}

func init() {
	rootCmd.AddCommand(deleteCmd)
	deleteCmd.Flags().Bool("force", false, "Force delete even if unmerged migrations exist")
	deleteCmd.Flags().Bool("auto", false, "Called from git hook — suppress prompts")
}

func runDelete(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	branchName := args[0]
	force, _ := cmd.Flags().GetBool("force")
	auto, _ := cmd.Flags().GetBool("auto")

	if !auto {
		fmt.Println()
		fmt.Printf("  pgDelta — deleting branch '%s'\n", branchName)
		fmt.Println("  ─────────────────────────────────────────")
		fmt.Println()
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

	// Look up the branch
	var branchID, schemaName string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, schema_name
		FROM pgdelta.branches
		WHERE name = $1 AND status = 'active'
	`, branchName).Scan(&branchID, &schemaName)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	// Check for unmerged migrations
	if !force {
		var unmergedCount int
		_ = dbManager.Branch.QueryRow(ctx,
			"SELECT COUNT(*) FROM pgdelta.branch_migrations WHERE branch_id = $1",
			branchID,
		).Scan(&unmergedCount)

		if unmergedCount > 0 && !auto {
			fmt.Printf("  ⚠  Branch has %d unmerged migrations.\n", unmergedCount)
			fmt.Println("  These will be lost if you delete this branch.")
			fmt.Println()
			fmt.Print("  Delete anyway? [y/N]: ")
			var confirm string
			fmt.Scanln(&confirm)
			if confirm != "y" && confirm != "Y" {
				fmt.Println("  Aborted.")
				return nil
			}
		}
	}

	// Step 1 — Drop the Postgres schema
	if !auto {
		fmt.Printf("  Dropping schema '%s'...  ", schemaName)
	}
	_, err = dbManager.Branch.Exec(ctx,
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName),
	)
	if err != nil {
		if !auto {
			fmt.Println("✗")
		}
		return fmt.Errorf("failed to drop schema: %w", err)
	}
	if !auto {
		fmt.Println("✓")
	}

	// Step 2 — Hard delete branch and all related data
	if !auto {
		fmt.Print("  Removing metadata...     ")
	}
	// Delete in FK order
	_, _ = dbManager.Branch.Exec(ctx,
		"DELETE FROM pgdelta.branch_data_snapshots WHERE branch_id = $1", branchID)
	_, _ = dbManager.Branch.Exec(ctx,
		"DELETE FROM pgdelta.branch_migrations WHERE branch_id = $1", branchID)
	_, _ = dbManager.Branch.Exec(ctx,
		"DELETE FROM pgdelta.conflict_log WHERE branch_id = $1", branchID)
	_, err = dbManager.Branch.Exec(ctx,
		"DELETE FROM pgdelta.branches WHERE id = $1", branchID)
	if err != nil {
		if !auto {
			fmt.Println("✗")
		}
		return fmt.Errorf("failed to delete metadata: %w", err)
	}
	if !auto {
		fmt.Println("✓")
	}

	if !auto {
		fmt.Println()
		fmt.Println("  ─────────────────────────────────────────")
		fmt.Printf("  Branch '%s' deleted. Storage freed.\n", branchName)
		fmt.Println()
	}

	return nil
}
