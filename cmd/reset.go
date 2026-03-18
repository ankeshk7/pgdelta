package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/ankeshkedia/pgdelta/internal/schema"
	"github.com/spf13/cobra"
)

var resetCmd = &cobra.Command{
	Use:   "reset [branch-name]",
	Short: "Undo the last migration(s) on a branch",
	RunE:  runReset,
}

func init() {
	rootCmd.AddCommand(resetCmd)
	resetCmd.Flags().Int("steps", 1, "Number of migrations to undo")
	resetCmd.Flags().Bool("force", false, "Skip confirmation prompt")
}

type resetMigRow struct {
	ID   string
	Seq  int
	SQL  string
	Type string
	Desc string
}

func runReset(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	steps, _ := cmd.Flags().GetInt("steps")
	force, _ := cmd.Flags().GetBool("force")

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

	// Get branch info
	var branchID, schemaName string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, schema_name FROM pgdelta.branches
		WHERE name = $1 AND status = 'active'
	`, branchName).Scan(&branchID, &schemaName)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	// Get last N migrations to undo
	rows, err := dbManager.Branch.Query(ctx, `
		SELECT id, sequence, sql, type, COALESCE(description,'')
		FROM pgdelta.branch_migrations
		WHERE branch_id = $1
		ORDER BY sequence DESC
		LIMIT $2
	`, branchID, steps)
	if err != nil {
		return fmt.Errorf("failed to query migrations: %w", err)
	}

	var toUndo []resetMigRow
	for rows.Next() {
		var m resetMigRow
		rows.Scan(&m.ID, &m.Seq, &m.SQL, &m.Type, &m.Desc)
		toUndo = append(toUndo, m)
	}
	rows.Close()

	if len(toUndo) == 0 {
		fmt.Println()
		fmt.Println("  No migrations to undo.")
		fmt.Println()
		return nil
	}

	fmt.Println()
	fmt.Printf("  pgDelta — reset '%s'\n", branchName)
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  Will undo %d migration(s):\n\n", len(toUndo))

	for _, m := range toUndo {
		fmt.Printf("    #%d [%s]  %s\n", m.Seq, m.Type, m.Desc)
		fmt.Printf("         %s\n\n", truncateSQL(m.SQL, 60))
	}

	// Confirm
	if !force {
		fmt.Print("  Undo these migrations? [y/N]: ")
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
			fmt.Println("  Reset aborted.")
			return nil
		}
		fmt.Println()
	}

	// Build set of IDs to remove
	removeIDs := make(map[string]bool)
	for _, m := range toUndo {
		removeIDs[m.ID] = true
	}

	// Get all remaining migrations
	allRows, err := dbManager.Branch.Query(ctx, `
		SELECT id, sequence, sql, type FROM pgdelta.branch_migrations
		WHERE branch_id = $1 ORDER BY sequence ASC
	`, branchID)
	if err != nil {
		return fmt.Errorf("failed to query migrations: %w", err)
	}

	type remainingMig struct {
		ID  string
		Seq int
		SQL string
	}
	var remaining []remainingMig
	for allRows.Next() {
		var m remainingMig
		var migType string
		allRows.Scan(&m.ID, &m.Seq, &m.SQL, &migType)
		if !removeIDs[m.ID] {
			remaining = append(remaining, m)
		}
	}
	allRows.Close()

	// Step 1 — Drop branch schema
	fmt.Print("  Dropping branch schema...     ")
	_, err = dbManager.Branch.Exec(ctx,
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
	if err != nil {
		fmt.Println("✗")
		return fmt.Errorf("failed to drop schema: %w", err)
	}
	fmt.Println("✓")

	// Step 2 — Recreate from main
	fmt.Print("  Recreating from main...       ")
	tables, err := schema.Snapshot(ctx, dbManager.Main)
	if err != nil {
		fmt.Println("✗")
		return fmt.Errorf("failed to snapshot main: %w", err)
	}
	if err := schema.Recreate(ctx, dbManager.Branch, schemaName, tables); err != nil {
		fmt.Println("✗")
		return fmt.Errorf("failed to recreate schema: %w", err)
	}
	fmt.Println("✓")

	// Step 3 — Replay remaining migrations
	fmt.Printf("  Replaying %d migration(s)...   ", len(remaining))
	for _, m := range remaining {
		_, _ = dbManager.Branch.Exec(ctx,
			fmt.Sprintf("SET search_path TO %s, public", schemaName))
		_, _ = dbManager.Branch.Exec(ctx, m.SQL)
		_, _ = dbManager.Branch.Exec(ctx, "RESET search_path")
	}
	fmt.Println("✓")

	// Step 4 — Delete undone migration records
	fmt.Print("  Removing migration records... ")
	for _, m := range toUndo {
		_, _ = dbManager.Branch.Exec(ctx,
			"DELETE FROM pgdelta.branch_migrations WHERE id = $1", m.ID)
	}
	fmt.Println("✓")

	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Printf("  Reset complete. %d migration(s) undone.\n", len(toUndo))
	fmt.Printf("  %d migration(s) remain on '%s'.\n", len(remaining), branchName)
	fmt.Println()

	return nil
}
