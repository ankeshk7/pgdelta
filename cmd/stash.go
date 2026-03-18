package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var stashCmd = &cobra.Command{
	Use:   "stash",
	Short: "Stash or restore branch migration state",
}

var stashSaveCmd = &cobra.Command{
	Use:   "save [message]",
	Short: "Stash current branch migration state",
	RunE:  runStashSave,
}

var stashPopCmd = &cobra.Command{
	Use:   "pop",
	Short: "Restore most recent stash",
	RunE:  runStashPop,
}

var stashListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all stashes",
	RunE:  runStashList,
}

var stashDropCmd = &cobra.Command{
	Use:   "drop [stash-id]",
	Short: "Drop a stash",
	RunE:  runStashDrop,
}

func init() {
	rootCmd.AddCommand(stashCmd)
	stashCmd.AddCommand(stashSaveCmd)
	stashCmd.AddCommand(stashPopCmd)
	stashCmd.AddCommand(stashListCmd)
	stashCmd.AddCommand(stashDropCmd)
	stashSaveCmd.Flags().String("branch", "", "Branch to stash (default: current git branch)")
}

type stashEntry struct {
	ID          string    `json:"id"`
	BranchName  string    `json:"branch_name"`
	Message     string    `json:"message"`
	MigCount    int       `json:"migration_count"`
	Migrations  []stashMig `json:"migrations"`
	CreatedAt   time.Time `json:"created_at"`
}

type stashMig struct {
	Sequence int    `json:"sequence"`
	SQL      string `json:"sql"`
	Type     string `json:"type"`
	Desc     string `json:"description"`
	Checksum string `json:"checksum"`
}

func ensureStashTable(ctx context.Context, dbManager *db.Manager) error {
	_, err := dbManager.Branch.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS pgdelta.stashes (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			branch_name TEXT NOT NULL,
			message     TEXT NOT NULL DEFAULT '',
			payload     JSONB NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	return err
}

func runStashSave(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	branchName, _ := cmd.Flags().GetString("branch")
	if branchName == "" {
		branchName = currentGitBranch()
	}
	if branchName == "" {
		return fmt.Errorf("could not detect branch — use --branch flag")
	}

	message := ""
	if len(args) > 0 {
		message = args[0]
	} else {
		message = fmt.Sprintf("WIP on %s", branchName)
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

	if err := ensureStashTable(ctx, dbManager); err != nil {
		return fmt.Errorf("failed to create stash table: %w", err)
	}

	// Get branch ID
	var branchID string
	err = dbManager.Branch.QueryRow(ctx,
		"SELECT id FROM pgdelta.branches WHERE name = $1 AND status = 'active'",
		branchName,
	).Scan(&branchID)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	// Read all migrations
	rows, err := dbManager.Branch.Query(ctx, `
		SELECT sequence, sql, type, COALESCE(description,''), checksum
		FROM pgdelta.branch_migrations
		WHERE branch_id = $1 ORDER BY sequence ASC
	`, branchID)
	if err != nil {
		return fmt.Errorf("failed to read migrations: %w", err)
	}

	var migs []stashMig
	for rows.Next() {
		var m stashMig
		rows.Scan(&m.Sequence, &m.SQL, &m.Type, &m.Desc, &m.Checksum)
		migs = append(migs, m)
	}
	rows.Close()

	if len(migs) == 0 {
		fmt.Println()
		fmt.Println("  Nothing to stash — no migrations on this branch.")
		fmt.Println()
		return nil
	}

	// Save stash
	entry := stashEntry{
		BranchName: branchName,
		Message:    message,
		MigCount:   len(migs),
		Migrations: migs,
	}

	payload, _ := json.Marshal(entry)

	var stashID string
	err = dbManager.Branch.QueryRow(ctx, `
		INSERT INTO pgdelta.stashes (branch_name, message, payload)
		VALUES ($1, $2, $3) RETURNING id
	`, branchName, message, payload).Scan(&stashID)
	if err != nil {
		return fmt.Errorf("failed to save stash: %w", err)
	}

	fmt.Println()
	fmt.Printf("  pgDelta — stash saved\n")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  Stash ID:  %s\n", stashID[:8])
	fmt.Printf("  Branch:    %s\n", branchName)
	fmt.Printf("  Message:   %s\n", message)
	fmt.Printf("  Saved:     %d migration(s)\n", len(migs))
	fmt.Println()
	fmt.Println("  Restore with: pgdelta stash pop")
	fmt.Println()

	return nil
}

func runStashPop(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	branchName := currentGitBranch()
	if branchName == "" {
		return fmt.Errorf("could not detect current branch")
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

	if err := ensureStashTable(ctx, dbManager); err != nil {
		return fmt.Errorf("failed to ensure stash table: %w", err)
	}

	// Get most recent stash for this branch
	var stashID string
	var payload []byte
	var message string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, payload, message FROM pgdelta.stashes
		WHERE branch_name = $1
		ORDER BY created_at DESC LIMIT 1
	`, branchName).Scan(&stashID, &payload, &message)
	if err != nil {
		return fmt.Errorf("no stash found for branch '%s'", branchName)
	}

	var entry stashEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return fmt.Errorf("failed to parse stash: %w", err)
	}

	// Get branch ID
	var branchID, schemaName string
	err = dbManager.Branch.QueryRow(ctx,
		"SELECT id, schema_name FROM pgdelta.branches WHERE name = $1 AND status = 'active'",
		branchName,
	).Scan(&branchID, &schemaName)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	fmt.Println()
	fmt.Printf("  pgDelta — stash pop\n")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Printf("  Restoring: %s\n", message)
	fmt.Printf("  Migrations: %d\n\n", len(entry.Migrations))

	// Apply each migration from stash
	applied := 0
	for _, m := range entry.Migrations {
		_, _ = dbManager.Branch.Exec(ctx,
			fmt.Sprintf("SET search_path TO %s, public", schemaName))
		_, err := dbManager.Branch.Exec(ctx, m.SQL)
		_, _ = dbManager.Branch.Exec(ctx, "RESET search_path")
		if err != nil {
			fmt.Printf("  ⚠  #%d failed (may already exist): %v\n", m.Sequence, err)
			continue
		}

		// Record migration
		_, _ = dbManager.Branch.Exec(ctx, `
			INSERT INTO pgdelta.branch_migrations
				(branch_id, sequence, sql, type, description, checksum)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (branch_id, sequence) DO NOTHING
		`, branchID, m.Sequence, m.SQL, m.Type, m.Desc, m.Checksum)
		applied++
	}

	// Delete the stash
	_, _ = dbManager.Branch.Exec(ctx,
		"DELETE FROM pgdelta.stashes WHERE id = $1", stashID)

	fmt.Printf("  ✓  %d migration(s) restored\n", applied)
	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println("  Stash popped successfully.")
	fmt.Println()

	return nil
}

func runStashList(cmd *cobra.Command, args []string) error {
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

	if err := ensureStashTable(ctx, dbManager); err != nil {
		return fmt.Errorf("failed to ensure stash table: %w", err)
	}

	rows, err := dbManager.Branch.Query(ctx, `
		SELECT id, branch_name, message, created_at
		FROM pgdelta.stashes
		ORDER BY created_at DESC
	`)
	if err != nil {
		return fmt.Errorf("failed to list stashes: %w", err)
	}
	defer rows.Close()

	fmt.Println()
	fmt.Println("  pgDelta — stashes")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()

	count := 0
	for rows.Next() {
		var id, branch, message string
		var createdAt time.Time
		rows.Scan(&id, &branch, &message, &createdAt)
		fmt.Printf("  stash@{%d}  %s  %s\n", count, id[:8], message)
		fmt.Printf("             branch: %s · %s\n\n",
			branch, formatAge(time.Since(createdAt)))
		count++
	}

	if count == 0 {
		fmt.Println("  No stashes found.")
	}
	fmt.Println()
	return nil
}

func runStashDrop(cmd *cobra.Command, args []string) error {
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

	if err := ensureStashTable(ctx, dbManager); err != nil {
		return fmt.Errorf("failed to ensure stash table: %w", err)
	}

	branchName := currentGitBranch()

	var result string
	if len(args) > 0 {
		// Drop by ID prefix
		err = dbManager.Branch.QueryRow(ctx, `
			DELETE FROM pgdelta.stashes
			WHERE id::text LIKE $1 RETURNING id
		`, args[0]+"%").Scan(&result)
	} else {
		// Drop most recent for current branch
		err = dbManager.Branch.QueryRow(ctx, `
			DELETE FROM pgdelta.stashes
			WHERE id = (
				SELECT id FROM pgdelta.stashes
				WHERE branch_name = $1
				ORDER BY created_at DESC LIMIT 1
			) RETURNING id
		`, branchName).Scan(&result)
	}

	if err != nil {
		return fmt.Errorf("no stash found to drop")
	}

	fmt.Println()
	fmt.Printf("  Dropped stash %s\n", result[:8])
	fmt.Println()
	return nil
}
