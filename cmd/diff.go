package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var diffCmd = &cobra.Command{
	Use:   "diff <branch1> [branch2]",
	Short: "Show schema diff between two branches",
	Long:  `Compares schema of two branches. If branch2 omitted, compares branch1 against its parent.`,
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runDiff,
}

func init() {
	rootCmd.AddCommand(diffCmd)
	diffCmd.Flags().Bool("migrations", false, "Show migration diff instead of schema diff")
}

type columnDef struct {
	Name     string
	DataType string
	Nullable bool
	Default  string
}

type tableDef struct {
	Name    string
	Columns []columnDef
}

func runDiff(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	showMigrations, _ := cmd.Flags().GetBool("migrations")
	branch1 := args[0]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	// Get branch1 info
	var schema1, parent1, branchID1 string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, schema_name, parent_branch FROM pgdelta.branches
		WHERE name = $1 AND status != 'deleted'
	`, branch1).Scan(&branchID1, &schema1, &parent1)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branch1)
	}

	// Determine branch2
	branch2 := parent1
	if len(args) > 1 {
		branch2 = args[1]
	}

	var schema2, branchID2 string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT id, schema_name FROM pgdelta.branches
		WHERE name = $1 AND status != 'deleted'
	`, branch2).Scan(&branchID2, &schema2)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branch2)
	}

	fmt.Println()
	fmt.Printf("  pgDelta — diff\n")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Printf("  Comparing:  %s → %s\n", branch2, branch1)
	fmt.Println()

	if showMigrations {
		return diffMigrations(ctx, dbManager, branchID1, branchID2, branch1, branch2)
	}

	return diffSchemas(ctx, dbManager, schema1, schema2, branch1, branch2)
}

func diffSchemas(ctx context.Context, dbManager *db.Manager, schema1, schema2, name1, name2 string) error {
	// Get tables + columns for both schemas
	tables1 := getSchemaSnapshot(ctx, dbManager, schema1)
	tables2 := getSchemaSnapshot(ctx, dbManager, schema2)

	added := 0
	removed := 0
	changed := 0

	// Find added and changed tables/columns
	for tableName, t1 := range tables1 {
		t2, exists := tables2[tableName]
		if !exists {
			fmt.Printf("  + TABLE %s (added in %s)\n", tableName, name1)
			added++
			continue
		}

		// Compare columns
		cols1 := make(map[string]columnDef)
		for _, c := range t1.Columns {
			cols1[c.Name] = c
		}
		cols2 := make(map[string]columnDef)
		for _, c := range t2.Columns {
			cols2[c.Name] = c
		}

		tableChanged := false
		for colName, c1 := range cols1 {
			c2, exists := cols2[colName]
			if !exists {
				if !tableChanged {
					fmt.Printf("\n  ~ TABLE %s\n", tableName)
					tableChanged = true
					changed++
				}
				fmt.Printf("    + COLUMN %s %s (added)\n", c1.Name, strings.ToUpper(c1.DataType))
				added++
			} else if c1.DataType != c2.DataType {
				if !tableChanged {
					fmt.Printf("\n  ~ TABLE %s\n", tableName)
					tableChanged = true
					changed++
				}
				fmt.Printf("    ~ COLUMN %s  %s → %s\n",
					colName,
					strings.ToUpper(c2.DataType),
					strings.ToUpper(c1.DataType))
				changed++
			}
		}

		// Removed columns
		for colName, c2 := range cols2 {
			if _, exists := cols1[colName]; !exists {
				if !tableChanged {
					fmt.Printf("\n  ~ TABLE %s\n", tableName)
					tableChanged = true
					changed++
				}
				fmt.Printf("    - COLUMN %s %s (removed)\n", c2.Name, strings.ToUpper(c2.DataType))
				removed++
			}
		}
	}

	// Find removed tables
	for tableName := range tables2 {
		if _, exists := tables1[tableName]; !exists {
			fmt.Printf("  - TABLE %s (removed in %s)\n", tableName, name1)
			removed++
		}
	}

	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────")
	if added == 0 && removed == 0 && changed == 0 {
		fmt.Printf("  No schema differences between %s and %s\n", name1, name2)
	} else {
		fmt.Printf("  %d added · %d removed · %d changed\n", added, removed, changed)
		fmt.Println()
		fmt.Println("  Legend:  + added   - removed   ~ modified")
	}
	fmt.Println()

	return nil
}

func diffMigrations(ctx context.Context, dbManager *db.Manager, id1, id2, name1, name2 string) error {
	// Get migrations for both branches
	type mig struct {
		Seq  int
		SQL  string
		Type string
		Desc string
	}

	getMigs := func(id string) []mig {
		rows, err := dbManager.Branch.Query(ctx, `
			SELECT sequence, sql, type, COALESCE(description,'')
			FROM pgdelta.branch_migrations WHERE branch_id = $1
			ORDER BY sequence
		`, id)
		if err != nil {
			return nil
		}
		defer rows.Close()
		var migs []mig
		for rows.Next() {
			var m mig
			rows.Scan(&m.Seq, &m.SQL, &m.Type, &m.Desc)
			migs = append(migs, m)
		}
		return migs
	}

	migs1 := getMigs(id1)
	migs2 := getMigs(id2)

	fmt.Printf("  %s has %d migration(s)\n", name1, len(migs1))
	fmt.Printf("  %s has %d migration(s)\n\n", name2, len(migs2))

	// Show migrations only in branch1
	checksums2 := make(map[string]bool)
	for _, m := range migs2 {
		checksums2[strings.TrimSpace(m.SQL)] = true
	}

	unique := 0
	for _, m := range migs1 {
		if !checksums2[strings.TrimSpace(m.SQL)] {
			fmt.Printf("  + #%d [%s]  %s\n", m.Seq, m.Type, m.Desc)
			unique++
		}
	}

	if unique == 0 {
		fmt.Printf("  No unique migrations in %s\n", name1)
	} else {
		fmt.Printf("\n  %d migration(s) in %s not in %s\n", unique, name1, name2)
	}
	fmt.Println()

	return nil
}

func getSchemaSnapshot(ctx context.Context, dbManager *db.Manager, schemaName string) map[string]tableDef {
	result := make(map[string]tableDef)

	rows, err := dbManager.Branch.Query(ctx, `
		SELECT table_name, column_name, data_type, is_nullable,
			COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = $1
		ORDER BY table_name, ordinal_position
	`, schemaName)
	if err != nil {
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var tableName, colName, dataType, isNullable, colDefault string
		if err := rows.Scan(&tableName, &colName, &dataType, &isNullable, &colDefault); err != nil {
			continue
		}

		t, exists := result[tableName]
		if !exists {
			t = tableDef{Name: tableName}
		}
		t.Columns = append(t.Columns, columnDef{
			Name:     colName,
			DataType: dataType,
			Nullable: isNullable == "YES",
			Default:  colDefault,
		})
		result[tableName] = t
	}

	return result
}
