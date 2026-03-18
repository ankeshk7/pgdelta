package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/ai"
	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/ankeshkedia/pgdelta/internal/snapshot"
	"github.com/spf13/cobra"
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot <table-name>",
	Short: "Snapshot a table from main DB into current branch",
	Args:  cobra.ExactArgs(1),
	RunE:  runSnapshot,
}

func init() {
	rootCmd.AddCommand(snapshotCmd)
	snapshotCmd.Flags().String("query", "", "Extraction query (default: AI suggests or SELECT *)")
	snapshotCmd.Flags().String("branch", "", "Branch name (default: current git branch)")
	snapshotCmd.Flags().Bool("no-ai", false, "Skip AI suggestion")
}

func runSnapshot(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	tableName := args[0]
	extractionQuery, _ := cmd.Flags().GetString("query")
	branchName, _ := cmd.Flags().GetString("branch")
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
		return fmt.Errorf("branch '%s' not found — run 'pgdelta create %s'",
			branchName, branchName)
	}

	fmt.Println()
	fmt.Printf("  pgDelta — snapshot '%s'\n", tableName)
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  Table '%s' has no data in branch '%s'.\n", tableName, branchName)
	fmt.Println()

	// If query not provided — try AI suggestion first
	if extractionQuery == "" {
		aiClient := ai.New()

		if aiClient.IsEnabled() && !noAI && cfg.AI.Enabled {
			fmt.Print("  Asking AI for suggestion...  ")

			// Get columns for context
			columns, _ := getTableColumns(ctx, dbManager, tableName)
			rowEst, _ := estimateRows(ctx, dbManager, tableName)

			suggested, err := aiClient.SuggestExtractionQuery(
				ctx, branchName, tableName, columns, rowEst,
			)
			if err == nil && suggested != "" {
				fmt.Println("✓")
				fmt.Println()
				fmt.Println("  AI suggests:")
				fmt.Printf("    %s\n", suggested)
				fmt.Println()
				fmt.Print("  [A]ccept / [E]dit / [S]kip AI (use default): ")

				var choice string
				fmt.Scanln(&choice)
				choice = strings.ToLower(strings.TrimSpace(choice))

				switch choice {
				case "a", "accept", "":
					extractionQuery = suggested
					fmt.Println()
				case "e", "edit":
					fmt.Printf("  Edit query: ")
					fmt.Scanln(&extractionQuery)
					if extractionQuery == "" {
						extractionQuery = suggested
					}
					fmt.Println()
				default:
					// Skip AI — use default
					extractionQuery = ""
				}
			} else {
				fmt.Println("✗  (using default)")
			}
		}

		// Fall back to default if still empty
		if extractionQuery == "" {
			defaultQuery := fmt.Sprintf("SELECT * FROM %s LIMIT 10000", tableName)
			fmt.Printf("  Default query:\n    %s\n\n", defaultQuery)
			fmt.Print("  Use default? [Y/n]: ")

			var confirm string
			fmt.Scanln(&confirm)
			confirm = strings.ToLower(strings.TrimSpace(confirm))

			if confirm == "n" || confirm == "no" {
				fmt.Print("  Extraction query: ")
				fmt.Scanln(&extractionQuery)
				if extractionQuery == "" {
					return fmt.Errorf("extraction query cannot be empty")
				}
			} else {
				extractionQuery = defaultQuery
			}
			fmt.Println()
		}
	}

	// Create snapshot manager
	mgr := snapshot.New(dbManager.Main, dbManager.Branch, cfg.Snapshots.ChunkSize)

	// Register snapshot
	snapshotID, err := mgr.Register(ctx, branchID, tableName, extractionQuery)
	if err != nil {
		return fmt.Errorf("failed to register snapshot: %w", err)
	}

	fmt.Printf("  Loading '%s'...\n\n", tableName)

	progressCh := make(chan snapshot.Progress, 100)

	errCh := make(chan error, 1)
	go func() {
		errCh <- mgr.Load(
			ctx, snapshotID, branchID, schemaName,
			tableName, extractionQuery, progressCh,
		)
		close(progressCh)
	}()

	for p := range progressCh {
		snapshot.PrintProgress(p.TableName, p.RowsLoaded, p.RowsTotal, p.Done)
		if p.Error != nil {
			break
		}
	}

	if err := <-errCh; err != nil {
		return fmt.Errorf("snapshot failed: %w", err)
	}

	// Sync sequences after load
	_, _ = dbManager.Branch.Exec(ctx, fmt.Sprintf(`
		SELECT setval(
			'%s.%s_id_seq',
			COALESCE((SELECT MAX(id) FROM %s.%s), 0) + 1,
			false
		)
	`, schemaName, tableName, schemaName, tableName))

	snap, err := mgr.GetStatus(ctx, branchID, tableName)
	if err != nil {
		return fmt.Errorf("failed to get snapshot status: %w", err)
	}

	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Printf("  Snapshot complete.\n")
	fmt.Printf("  Table:   %s.%s\n", schemaName, tableName)
	fmt.Printf("  Rows:    %d loaded\n", snap.RowsLoaded)
	fmt.Printf("  Query:   %s\n", extractionQuery)
	fmt.Println()

	return nil
}

// getTableColumns returns column names for a table
func getTableColumns(ctx context.Context, dbManager *db.Manager, tableName string) ([]string, error) {
	rows, err := dbManager.Main.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position
	`, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	return cols, rows.Err()
}

// estimateRows returns approximate row count for a table
func estimateRows(ctx context.Context, dbManager *db.Manager, tableName string) (int64, error) {
	var count int64
	err := dbManager.Main.QueryRow(ctx, `
		SELECT reltuples::BIGINT FROM pg_class
		WHERE relname = $1 AND relnamespace = 'public'::regnamespace
	`, tableName).Scan(&count)
	return count, err
}
