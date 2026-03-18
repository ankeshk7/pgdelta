package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var tagCmd = &cobra.Command{
	Use:   "tag",
	Short: "Mark named checkpoints on a branch",
}

var tagCreateCmd = &cobra.Command{
	Use:   "create <tag-name> [message]",
	Short: "Create a tag at current migration state",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runTagCreate,
}

var tagListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all tags",
	RunE:  runTagList,
}

var tagDeleteCmd = &cobra.Command{
	Use:   "delete <tag-name>",
	Short: "Delete a tag",
	Args:  cobra.ExactArgs(1),
	RunE:  runTagDelete,
}

func init() {
	rootCmd.AddCommand(tagCmd)
	tagCmd.AddCommand(tagCreateCmd)
	tagCmd.AddCommand(tagListCmd)
	tagCmd.AddCommand(tagDeleteCmd)
	tagCreateCmd.Flags().String("branch", "", "Branch to tag (default: current git branch)")
}

func ensureTagTable(ctx context.Context, dbManager *db.Manager) error {
	_, err := dbManager.Branch.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS pgdelta.branch_tags (
			id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name          TEXT NOT NULL,
			branch_name   TEXT NOT NULL,
			branch_id     UUID REFERENCES pgdelta.branches(id),
			migration_seq INT NOT NULL DEFAULT 0,
			message       TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE(name, branch_name)
		)
	`)
	return err
}

func runTagCreate(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tagName := args[0]
	message := ""
	if len(args) > 1 {
		message = args[1]
	}

	branchName, _ := cmd.Flags().GetString("branch")
	if branchName == "" {
		branchName = currentGitBranch()
	}
	if branchName == "" {
		return fmt.Errorf("could not detect branch — use --branch flag")
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

	if err := ensureTagTable(ctx, dbManager); err != nil {
		return fmt.Errorf("failed to create tag table: %w", err)
	}

	// Get branch + current migration seq
	var branchID string
	var migSeq int
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT b.id, COALESCE(MAX(m.sequence), 0)
		FROM pgdelta.branches b
		LEFT JOIN pgdelta.branch_migrations m ON m.branch_id = b.id
		WHERE b.name = $1 AND b.status = 'active'
		GROUP BY b.id
	`, branchName).Scan(&branchID, &migSeq)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	// Create tag
	var tagID string
	err = dbManager.Branch.QueryRow(ctx, `
		INSERT INTO pgdelta.branch_tags
			(name, branch_name, branch_id, migration_seq, message)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, tagName, branchName, branchID, migSeq, message).Scan(&tagID)
	if err != nil {
		return fmt.Errorf("failed to create tag (may already exist): %w", err)
	}

	fmt.Println()
	fmt.Printf("  pgDelta — tag created\n")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  Tag:     %s\n", tagName)
	fmt.Printf("  Branch:  %s\n", branchName)
	fmt.Printf("  At seq:  #%d\n", migSeq)
	if message != "" {
		fmt.Printf("  Message: %s\n", message)
	}
	fmt.Println()

	return nil
}

func runTagList(cmd *cobra.Command, args []string) error {
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

	if err := ensureTagTable(ctx, dbManager); err != nil {
		return fmt.Errorf("failed to ensure tag table: %w", err)
	}

	rows, err := dbManager.Branch.Query(ctx, `
		SELECT name, branch_name, migration_seq, message, created_at
		FROM pgdelta.branch_tags
		ORDER BY created_at DESC
	`)
	if err != nil {
		return fmt.Errorf("failed to list tags: %w", err)
	}
	defer rows.Close()

	fmt.Println()
	fmt.Println("  pgDelta — tags")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Printf("  %-20s  %-25s  %-6s  %s\n", "TAG", "BRANCH", "SEQ", "MESSAGE")
	fmt.Println("  ─────────────────────────────────────────")

	count := 0
	for rows.Next() {
		var name, branch, message string
		var seq int
		var createdAt time.Time
		rows.Scan(&name, &branch, &seq, &message, &createdAt)
		fmt.Printf("  %-20s  %-25s  #%-5d  %s\n", name, branch, seq, message)
		count++
	}

	if count == 0 {
		fmt.Println("  No tags found.")
		fmt.Println("  Create one: pgdelta tag create <name>")
	}
	fmt.Println()
	return nil
}

func runTagDelete(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tagName := args[0]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	if err := ensureTagTable(ctx, dbManager); err != nil {
		return fmt.Errorf("failed to ensure tag table: %w", err)
	}

	result, err := dbManager.Branch.Exec(ctx,
		"DELETE FROM pgdelta.branch_tags WHERE name = $1", tagName)
	if err != nil {
		return fmt.Errorf("failed to delete tag: %w", err)
	}

	rows := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("tag '%s' not found", tagName)
	}

	fmt.Println()
	fmt.Printf("  Deleted tag '%s'\n", tagName)
	fmt.Println()
	return nil
}
