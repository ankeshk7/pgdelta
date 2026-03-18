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
	"github.com/spf13/cobra"
)

var mergeCmd = &cobra.Command{
	Use:   "merge <branch-name>",
	Short: "Merge a branch's migrations into its parent",
	Args:  cobra.ExactArgs(1),
	RunE:  runMerge,
}

func init() {
	rootCmd.AddCommand(mergeCmd)
	mergeCmd.Flags().Bool("auto", false, "Called from git hook — suppress prompts")
	mergeCmd.Flags().Bool("dry-run", false, "Simulate only — do not apply")
	mergeCmd.Flags().String("strategy", "", "Conflict strategy: theirs, ours (default: interactive)")
}

func runMerge(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	branchName := args[0]
	auto, _ := cmd.Flags().GetBool("auto")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	strategy, _ := cmd.Flags().GetString("strategy")

	if !auto {
		fmt.Println()
		fmt.Printf("  pgDelta — merging '%s'\n", branchName)
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

	// Look up source branch
	var branchID, schemaName, parentBranch string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, schema_name, parent_branch
		FROM pgdelta.branches
		WHERE name = $1 AND status = 'active'
	`, branchName).Scan(&branchID, &schemaName, &parentBranch)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	// Look up parent branch schema
	var parentSchemaName string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT schema_name FROM pgdelta.branches
		WHERE name = $1 AND status = 'active'
	`, parentBranch).Scan(&parentSchemaName)
	if err != nil {
		parentSchemaName = "public"
	}

	if !auto {
		fmt.Printf("  Branch:   %s\n", branchName)
		fmt.Printf("  Parent:   %s\n", parentBranch)
		fmt.Println()
	}

	// Get mergeable migrations (DDL + seed only)
	mgr := migration.New(dbManager.Branch)
	migrations, err := mgr.ListMergeable(ctx, branchID)
	if err != nil {
		return fmt.Errorf("failed to list migrations: %w", err)
	}

	if len(migrations) == 0 {
		fmt.Println("  Nothing to merge — no DDL or seed migrations on this branch.")
		fmt.Println()
		return nil
	}

	if !auto {
		fmt.Printf("  Found %d migration(s) to merge:\n\n", len(migrations))
		for _, mg := range migrations {
			icon := "⬆"
			if mg.Type == "seed" {
				icon = "🌱"
			}
			fmt.Printf("    %s  #%d  %s\n", icon, mg.Sequence, mg.Description)
		}
		fmt.Println()
	}

	// Extract SQL strings
	var sqls []string
	for _, mg := range migrations {
		sqls = append(sqls, mg.SQL)
	}

	// Step 1 — Simulate against parent schema
	if !auto {
		fmt.Print("  Simulating against parent...  ")
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
			fmt.Printf("✗  %d conflict(s) detected\n", len(result.Conflicts))
		}
		conflict.Print(result.Conflicts)

		resolved := tryAutoResolve(sqls, result.Conflicts)

		if strategy == "ours" {
			fmt.Println("  Strategy: ours — skipping conflicting migrations")
			sqls = filterConflicting(sqls, result.Conflicts)
		} else if strategy == "theirs" {
			sqls = resolved
			fmt.Println("  Strategy: theirs — applying auto-resolved migrations")
		} else if !auto {
			fmt.Println()
			fmt.Println("  How would you like to resolve?")
			fmt.Println("  [1] Skip conflicting migrations (keep parent)")
			fmt.Println("  [2] Force apply anyway (overwrite parent)")
			fmt.Println("  [3] Abort merge")
			fmt.Println()
			fmt.Print("  Choice [1/2/3]: ")

			var choice string
			fmt.Scanln(&choice)
			choice = strings.TrimSpace(choice)

			switch choice {
			case "1":
				sqls = filterConflicting(sqls, result.Conflicts)
				fmt.Println("  Skipping conflicting migrations.")
			case "2":
				sqls = resolved
				fmt.Println("  Forcing application.")
			default:
				fmt.Println("  Merge aborted.")
				return nil
			}
		} else {
			return fmt.Errorf("conflicts detected — run interactively to resolve")
		}
	}

	// Step 2 — Dry run check
	if dryRun {
		fmt.Println("  Dry run — no changes applied.")
		fmt.Println()
		return nil
	}

	// Step 3 — Apply to parent schema
	if !auto {
		fmt.Printf("  Applying %d migration(s) to '%s'...\n\n", len(sqls), parentBranch)
	}

	appliedCount := 0
	for i, sql := range sqls {
		if sql == "" {
			continue
		}

		_, err := dbManager.Branch.Exec(ctx,
			fmt.Sprintf("SET search_path TO %s, public", parentSchemaName),
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
			return fmt.Errorf("failed to apply migration #%d: %w", i+1, err)
		}

		if !auto {
			fmt.Printf("  ✓  #%d  %s\n", i+1, truncateSQL(sql, 50))
		}
		appliedCount++
	}

	// Step 4 — Mark branch as merged
	_, err = dbManager.Branch.Exec(ctx, `
		UPDATE pgdelta.branches
		SET status = 'merged', merged_at = now()
		WHERE id = $1
	`, branchID)
	if err != nil {
		return fmt.Errorf("failed to mark branch as merged: %w", err)
	}

	if !auto {
		fmt.Println()
		fmt.Println("  ─────────────────────────────────────────")
		fmt.Printf("  Merge complete.\n")
		fmt.Printf("  %d migration(s) applied to '%s'.\n", appliedCount, parentBranch)
		fmt.Printf("  Test data discarded.\n")
		fmt.Printf("  Branch '%s' marked as merged.\n", branchName)
		fmt.Println()
	}

	return nil
}

func filterConflicting(sqls []string, conflicts []conflict.Conflict) []string {
	conflictSeqs := make(map[int]bool)
	for _, c := range conflicts {
		conflictSeqs[c.MigrationSeq-1] = true
	}
	var filtered []string
	for i, sql := range sqls {
		if !conflictSeqs[i] {
			filtered = append(filtered, sql)
		}
	}
	return filtered
}

func tryAutoResolve(sqls []string, conflicts []conflict.Conflict) []string {
	resolved := make([]string, len(sqls))
	copy(resolved, sqls)
	for _, c := range conflicts {
		idx := c.MigrationSeq - 1
		if idx >= 0 && idx < len(resolved) {
			if newSQL, ok := conflict.AutoResolve(resolved[idx], c); ok {
				resolved[idx] = newSQL
			}
		}
	}
	return resolved
}

func truncateSQL(sql string, max int) string {
	sql = strings.TrimSpace(sql)
	sql = strings.ReplaceAll(sql, "\n", " ")
	sql = strings.ReplaceAll(sql, "\t", " ")
	for strings.Contains(sql, "  ") {
		sql = strings.ReplaceAll(sql, "  ", " ")
	}
	if len(sql) > max {
		return sql[:max] + "..."
	}
	return sql
}
