package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/ai"
	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/ankeshkedia/pgdelta/internal/migration"
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate <sql>",
	Short: "Apply and record a migration on the current branch",
	Args:  cobra.ExactArgs(1),
	RunE:  runMigrate,
}

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.Flags().String("branch", "", "Branch name (default: current git branch)")
	migrateCmd.Flags().String("type", "", "Migration type: ddl, seed, test (auto-detected if not set)")
	migrateCmd.Flags().String("description", "", "Human readable description")
	migrateCmd.Flags().Bool("no-ai", false, "Skip AI risk analysis")
}

func runMigrate(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sql := args[0]
	branchName, _ := cmd.Flags().GetString("branch")
	migrationType, _ := cmd.Flags().GetString("type")
	description, _ := cmd.Flags().GetString("description")
	noAI, _ := cmd.Flags().GetBool("no-ai")

	if branchName == "" {
		branchName = currentGitBranch()
	}
	if branchName == "" {
		return fmt.Errorf("could not detect git branch — use --branch flag")
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
	var branchID, schemaName string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, schema_name FROM pgdelta.branches
		WHERE name = $1 AND status = 'active'
	`, branchName).Scan(&branchID, &schemaName)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	fmt.Println()
	fmt.Printf("  pgDelta — migrate on '%s'\n", branchName)
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()

	// AI risk analysis before applying
	aiClient := ai.New()
	if aiClient.IsEnabled() && !noAI && cfg.AI.Enabled {
		fmt.Print("  AI risk analysis...    ")

		// Get current schema context
		tableColumns := getSchemaContext(ctx, dbManager, schemaName)

		analysis, isRisky, err := aiClient.AnalyzeMigrationRisk(ctx, sql, tableColumns)
		if err == nil {
			if isRisky {
				fmt.Println("⚠  WARNING")
				fmt.Println()
				fmt.Printf("  %s\n", analysis)
				fmt.Println()
				fmt.Print("  Proceed anyway? [y/N]: ")

				var confirm string
				fmt.Scanln(&confirm)
				if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
					fmt.Println("  Migration aborted.")
					return nil
				}
				fmt.Println()
			} else {
				fmt.Println("✓  safe")
				fmt.Println()
			}
		} else {
			fmt.Println("✗  (skipping)")
			fmt.Println()
		}
	}

	mgr := migration.New(dbManager.Branch)

	fmt.Print("  Applying migration...  ")
	mg, err := mgr.Apply(ctx, branchID, schemaName, sql, description)
	if err != nil {
		fmt.Println("✗")
		return fmt.Errorf("\n  %w", err)
	}
	fmt.Println("✓")

	_ = migrationType

	fmt.Println()
	fmt.Printf("  Sequence:     #%d\n", mg.Sequence)
	fmt.Printf("  Type:         %s\n", mg.Type)
	fmt.Printf("  Description:  %s\n", mg.Description)
	fmt.Printf("  Checksum:     %s\n", mg.Checksum)
	fmt.Println()

	if mg.Type == "ddl" || mg.Type == "seed" {
		fmt.Println("  This migration will travel to parent on merge.")
	} else {
		fmt.Println("  This is test data — will be discarded on merge.")
	}
	fmt.Println()

	return nil
}

// getSchemaContext reads current table/column info for AI context
func getSchemaContext(ctx context.Context, dbManager *db.Manager, schemaName string) map[string][]string {
	result := make(map[string][]string)

	rows, err := dbManager.Branch.Query(ctx, `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = $1
		ORDER BY table_name, ordinal_position
	`, schemaName)
	if err != nil {
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			continue
		}
		result[table] = append(result[table], column)
	}

	return result
}
