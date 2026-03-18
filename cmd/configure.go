package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/ai"
	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var configureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Auto-generate snapshot config from your schema using AI",
	RunE:  runConfigure,
}

func init() {
	rootCmd.AddCommand(configureCmd)
	configureCmd.Flags().Bool("no-ai", false, "Skip AI — use simple LIMIT-based queries")
	configureCmd.Flags().Int("default-limit", 5000, "Default row limit per table")
}

func runConfigure(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	noAI, _ := cmd.Flags().GetBool("no-ai")
	defaultLimit, _ := cmd.Flags().GetInt("default-limit")

	fmt.Println()
	fmt.Println("  pgDelta — configure")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Println("  Scanning your schema and generating snapshot config...")
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

	// Get all tables from main DB
	rows, err := dbManager.Main.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public'
		ORDER BY tablename
	`)
	if err != nil {
		return fmt.Errorf("failed to list tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return err
		}
		tables = append(tables, t)
	}

	if len(tables) == 0 {
		fmt.Println("  No tables found in main DB.")
		return nil
	}

	fmt.Printf("  Found %d tables: %s\n\n", len(tables),
		strings.Join(tables, ", "))

	// Get columns and row counts for each table
	tableColumns := make(map[string][]string)
	rowCounts := make(map[string]int64)

	for _, table := range tables {
		// Get columns
		colRows, err := dbManager.Main.Query(ctx, `
			SELECT column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1
			ORDER BY ordinal_position
		`, table)
		if err != nil {
			continue
		}

		var cols []string
		for colRows.Next() {
			var col string
			if err := colRows.Scan(&col); err != nil {
				continue
			}
			cols = append(cols, col)
		}
		colRows.Close()
		tableColumns[table] = cols

		// Get row estimate
		var count int64
		_ = dbManager.Main.QueryRow(ctx, `
			SELECT reltuples::BIGINT FROM pg_class
			WHERE relname = $1 AND relnamespace = 'public'::regnamespace
		`, table).Scan(&count)
		rowCounts[table] = count
	}

	// Generate queries
	tableConfigs := make(map[string]config.TableSnapshot)

	aiClient := ai.New()

	if aiClient.IsEnabled() && !noAI {
		fmt.Print("  Asking AI to suggest extraction queries...  ")

		suggestions, err := aiClient.SuggestSnapshotConfig(
			ctx,
			"development",
			tableColumns,
			rowCounts,
		)

		if err == nil && len(suggestions) > 0 {
			fmt.Printf("✓\n\n")

			for _, table := range tables {
				query, ok := suggestions[table]
				if !ok || query == "" {
					query = fmt.Sprintf(
						"SELECT * FROM %s LIMIT %d", table, defaultLimit)
				}
				// Remove trailing semicolon
				query = strings.TrimSuffix(strings.TrimSpace(query), ";")

				tableConfigs[table] = config.TableSnapshot{
					Query: query,
					Limit: 0, // limit already in query from AI
				}

				fmt.Printf("  %-20s  %s\n", table, truncateSQL(query, 60))
			}
		} else {
			fmt.Printf("✗  (using defaults)\n\n")
			for _, table := range tables {
				tableConfigs[table] = config.TableSnapshot{
					Query: fmt.Sprintf("SELECT * FROM %s", table),
					Limit: defaultLimit,
				}
				fmt.Printf("  %-20s  SELECT * FROM %s LIMIT %d\n",
					table, table, defaultLimit)
			}
		}
	} else {
		fmt.Println("  Generating default queries...\n")
		for _, table := range tables {
			tableConfigs[table] = config.TableSnapshot{
				Query: fmt.Sprintf("SELECT * FROM %s", table),
				Limit: defaultLimit,
			}
			fmt.Printf("  %-20s  SELECT * FROM %s LIMIT %d\n",
				table, table, defaultLimit)
		}
	}

	// Show summary and confirm
	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Print("  Write these queries to .pgdelta.yml? [Y/n]: ")

	var confirm string
	fmt.Scanln(&confirm)
	confirm = strings.ToLower(strings.TrimSpace(confirm))

	if confirm == "n" || confirm == "no" {
		fmt.Println("  Cancelled — no changes made.")
		return nil
	}

	// Read existing config file
	configData, err := os.ReadFile(config.ConfigFileName)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// Parse as generic map to preserve comments + structure
	var rawConfig map[string]interface{}
	if err := yaml.Unmarshal(configData, &rawConfig); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Build snapshots section
	tablesMap := make(map[string]interface{})
	for tableName, tc := range tableConfigs {
		tablesMap[tableName] = map[string]interface{}{
			"query": tc.Query,
			"limit": tc.Limit,
		}
	}

	snapshotsMap := map[string]interface{}{
		"default_row_limit":   defaultLimit,
		"chunk_size":          10000,
		"parallel_tables":     3,
		"resume_on_interrupt": true,
		"tables":              tablesMap,
		"exclude":             []string{},
	}

	rawConfig["snapshots"] = snapshotsMap

	// Write back
	out, err := yaml.Marshal(rawConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(config.ConfigFileName, out, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Println()
	fmt.Println("  ✓  .pgdelta.yml updated with snapshot queries.")
	fmt.Println()
	fmt.Println("  Next time you create a branch:")
	fmt.Println("  → all tables will auto-snapshot with these queries")
	fmt.Println("  → no prompts, no manual configuration")
	fmt.Println()
	fmt.Println("  To regenerate: pgdelta configure")
	fmt.Println("  To customize: edit snapshots.tables in .pgdelta.yml")
	fmt.Println()

	return nil
}
