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

var createCmd = &cobra.Command{
	Use:   "create <branch-name>",
	Short: "Create a new database branch",
	Long:  `Creates a new database branch by copying the schema from the parent branch.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runCreate,
}

func init() {
	rootCmd.AddCommand(createCmd)
	createCmd.Flags().String("from", "", "Parent branch to copy schema from (default: current git branch)")
	createCmd.Flags().Bool("auto", false, "Called automatically from git hook — suppress prompts")
}

func runCreate(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	branchName := args[0]
	auto, _ := cmd.Flags().GetBool("auto")
	schemaName := branchToSchema(branchName)

	if !auto {
		fmt.Println()
		fmt.Printf("  pgDelta — creating branch '%s'\n", branchName)
		fmt.Println("  ─────────────────────────────────────────")
		fmt.Println()
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect to databases: %w", err)
	}
	defer dbManager.Close()

	// Check branch doesn't already exist
	var exists bool
	err = dbManager.Branch.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pgdelta.branches WHERE name = $1 AND status = 'active')",
		branchName,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check branch existence: %w", err)
	}
	if exists {
		if auto {
			return nil
		}
		return fmt.Errorf("branch '%s' already exists — use 'pgdelta switch %s'", branchName, branchName)
	}

	// Determine parent branch
	parentBranch, _ := cmd.Flags().GetString("from")
	if parentBranch == "" {
		parentBranch = detectGitParent(branchName)
	}
	if parentBranch == "" {
		parentBranch = "main"
	}

	if !auto {
		fmt.Printf("  Parent branch:  %s\n", parentBranch)
		fmt.Printf("  Schema name:    %s\n", schemaName)
		fmt.Println()
	}

	// Step 1 — Read schema from main DB
	if !auto {
		fmt.Print("  Reading schema from main...  ")
	}
	tables, err := schema.Snapshot(ctx, dbManager.Main)
	if err != nil {
		if !auto {
			fmt.Println("✗")
		}
		return fmt.Errorf("failed to read schema: %w", err)
	}
	if !auto {
		fmt.Printf("✓  (%d tables)\n", len(tables))
	}

	// Step 2 — Recreate schema on branch DB
	if !auto {
		fmt.Print("  Creating branch schema...    ")
	}
	if err := schema.Recreate(ctx, dbManager.Branch, schemaName, tables); err != nil {
		if !auto {
			fmt.Println("✗")
		}
		return fmt.Errorf("failed to create branch schema: %w", err)
	}
	if !auto {
		fmt.Println("✓")
	}

	// Step 3 — Register branch in metadata
	if !auto {
		fmt.Print("  Registering branch...        ")
	}
	_, err = dbManager.Branch.Exec(ctx, `
		INSERT INTO pgdelta.branches
			(name, git_branch, parent_branch, schema_name, branched_at_seq, status)
		VALUES
			($1, $2, $3, $4, 0, 'active')
	`, branchName, branchName, parentBranch, schemaName)
	if err != nil {
		if !auto {
			fmt.Println("✗")
		}
		return fmt.Errorf("failed to register branch: %w", err)
	}
	if !auto {
		fmt.Println("✓")
	}

	// Step 4 — Sync sequences to prevent PK conflicts after snapshotting
	_, _ = dbManager.Branch.Exec(ctx, fmt.Sprintf(`
		DO $$
		DECLARE
			r RECORD;
		BEGIN
			FOR r IN
				SELECT sequence_name
				FROM information_schema.sequences
				WHERE sequence_schema = '%s'
			LOOP
				EXECUTE format(
					'SELECT setval(''%s.%%s'', COALESCE((SELECT MAX(id) FROM %s.%%s), 0) + 1, false)',
					r.sequence_name, r.sequence_name
				);
			END LOOP;
		END $$;
	`, schemaName, schemaName, schemaName))

	// Success
	if !auto {
		fmt.Println()
		fmt.Println("  ─────────────────────────────────────────")
		fmt.Printf("  Branch '%s' created.\n", branchName)
		fmt.Println()
		fmt.Println("  Tables available (schema only, no data yet):")
		for _, t := range tables {
			fmt.Printf("    → %-20s  (%d columns)\n", t.Name, len(t.Columns))
		}
		fmt.Println()
		fmt.Println("  Data loads lazily when you first query each table.")
		fmt.Printf("  Connection:  set DATABASE_URL to point to schema '%s'\n", schemaName)
		fmt.Println()
	}

	return nil
}

func branchToSchema(branch string) string {
	safe := strings.NewReplacer(
		"/", "_",
		"-", "_",
		".", "_",
		" ", "_",
	).Replace(branch)
	return "branch_" + strings.ToLower(safe)
}

func detectGitParent(currentBranch string) string {
	if currentBranch == "develop" {
		return "main"
	}
	return "main"
}
