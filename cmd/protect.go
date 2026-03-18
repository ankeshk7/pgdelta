package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var protectCmd = &cobra.Command{
	Use:   "protect",
	Short: "Manage protected branches — prevent accidental merges",
}

var protectAddCmd = &cobra.Command{
	Use:   "add <branch-name>",
	Short: "Protect a branch from being merged into",
	Args:  cobra.ExactArgs(1),
	RunE:  runProtectAdd,
}

var protectRemoveCmd = &cobra.Command{
	Use:   "remove <branch-name>",
	Short: "Remove protection from a branch",
	Args:  cobra.ExactArgs(1),
	RunE:  runProtectRemove,
}

var protectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all protected branches",
	RunE:  runProtectList,
}

func init() {
	rootCmd.AddCommand(protectCmd)
	protectCmd.AddCommand(protectAddCmd)
	protectCmd.AddCommand(protectRemoveCmd)
	protectCmd.AddCommand(protectListCmd)
}

func ensureProtectTable(ctx context.Context, dbManager *db.Manager) error {
	_, err := dbManager.Branch.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS pgdelta.protected_branches (
			id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			branch_name  TEXT UNIQUE NOT NULL,
			reason       TEXT NOT NULL DEFAULT 'protected',
			created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	return err
}

func IsProtected(ctx context.Context, dbManager *db.Manager, branchName string) bool {
	var exists bool
	_ = dbManager.Branch.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM pgdelta.protected_branches
			WHERE branch_name = $1
		)
	`, branchName).Scan(&exists)
	return exists
}

func runProtectAdd(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	branchName := args[0]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	if err := ensureProtectTable(ctx, dbManager); err != nil {
		return fmt.Errorf("failed to create protect table: %w", err)
	}

	_, err = dbManager.Branch.Exec(ctx, `
		INSERT INTO pgdelta.protected_branches (branch_name)
		VALUES ($1) ON CONFLICT (branch_name) DO NOTHING
	`, branchName)
	if err != nil {
		return fmt.Errorf("failed to protect branch: %w", err)
	}

	// Write hook that enforces protection
	hookPath := ".git/hooks/pre-merge-commit"
	hookContent := fmt.Sprintf(`#!/bin/sh
# pgDelta — protect branches from accidental merge
CURRENT=$(git symbolic-ref --short HEAD 2>/dev/null)
if [ "$CURRENT" = "%s" ]; then
  echo ""
  echo "  pgDelta: Branch '%s' is protected."
  echo "  Direct merges to '%s' are not allowed."
  echo "  Use: pgdelta merge <branch> to merge safely."
  echo ""
  exit 1
fi
`, branchName, branchName, branchName)

	if _, err := os.Stat(".git"); err == nil {
		os.WriteFile(hookPath, []byte(hookContent), 0755)
	}

	fmt.Println()
	fmt.Printf("  pgDelta — branch protected\n")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  Branch '%s' is now protected.\n", branchName)
	fmt.Println()
	fmt.Println("  Protection rules:")
	fmt.Println("  → Direct git merge to this branch will be blocked")
	fmt.Println("  → Use 'pgdelta merge <branch>' to merge safely")
	fmt.Println("  → pgdelta merge enforces simulation before applying")
	fmt.Println()

	return nil
}

func runProtectRemove(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	branchName := args[0]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	if err := ensureProtectTable(ctx, dbManager); err != nil {
		return fmt.Errorf("failed to ensure protect table: %w", err)
	}

	result, err := dbManager.Branch.Exec(ctx,
		"DELETE FROM pgdelta.protected_branches WHERE branch_name = $1",
		branchName)
	if err != nil {
		return fmt.Errorf("failed to remove protection: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("branch '%s' is not protected", branchName)
	}

	fmt.Println()
	fmt.Printf("  Protection removed from '%s'\n", branchName)
	fmt.Println()
	return nil
}

func runProtectList(cmd *cobra.Command, args []string) error {
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

	if err := ensureProtectTable(ctx, dbManager); err != nil {
		return fmt.Errorf("failed to ensure protect table: %w", err)
	}

	rows, err := dbManager.Branch.Query(ctx, `
		SELECT branch_name, created_at FROM pgdelta.protected_branches
		ORDER BY created_at DESC
	`)
	if err != nil {
		return fmt.Errorf("failed to list protected branches: %w", err)
	}
	defer rows.Close()

	fmt.Println()
	fmt.Println("  pgDelta — protected branches")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()

	count := 0
	for rows.Next() {
		var name string
		var createdAt time.Time
		rows.Scan(&name, &createdAt)
		fmt.Printf("  🔒  %-30s  protected %s\n",
			name, formatAge(time.Since(createdAt)))
		count++
	}

	if count == 0 {
		fmt.Println("  No protected branches.")
		fmt.Println("  Protect one: pgdelta protect add main")
	}
	fmt.Println()
	return nil
}
