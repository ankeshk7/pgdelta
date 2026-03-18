package cmd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/ai"
	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/ankeshkedia/pgdelta/internal/snapshot"
	"github.com/spf13/cobra"
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot [table-name]",
	Short: "Snapshot a table (or all configured tables) into current branch",
	RunE:  runSnapshot,
}

func init() {
	rootCmd.AddCommand(snapshotCmd)
	snapshotCmd.Flags().String("query", "", "Extraction query (default: AI suggests or SELECT *)")
	snapshotCmd.Flags().String("branch", "", "Branch name (default: current git branch)")
	snapshotCmd.Flags().Bool("no-ai", false, "Skip AI suggestion")
	snapshotCmd.Flags().Bool("all", false, "Snapshot all pre-configured tables in parallel")
}

func runSnapshot(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	allTables, _ := cmd.Flags().GetBool("all")
	branchName, _ := cmd.Flags().GetString("branch")

	if branchName == "" {
		branchName = currentGitBranch()
	}
	if branchName == "" {
		return fmt.Errorf("could not detect git branch — use --branch flag")
	}

	// Require table name unless --all
	if !allTables && len(args) == 0 {
		return fmt.Errorf("specify a table name or use --all to snapshot all configured tables")
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

	if allTables {
		return runSnapshotAll(ctx, cmd, cfg, dbManager, branchID, schemaName, branchName)
	}

	return runSnapshotOne(ctx, cmd, cfg, dbManager, branchID, schemaName, branchName, args[0])
}

// runSnapshotAll loads all pre-configured tables in parallel
func runSnapshotAll(
	ctx context.Context,
	cmd *cobra.Command,
	cfg *config.Config,
	dbManager *db.Manager,
	branchID, schemaName, branchName string,
) error {
	if len(cfg.Snapshots.Tables) == 0 {
		fmt.Println()
		fmt.Println("  No tables configured in .pgdelta.yml snapshots.tables")
		fmt.Println("  Add table configs or use: pgdelta snapshot <table>")
		fmt.Println()
		return nil
	}

	fmt.Println()
	fmt.Printf("  pgDelta — snapshot all tables on '%s'\n", branchName)
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  Loading %d table(s) in parallel...\n\n", len(cfg.Snapshots.Tables))

	mgr := snapshot.New(dbManager.Main, dbManager.Branch, cfg.Snapshots.ChunkSize)

	type result struct {
		table   string
		rows    int64
		err     error
		skipped bool
	}

	results := make(chan result, len(cfg.Snapshots.Tables))
	var wg sync.WaitGroup

	// Launch parallel workers — one per table
	for tableName, tableConfig := range cfg.Snapshots.Tables {
		wg.Add(1)
		go func(table string, tcfg config.TableSnapshot) {
			defer wg.Done()

			// Skip if already snapshotted and ready
			exists, _ := mgr.Exists(ctx, branchID, table)
			if exists {
				snap, _ := mgr.GetStatus(ctx, branchID, table)
				if snap != nil && snap.Status == "ready" && snap.RowsLoaded > 0 {
					results <- result{table: table, rows: snap.RowsLoaded, skipped: true}
					return
				}
			}

			// Truncate branch table before loading to avoid PK conflicts on retry
			_, _ = dbManager.Branch.Exec(ctx,
				fmt.Sprintf("TRUNCATE TABLE %s.%s RESTART IDENTITY CASCADE",
					schemaName, table))

			extractionSQL := tcfg.Query
			if extractionSQL == "" {
				extractionSQL = fmt.Sprintf("SELECT * FROM %s", table)
			}
			if tcfg.Limit > 0 && !strings.Contains(
				strings.ToUpper(extractionSQL), "LIMIT") {
				extractionSQL = fmt.Sprintf("%s LIMIT %d", extractionSQL, tcfg.Limit)
			}

			snapshotID, err := mgr.Register(ctx, branchID, table, extractionSQL)
			if err != nil {
				results <- result{table: table, err: err}
				return
			}

			err = mgr.Load(ctx, snapshotID, branchID, schemaName, table, extractionSQL, nil)
			if err != nil {
				results <- result{table: table, err: err}
				return
			}

			snap, _ := mgr.GetStatus(ctx, branchID, table)
			var rows int64
			if snap != nil {
				rows = snap.RowsLoaded
			}

			// Sync sequences after load
			_, _ = dbManager.Branch.Exec(ctx, fmt.Sprintf(`
				SELECT setval(
					'%s.%s_id_seq',
					COALESCE((SELECT MAX(id) FROM %s.%s), 0) + 1,
					false
				)
			`, schemaName, table, schemaName, table))

			results <- result{table: table, rows: rows}
		}(tableName, tableConfig)
	}

	// Wait for all then close channel
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var totalRows int64
	var failed []string

	for r := range results {
		if r.err != nil {
			fmt.Printf("  ✗  %-20s  error: %v\n", r.table, r.err)
			failed = append(failed, r.table)
		} else if r.skipped {
			fmt.Printf("  ◌  %-20s  already loaded (%d rows)\n", r.table, r.rows)
			totalRows += r.rows
		} else {
			fmt.Printf("  ✓  %-20s  %d rows\n", r.table, r.rows)
			totalRows += r.rows
		}
	}

	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────")
	if len(failed) > 0 {
		fmt.Printf("  %d table(s) failed: %s\n",
			len(failed), strings.Join(failed, ", "))
	} else {
		fmt.Printf("  All tables loaded. %d total rows.\n", totalRows)
	}
	fmt.Println()

	return nil
}

// runSnapshotOne loads a single table with AI suggestion
func runSnapshotOne(
	ctx context.Context,
	cmd *cobra.Command,
	cfg *config.Config,
	dbManager *db.Manager,
	branchID, schemaName, branchName, tableName string,
) error {
	extractionQuery, _ := cmd.Flags().GetString("query")
	noAI, _ := cmd.Flags().GetBool("no-ai")

	fmt.Println()
	fmt.Printf("  pgDelta — snapshot '%s'\n", tableName)
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  Table '%s' has no data in branch '%s'.\n", tableName, branchName)
	fmt.Println()

	// Check pre-defined config first
	if extractionQuery == "" {
		if tableConfig, ok := cfg.Snapshots.Tables[tableName]; ok && tableConfig.Query != "" {
			extractionQuery = tableConfig.Query
			if tableConfig.Limit > 0 {
				extractionQuery = fmt.Sprintf("%s LIMIT %d",
					extractionQuery, tableConfig.Limit)
			}
			fmt.Printf("  Using config query:\n    %s\n\n", extractionQuery)
		}
	}

	// Try AI suggestion if no config and not skipped
	if extractionQuery == "" {
		aiClient := ai.New()
		if aiClient.IsEnabled() && !noAI && cfg.AI.Enabled {
			fmt.Print("  Asking AI for suggestion...  ")
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
				fmt.Print("  [A]ccept / [E]dit / [S]kip (use default): ")

				var choice string
				fmt.Scanln(&choice)
				choice = strings.ToLower(strings.TrimSpace(choice))

				switch choice {
				case "a", "accept", "":
					extractionQuery = suggested
					fmt.Println()
				case "e", "edit":
					fmt.Print("  Edit query: ")
					fmt.Scanln(&extractionQuery)
					if extractionQuery == "" {
						extractionQuery = suggested
					}
					fmt.Println()
				}
			} else {
				fmt.Println("✗  (using default)")
			}
		}
	}

	// Fall back to default
	if extractionQuery == "" {
		limit := cfg.Snapshots.DefaultRowLimit
		if limit <= 0 {
			limit = 10000
		}
		defaultQuery := fmt.Sprintf("SELECT * FROM %s LIMIT %d", tableName, limit)
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

	mgr := snapshot.New(dbManager.Main, dbManager.Branch, cfg.Snapshots.ChunkSize)

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

	// Sync sequences
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

func estimateRows(ctx context.Context, dbManager *db.Manager, tableName string) (int64, error) {
	var count int64
	err := dbManager.Main.QueryRow(ctx, `
		SELECT reltuples::BIGINT FROM pg_class
		WHERE relname = $1 AND relnamespace = 'public'::regnamespace
	`, tableName).Scan(&count)
	return count, err
}
