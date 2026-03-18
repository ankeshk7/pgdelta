package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/ankeshkedia/pgdelta/internal/schema"
	"github.com/spf13/cobra"
)

var cloneCmd = &cobra.Command{
	Use:   "clone <source-branch> <new-branch>",
	Short: "Clone an existing branch into a new branch",
	Long:  `Creates a new branch as an exact copy of an existing branch — schema, migrations and all.`,
	Args:  cobra.ExactArgs(2),
	RunE:  runClone,
}

func init() {
	rootCmd.AddCommand(cloneCmd)
	cloneCmd.Flags().Bool("with-data", true, "Clone data snapshots too (default: true)")
}

func runClone(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	sourceName := args[0]
	newName := args[1]
	withData, _ := cmd.Flags().GetBool("with-data")

	fmt.Println()
	fmt.Printf("  pgDelta — cloning '%s' → '%s'\n", sourceName, newName)
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	// Check source exists
	var sourceID, sourceSchema, sourceParent string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, schema_name, parent_branch FROM pgdelta.branches
		WHERE name = $1 AND status = 'active'
	`, sourceName).Scan(&sourceID, &sourceSchema, &sourceParent)
	if err != nil {
		return fmt.Errorf("source branch '%s' not found", sourceName)
	}

	// Check new name doesn't exist
	var exists bool
	_ = dbManager.Branch.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pgdelta.branches WHERE name = $1 AND status = 'active')",
		newName,
	).Scan(&exists)
	if exists {
		return fmt.Errorf("branch '%s' already exists", newName)
	}

	newSchema := branchToSchema(newName)

	fmt.Printf("  Source:  %s (%s)\n", sourceName, sourceSchema)
	fmt.Printf("  New:     %s (%s)\n", newName, newSchema)
	fmt.Println()

	// Step 1 — Read schema from main DB
	fmt.Print("  Reading schema from main...   ")
	tables, err := schema.Snapshot(ctx, dbManager.Main)
	if err != nil {
		fmt.Println("✗")
		return fmt.Errorf("failed to read schema: %w", err)
	}
	fmt.Printf("✓  (%d tables)\n", len(tables))

	// Step 2 — Recreate schema on new branch
	fmt.Print("  Creating new branch schema... ")
	if err := schema.Recreate(ctx, dbManager.Branch, newSchema, tables); err != nil {
		fmt.Println("✗")
		return fmt.Errorf("failed to create schema: %w", err)
	}
	fmt.Println("✓")

	// Step 3 — Apply source branch migrations to new schema
	fmt.Print("  Applying source migrations... ")
	migRows, err := dbManager.Branch.Query(ctx, `
		SELECT sql, type FROM pgdelta.branch_migrations
		WHERE branch_id = $1 ORDER BY sequence ASC
	`, sourceID)
	if err != nil {
		fmt.Println("✗")
		return fmt.Errorf("failed to read migrations: %w", err)
	}

	type migration struct {
		SQL  string
		Type string
	}
	var migrations []migration
	for migRows.Next() {
		var m migration
		if err := migRows.Scan(&m.SQL, &m.Type); err != nil {
			continue
		}
		migrations = append(migrations, m)
	}
	migRows.Close()

	for _, m := range migrations {
		_, _ = dbManager.Branch.Exec(ctx,
			fmt.Sprintf("SET search_path TO %s, public", newSchema))
		_, err := dbManager.Branch.Exec(ctx, m.SQL)
		_, _ = dbManager.Branch.Exec(ctx, "RESET search_path")
		if err != nil {
			// Non-fatal — DDL might already exist
			continue
		}
	}
	fmt.Printf("✓  (%d migrations)\n", len(migrations))

	// Step 4 — Register new branch
	fmt.Print("  Registering branch...         ")
	var newBranchID string
	err = dbManager.Branch.QueryRow(ctx, `
		INSERT INTO pgdelta.branches
			(name, git_branch, parent_branch, schema_name, branched_at_seq, status)
		VALUES ($1, $2, $3, $4, $5, 'active')
		RETURNING id
	`, newName, newName, sourceParent, newSchema, len(migrations)).Scan(&newBranchID)
	if err != nil {
		fmt.Println("✗")
		return fmt.Errorf("failed to register branch: %w", err)
	}
	fmt.Println("✓")

	// Step 5 — Copy migration records
	fmt.Print("  Copying migration history...  ")
	for i, m := range migrations {
		checksum := fmt.Sprintf("%x", []byte(m.SQL))[:16]
		_, _ = dbManager.Branch.Exec(ctx, `
			INSERT INTO pgdelta.branch_migrations
				(branch_id, sequence, sql, type, description, checksum)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, newBranchID, i+1, m.SQL, m.Type,
			fmt.Sprintf("cloned from %s", sourceName), checksum)
	}
	fmt.Printf("✓  (%d records)\n", len(migrations))

	// Step 6 — Clone data snapshots if requested
	if withData {
		fmt.Print("  Cloning data snapshots...     ")
		snapRows, err := dbManager.Branch.Query(ctx, `
			SELECT table_name, extraction_sql, rows_loaded
			FROM pgdelta.branch_data_snapshots
			WHERE branch_id = $1 AND status = 'ready'
		`, sourceID)
		if err == nil {
			type snap struct {
				Table string
				SQL   string
				Rows  int64
			}
			var snaps []snap
			for snapRows.Next() {
				var s snap
				if err := snapRows.Scan(&s.Table, &s.SQL, &s.Rows); err != nil {
					continue
				}
				snaps = append(snaps, s)
			}
			snapRows.Close()

			for _, s := range snaps {
				// Copy rows directly between schemas
				_, err := dbManager.Branch.Exec(ctx, fmt.Sprintf(
					"INSERT INTO %s.%s SELECT * FROM %s.%s",
					newSchema, s.Table, sourceSchema, s.Table,
				))
				if err != nil {
					continue
				}

				// Register snapshot
				_, _ = dbManager.Branch.Exec(ctx, `
					INSERT INTO pgdelta.branch_data_snapshots
						(branch_id, table_name, extraction_sql, rows_loaded,
						 row_count, status, completed_at)
					VALUES ($1, $2, $3, $4, $4, 'ready', now())
				`, newBranchID, s.Table, s.SQL, s.Rows)

				// Sync sequence
				_, _ = dbManager.Branch.Exec(ctx, fmt.Sprintf(`
					SELECT setval(
						'%s.%s_id_seq',
						COALESCE((SELECT MAX(id) FROM %s.%s), 0) + 1, false
					)
				`, newSchema, s.Table, newSchema, s.Table))
			}
			fmt.Printf("✓  (%d tables)\n", len(snaps))
		} else {
			fmt.Println("✗  (skipped)")
		}
	}

	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Printf("  Branch '%s' cloned successfully.\n", newName)
	fmt.Println()
	fmt.Printf("  Switch to it:  pgdelta switch %s\n", newName)
	fmt.Printf("  Or:            git checkout -b %s\n", newName)
	fmt.Println()

	return nil
}
