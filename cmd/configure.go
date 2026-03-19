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
	configureCmd.Flags().Bool("no-ai", false, "Skip AI — use simple SELECT * queries")
	configureCmd.Flags().Int("default-limit", 5000, "Default row limit per table")
	configureCmd.Flags().String("table", "", "Add or update a single table without regenerating all")
}

func runConfigure(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	noAI, _ := cmd.Flags().GetBool("no-ai")
	defaultLimit, _ := cmd.Flags().GetInt("default-limit")
	singleTable, _ := cmd.Flags().GetString("table")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	// Single table mode
	if singleTable != "" {
		return configureSingleTable(ctx, dbManager, cfg, singleTable, defaultLimit, noAI)
	}

	// Full configure mode
	fmt.Println()
	fmt.Println("  pgDelta — configure")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Println("  Scanning your schema and generating snapshot config...")
	fmt.Println()

	// Get all tables from main DB
	tables, tableColumns, rowCounts, err := scanSchema(ctx, dbManager)
	if err != nil {
		return err
	}

	// Filter out excluded tables
	excluded := make(map[string]bool)
	for _, e := range cfg.Snapshots.Exclude {
		excluded[e] = true
	}
	var filteredTables []string
	for _, t := range tables {
		if !excluded[t] {
			filteredTables = append(filteredTables, t)
		}
	}

	if len(filteredTables) == 0 {
		fmt.Println("  No tables found in main DB.")
		return nil
	}

	fmt.Printf("  Found %d tables: %s\n\n",
		len(filteredTables), strings.Join(filteredTables, ", "))

	// Generate queries
	tableConfigs := make(map[string]config.TableSnapshot)
	aiClient := ai.New()

	if aiClient.IsEnabled() && !noAI {
		fmt.Print("  Asking AI to suggest extraction queries...  ")

		// Build filtered maps
		filteredColumns := make(map[string][]string)
		filteredCounts := make(map[string]int64)
		for _, t := range filteredTables {
			filteredColumns[t] = tableColumns[t]
			filteredCounts[t] = rowCounts[t]
		}

		suggestions, err := aiClient.SuggestSnapshotConfig(
			ctx, "development", filteredColumns, filteredCounts,
		)

		if err == nil && len(suggestions) > 0 {
			fmt.Printf("✓\n\n")
			for _, table := range filteredTables {
				query, ok := suggestions[table]
				if !ok || query == "" {
					query = fmt.Sprintf("SELECT * FROM %s", table)
				}
				query = strings.TrimSuffix(strings.TrimSpace(query), ";")
				// Simplify — use SELECT * if AI listed specific columns
				query = simplifyQuery(query, table, rowCounts[table], defaultLimit)
				tableConfigs[table] = config.TableSnapshot{
					Query: query,
					Limit: 0,
				}
				fmt.Printf("  %-20s  %s\n", table, query)
			}
		} else {
			fmt.Printf("✗  (using defaults)\n\n")
			for _, table := range filteredTables {
				tableConfigs[table] = buildDefaultConfig(table, defaultLimit)
				fmt.Printf("  %-20s  SELECT * FROM %s LIMIT %d\n",
					table, table, defaultLimit)
			}
		}
	} else {
		fmt.Println("  Generating default queries...\n")
		for _, table := range filteredTables {
			tableConfigs[table] = buildDefaultConfig(table, defaultLimit)
			fmt.Printf("  %-20s  SELECT * FROM %s LIMIT %d\n",
				table, table, defaultLimit)
		}
	}

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

	return writeSnapshotConfig(cfg, tableConfigs, defaultLimit)
}

func configureSingleTable(
	ctx context.Context,
	dbManager *db.Manager,
	cfg *config.Config,
	tableName string,
	defaultLimit int,
	noAI bool,
) error {
	fmt.Println()
	fmt.Printf("  pgDelta — configure table '%s'\n", tableName)
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()

	// Verify table exists
	var exists bool
	_ = dbManager.Main.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM pg_tables
			WHERE schemaname = 'public' AND tablename = $1
		)
	`, tableName).Scan(&exists)
	if !exists {
		return fmt.Errorf("table '%s' not found in main DB", tableName)
	}

	// Get columns and row count
	columns, _ := getTableColumns(ctx, dbManager, tableName)
	rowCount, _ := estimateRows(ctx, dbManager, tableName)

	var tableConfig config.TableSnapshot
	aiClient := ai.New()

	if aiClient.IsEnabled() && !noAI {
		fmt.Print("  Asking AI for extraction query...  ")

		suggested, err := aiClient.SuggestExtractionQuery(
			ctx, "development", tableName, columns, rowCount,
		)
		if err == nil && suggested != "" {
			suggested = strings.TrimSuffix(strings.TrimSpace(suggested), ";")
			suggested = simplifyQuery(suggested, tableName, rowCount, defaultLimit)
			fmt.Println("✓")
			fmt.Println()
			fmt.Printf("  AI suggests:\n    %s\n\n", suggested)
			fmt.Print("  [A]ccept / [E]dit / [S]kip (use default): ")

			var choice string
			fmt.Scanln(&choice)
			choice = strings.ToLower(strings.TrimSpace(choice))

			switch choice {
			case "a", "accept", "":
				tableConfig = config.TableSnapshot{Query: suggested}
			case "e", "edit":
				fmt.Print("  Query: ")
				var custom string
				fmt.Scanln(&custom)
				if custom == "" {
					custom = suggested
				}
				tableConfig = config.TableSnapshot{Query: custom}
			default:
				tableConfig = buildDefaultConfig(tableName, defaultLimit)
			}
		} else {
			fmt.Println("✗  (using default)")
			tableConfig = buildDefaultConfig(tableName, defaultLimit)
		}
	} else {
		tableConfig = buildDefaultConfig(tableName, defaultLimit)
		fmt.Printf("  Using default: SELECT * FROM %s LIMIT %d\n",
			tableName, defaultLimit)
	}

	// Load existing config and add/update this table
	configData, err := os.ReadFile(config.ConfigFileName)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var rawConfig map[string]interface{}
	if err := yaml.Unmarshal(configData, &rawConfig); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Get existing tables or create new map
	snapshots, _ := rawConfig["snapshots"].(map[string]interface{})
	if snapshots == nil {
		snapshots = make(map[string]interface{})
	}
	tables, _ := snapshots["tables"].(map[string]interface{})
	if tables == nil {
		tables = make(map[string]interface{})
	}

	tables[tableName] = map[string]interface{}{
		"query": tableConfig.Query,
	}
	snapshots["tables"] = tables
	rawConfig["snapshots"] = snapshots

	out, err := yaml.Marshal(rawConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(config.ConfigFileName, out, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Println()
	fmt.Printf("  ✓  '%s' added to .pgdelta.yml\n", tableName)
	fmt.Println()
	fmt.Println("  Tip: run 'pgdelta configure' to regenerate all tables")
	fmt.Println()

	return nil
}

// scanSchema reads all tables, columns and row counts from main DB
func scanSchema(ctx context.Context, dbManager *db.Manager) (
	[]string, map[string][]string, map[string]int64, error,
) {
	rows, err := dbManager.Main.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' ORDER BY tablename
	`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to list tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, nil, nil, err
		}
		tables = append(tables, t)
	}

	tableColumns := make(map[string][]string)
	rowCounts := make(map[string]int64)

	for _, table := range tables {
		cols, _ := getTableColumns(ctx, dbManager, table)
		tableColumns[table] = cols

		var count int64
		_ = dbManager.Main.QueryRow(ctx, `
			SELECT reltuples::BIGINT FROM pg_class
			WHERE relname = $1 AND relnamespace = 'public'::regnamespace
		`, table).Scan(&count)
		rowCounts[table] = count
	}

	return tables, tableColumns, rowCounts, nil
}

// simplifyQuery rewrites verbose AI queries into clean SELECT * form
// when the AI just selects all columns anyway
func simplifyQuery(query, tableName string, rowCount int64, defaultLimit int) string {
	upper := strings.ToUpper(strings.TrimSpace(query))

	// If query has a meaningful WHERE clause keep it but simplify SELECT
	if strings.Contains(upper, "WHERE") {
		// Extract WHERE onwards
		whereIdx := strings.Index(upper, "WHERE")
		wherePart := query[whereIdx:]

		// Remove ORDER BY
		if orderIdx := strings.Index(strings.ToUpper(wherePart), "ORDER BY"); orderIdx != -1 {
			wherePart = wherePart[:orderIdx]
		}

		// Remove existing LIMIT from wherePart
		if limitIdx := strings.Index(strings.ToUpper(wherePart), "LIMIT"); limitIdx != -1 {
			wherePart = wherePart[:limitIdx]
		}

		// Add clean LIMIT
		limitClause := fmt.Sprintf(" LIMIT %d", defaultLimit)

		// Use larger limit if AI suggested one in original query
		if limitIdx := strings.Index(upper, "LIMIT"); limitIdx != -1 {
			limitStr := strings.TrimSpace(query[limitIdx:])
			var aiLimit int
			fmt.Sscanf(limitStr, "LIMIT %d", &aiLimit)
			if aiLimit > 0 {
				limitClause = fmt.Sprintf(" LIMIT %d", aiLimit)
			}
		}

		return fmt.Sprintf("SELECT * FROM %s %s%s",
			tableName, strings.TrimSpace(wherePart), limitClause)
	}

	// No WHERE clause — simple SELECT * with limit
	if rowCount < 1000 {
		return fmt.Sprintf("SELECT * FROM %s", tableName)
	}
	return fmt.Sprintf("SELECT * FROM %s LIMIT %d", tableName, defaultLimit)
}

// buildDefaultConfig creates a simple default snapshot config
func buildDefaultConfig(tableName string, limit int) config.TableSnapshot {
	return config.TableSnapshot{
		Query: fmt.Sprintf("SELECT * FROM %s LIMIT %d", tableName, limit),
	}
}

// writeSnapshotConfig writes the snapshot config cleanly to .pgdelta.yml
func writeSnapshotConfig(cfg *config.Config, tableConfigs map[string]config.TableSnapshot, defaultLimit int) error {
	configData, err := os.ReadFile(config.ConfigFileName)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var rawConfig map[string]interface{}
	if err := yaml.Unmarshal(configData, &rawConfig); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Build clean tables map — query only, no empty limit
	tablesMap := make(map[string]interface{})
	for tableName, tc := range tableConfigs {
		entry := map[string]interface{}{
			"query": tc.Query,
		}
		tablesMap[tableName] = entry
	}

	snapshotsMap := map[string]interface{}{
		"default_row_limit":   defaultLimit,
		"chunk_size":          10000,
		"parallel_tables":     3,
		"resume_on_interrupt": true,
		"tables":              tablesMap,
		"exclude":             cfg.Snapshots.Exclude,
	}

	rawConfig["snapshots"] = snapshotsMap

	out, err := yaml.Marshal(rawConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Add helpful comment at top of snapshots section
	content := string(out)
	content = strings.Replace(content,
		"snapshots:",
		"# Auto-generated by pgdelta configure\n"+
			"# Edit queries per table, or run 'pgdelta configure' to regenerate\n"+
			"# Add a single table: pgdelta configure --table=<name>\n"+
			"snapshots:",
		1,
	)

	if err := os.WriteFile(config.ConfigFileName, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Println()
	fmt.Println("  ✓  .pgdelta.yml updated with clean snapshot queries.")
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Println("  → Commit .pgdelta.yml to share queries with your team")
	fmt.Println("  → Add a new table anytime: pgdelta configure --table=<name>")
	fmt.Println("  → Regenerate all: pgdelta configure")
	fmt.Println()

	return nil
}
