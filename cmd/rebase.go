package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/conflict"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/ankeshkedia/pgdelta/internal/migration"
	"github.com/ankeshkedia/pgdelta/internal/schema"
	"github.com/spf13/cobra"
)

var rebaseCmd = &cobra.Command{
	Use:   "rebase <branch-name>",
	Short: "Replay branch migrations on top of latest parent schema",
	Args:  cobra.ExactArgs(1),
	RunE:  runRebase,
}

func init() {
	rootCmd.AddCommand(rebaseCmd)
	rebaseCmd.Flags().String("onto", "", "Parent branch to rebase onto (default: branch's parent)")
	rebaseCmd.Flags().Bool("auto", false, "Called from git hook — suppress prompts")
	rebaseCmd.Flags().Bool("dry-run", false, "Simulate only — do not apply")
}

func runRebase(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	branchName := args[0]
	onto, _ := cmd.Flags().GetString("onto")
	auto, _ := cmd.Flags().GetBool("auto")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	if !auto {
		fmt.Println()
		fmt.Printf("  pgDelta — rebasing '%s'\n", branchName)
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

	// Look up branch
	var branchID, schemaName, parentBranch string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, schema_name, parent_branch
		FROM pgdelta.branches
		WHERE name = $1 AND status = 'active'
	`, branchName).Scan(&branchID, &schemaName, &parentBranch)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	// Use --onto if provided
	if onto != "" {
		parentBranch = onto
	}

	// Look up parent schema
	var parentSchemaName string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT schema_name FROM pgdelta.branches
		WHERE name = $1 AND status = 'active'
	`, parentBranch).Scan(&parentSchemaName)
	if err != nil {
		parentSchemaName = "branch_main"
	}

	if !auto {
		fmt.Printf("  Branch:  %s\n", branchName)
		fmt.Printf("  Onto:    %s\n", parentBranch)
		fmt.Println()
	}

	// Get all migrations on this branch
	mgr := migration.New(dbManager.Branch)
	migrations, err := mgr.List(ctx, branchID)
	if err != nil {
		return fmt.Errorf("failed to list migrations: %w", err)
	}

	if len(migrations) == 0 {
		fmt.Println("  Nothing to rebase — no migrations on this branch.")
		fmt.Println()
		return nil
	}

	if !auto {
		fmt.Printf("  Replaying %d migration(s) on '%s':\n\n", len(migrations), parentBranch)
		for _, mg := range migrations {
			fmt.Printf("    → #%d  %s  (%s)\n", mg.Sequence, mg.Description, mg.Type)
		}
		fmt.Println()
	}

	// Extract SQLs
	var sqls []string
	for _, mg := range migrations {
		sqls = append(sqls, mg.SQL)
	}

	// Simulate replay against new parent
	if !auto {
		fmt.Print("  Simulating replay...  ")
	}
	sim := conflict.New(dbManager.Branch)
	result, err := sim.Simulate(ctx, parentSchemaName, sqls)
	if err != nil {
		if !auto {
			fmt.Println("✗")
		}
		return fmt.Errorf("simulation failed: %w", err)
	}

	if result.Clean {
		if !auto {
			fmt.Println("✓  no conflicts")
			fmt.Println()
		}
	} else {
		if !auto {
			fmt.Printf("✗  %d conflict(s)\n", len(result.Conflicts))
		}
		conflict.Print(result.Conflicts)

		if !auto {
			fmt.Println()
			fmt.Println("  Rebase has conflicts. Options:")
			fmt.Println("  [1] Skip conflicting migrations")
			fmt.Println("  [2] Abort rebase")
			fmt.Println()
			fmt.Print("  Choice [1/2]: ")

			var choice string
			fmt.Scanln(&choice)
			if strings.TrimSpace(choice) != "1" {
				fmt.Println("  Rebase aborted.")
				return nil
			}
			sqls = filterConflicting(sqls, result.Conflicts)
		} else {
			return fmt.Errorf("rebase conflicts detected")
		}
	}

	if dryRun {
		fmt.Println("  Dry run — no changes applied.")
		fmt.Println()
		return nil
	}

	// Drop and recreate branch schema from new parent
	if !auto {
		fmt.Printf("  Rebuilding schema from '%s'...  ", parentBranch)
	}

	// Drop existing branch schema
	_, err = dbManager.Branch.Exec(ctx,
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName),
	)
	if err != nil {
		if !auto {
			fmt.Println("✗")
		}
		return fmt.Errorf("failed to drop branch schema: %w", err)
	}

	// Recreate from parent's current state
	tables, err := schema.Snapshot(ctx, dbManager.Main)
	if err != nil {
		if !auto {
			fmt.Println("✗")
		}
		return fmt.Errorf("failed to snapshot parent schema: %w", err)
	}

	if err := schema.Recreate(ctx, dbManager.Branch, schemaName, tables); err != nil {
		if !auto {
			fmt.Println("✗")
		}
		return fmt.Errorf("failed to recreate branch schema: %w", err)
	}

	if !auto {
		fmt.Println("✓")
	}

	// Replay migrations on new base
	if !auto {
		fmt.Printf("  Replaying migrations...\n\n")
	}

	replayedCount := 0
	for i, sql := range sqls {
		if sql == "" {
			continue
		}

		_, err := dbManager.Branch.Exec(ctx,
			fmt.Sprintf("SET search_path TO %s, public", schemaName),
		)
		if err != nil {
			return fmt.Errorf("failed to set search path: %w", err)
		}

		_, err = dbManager.Branch.Exec(ctx, sql)
		_, _ = dbManager.Branch.Exec(ctx, "RESET search_path")

		if err != nil {
			if !auto {
				fmt.Printf("  ✗  Migration #%d failed: %v\n", i+1, err)
			}
			return fmt.Errorf("failed to replay migration #%d: %w", i+1, err)
		}

		if !auto {
			fmt.Printf("  ✓  #%d  %s\n", i+1, truncateSQL(sql, 50))
		}
		replayedCount++
	}

	// Update parent branch in metadata
	_, err = dbManager.Branch.Exec(ctx, `
		UPDATE pgdelta.branches
		SET parent_branch = $1
		WHERE id = $2
	`, parentBranch, branchID)
	if err != nil {
		return fmt.Errorf("failed to update parent branch: %w", err)
	}

	if !auto {
		fmt.Println()
		fmt.Println("  ─────────────────────────────────────────")
		fmt.Printf("  Rebase complete.\n")
		fmt.Printf("  %d migration(s) replayed on '%s'.\n", replayedCount, parentBranch)
		fmt.Println("  Data snapshots preserved.")
	}

	return nil
}
