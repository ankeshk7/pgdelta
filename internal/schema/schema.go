package schema

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Table represents a full table definition read from pg_catalog
type Table struct {
	Name        string
	Columns     []Column
	Indexes     []Index
	Constraints []Constraint
}

// Column represents a single column definition
type Column struct {
	Name       string
	DataType   string
	IsNullable bool
	Default    string
	Position   int
}

// Index represents a table index
type Index struct {
	Name       string
	Definition string
	IsPrimary  bool
	IsUnique   bool
}

// Constraint represents a table constraint
type Constraint struct {
	Name       string
	Type       string // p=primary, f=foreign, u=unique, c=check
	Definition string
}

// Snapshot reads the full schema of the public schema from main DB
func Snapshot(ctx context.Context, mainPool *pgxpool.Pool) ([]Table, error) {
	tableNames, err := listTables(ctx, mainPool)
	if err != nil {
		return nil, fmt.Errorf("failed to list tables: %w", err)
	}

	var tables []Table
	for _, name := range tableNames {
		table, err := readTable(ctx, mainPool, name)
		if err != nil {
			return nil, fmt.Errorf("failed to read table %s: %w", name, err)
		}
		tables = append(tables, table)
	}

	return tables, nil
}

// listTables returns all user table names in the public schema
func listTables(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT tablename
		FROM pg_tables
		WHERE schemaname = 'public'
		ORDER BY tablename
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// readTable reads the full definition of a single table
func readTable(ctx context.Context, pool *pgxpool.Pool, tableName string) (Table, error) {
	table := Table{Name: tableName}

	columns, err := readColumns(ctx, pool, tableName)
	if err != nil {
		return table, err
	}
	table.Columns = columns

	indexes, err := readIndexes(ctx, pool, tableName)
	if err != nil {
		return table, err
	}
	table.Indexes = indexes

	constraints, err := readConstraints(ctx, pool, tableName)
	if err != nil {
		return table, err
	}
	table.Constraints = constraints

	return table, nil
}

// readColumns reads all column definitions for a table
func readColumns(ctx context.Context, pool *pgxpool.Pool, tableName string) ([]Column, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			column_name,
			data_type,
			is_nullable,
			column_default,
			ordinal_position
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = $1
		ORDER BY ordinal_position
	`, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []Column
	for rows.Next() {
		var col Column
		var isNullable string
		var defaultVal *string

		if err := rows.Scan(
			&col.Name,
			&col.DataType,
			&isNullable,
			&defaultVal,
			&col.Position,
		); err != nil {
			return nil, err
		}

		col.IsNullable = isNullable == "YES"
		if defaultVal != nil {
			col.Default = *defaultVal
		}

		columns = append(columns, col)
	}
	return columns, rows.Err()
}

// readIndexes reads all indexes for a table
func readIndexes(ctx context.Context, pool *pgxpool.Pool, tableName string) ([]Index, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			i.relname AS index_name,
			pg_get_indexdef(ix.indexrelid) AS index_def,
			ix.indisprimary,
			ix.indisunique
		FROM pg_index ix
		JOIN pg_class t ON t.oid = ix.indrelid
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = 'public'
		  AND t.relname = $1
		  AND t.relkind = 'r'
		ORDER BY i.relname
	`, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indexes []Index
	for rows.Next() {
		var idx Index
		if err := rows.Scan(
			&idx.Name,
			&idx.Definition,
			&idx.IsPrimary,
			&idx.IsUnique,
		); err != nil {
			return nil, err
		}
		indexes = append(indexes, idx)
	}
	return indexes, rows.Err()
}

// readConstraints reads all constraints for a table
func readConstraints(ctx context.Context, pool *pgxpool.Pool, tableName string) ([]Constraint, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			con.conname,
			con.contype::text,
			pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class rel ON rel.oid = con.conrelid
		JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
		WHERE nsp.nspname = 'public'
		  AND rel.relname = $1
		ORDER BY con.conname
	`, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var constraints []Constraint
	for rows.Next() {
		var c Constraint
		if err := rows.Scan(&c.Name, &c.Type, &c.Definition); err != nil {
			return nil, err
		}
		constraints = append(constraints, c)
	}
	return constraints, rows.Err()
}

// Recreate creates all tables from a snapshot inside a target schema on branch DB
func Recreate(ctx context.Context, branchPool *pgxpool.Pool, schemaName string, tables []Table) error {
	// Create the schema
	if _, err := branchPool.Exec(ctx, fmt.Sprintf(
		"CREATE SCHEMA IF NOT EXISTS %s", schemaName,
	)); err != nil {
		return fmt.Errorf("failed to create schema %s: %w", schemaName, err)
	}

	// Order tables by dependency
	ordered := orderByDependency(tables)

	// Create sequences first (needed for SERIAL columns)
	for _, table := range ordered {
		for _, col := range table.Columns {
			if strings.Contains(col.Default, "nextval(") {
				seqName := extractSequenceName(col.Default)
				if seqName != "" {
					createSeq := fmt.Sprintf(
						"CREATE SEQUENCE IF NOT EXISTS %s.%s",
						schemaName, seqName,
					)
					if _, err := branchPool.Exec(ctx, createSeq); err != nil {
						return fmt.Errorf("failed to create sequence %s: %w", seqName, err)
					}
				}
			}
		}
	}

	// Create each table
	for _, table := range ordered {
		ddl := generateCreateTable(schemaName, table)
		if _, err := branchPool.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("failed to create table %s.%s: %w\nDDL: %s",
				schemaName, table.Name, err, ddl)
		}
	}

	// Create indexes (after all tables exist)
for _, table := range ordered {
    for _, idx := range table.Indexes {
        if idx.IsPrimary {
            continue
        }
        // Skip — unique constraints already create their index
        if idx.IsUnique {
            continue
        }
        idxDDL := rewriteIndexSchema(idx.Definition, schemaName)
        if _, err := branchPool.Exec(ctx, idxDDL); err != nil {
            fmt.Printf("  warning: failed to create index %s: %v\n", idx.Name, err)
        }
    }
}

	return nil
}

// generateCreateTable generates a CREATE TABLE statement for a given schema
func generateCreateTable(schemaName string, table Table) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s.%s (\n", schemaName, table.Name))

	var parts []string

	// Columns
	for _, col := range table.Columns {
		part := fmt.Sprintf("  %s %s", col.Name, mapDataType(col.DataType))

		if !col.IsNullable {
			part += " NOT NULL"
		}

		if col.Default != "" {
			rewrittenDefault := rewriteSequenceDefault(col.Default, schemaName)
			part += fmt.Sprintf(" DEFAULT %s", rewrittenDefault)
		}

		parts = append(parts, part)
	}

	// Constraints (skip FK on first pass)
	for _, con := range table.Constraints {
		if con.Type == "f" {
			continue
		}
		parts = append(parts, fmt.Sprintf("  CONSTRAINT %s %s", con.Name, con.Definition))
	}

	sb.WriteString(strings.Join(parts, ",\n"))
	sb.WriteString("\n)")

	return sb.String()
}

// extractSequenceName pulls the sequence name out of a nextval default
// nextval('products_id_seq'::regclass) → products_id_seq
func extractSequenceName(defaultVal string) string {
	start := strings.Index(defaultVal, "'")
	end := strings.LastIndex(defaultVal, "'")
	if start == -1 || end == -1 || start == end {
		return ""
	}
	full := defaultVal[start+1 : end]
	// Remove schema prefix if present
	if idx := strings.LastIndex(full, "."); idx != -1 {
		return full[idx+1:]
	}
	return full
}

// rewriteSequenceDefault rewrites nextval to reference the branch schema
// nextval('products_id_seq'::regclass) → nextval('branch_x.products_id_seq'::regclass)
func rewriteSequenceDefault(defaultVal, schemaName string) string {
	if !strings.Contains(defaultVal, "nextval(") {
		return defaultVal
	}
	seqName := extractSequenceName(defaultVal)
	if seqName == "" {
		return defaultVal
	}
	return fmt.Sprintf("nextval('%s.%s'::regclass)", schemaName, seqName)
}

// mapDataType maps information_schema data types to Postgres DDL types
func mapDataType(dataType string) string {
	mapping := map[string]string{
		"integer":                      "INTEGER",
		"bigint":                       "BIGINT",
		"smallint":                     "SMALLINT",
		"text":                         "TEXT",
		"character varying":            "TEXT",
		"character":                    "TEXT",
		"boolean":                      "BOOLEAN",
		"numeric":                      "NUMERIC",
		"real":                         "REAL",
		"double precision":             "DOUBLE PRECISION",
		"timestamp with time zone":     "TIMESTAMPTZ",
		"timestamp without time zone":  "TIMESTAMP",
		"date":                         "DATE",
		"time with time zone":          "TIMETZ",
		"time without time zone":       "TIME",
		"jsonb":                        "JSONB",
		"json":                         "JSON",
		"uuid":                         "UUID",
		"bytea":                        "BYTEA",
		"inet":                         "INET",
		"cidr":                         "CIDR",
	}

	if mapped, ok := mapping[strings.ToLower(dataType)]; ok {
		return mapped
	}
	return strings.ToUpper(dataType)
}

// orderByDependency sorts tables so referenced tables come before referencing tables
func orderByDependency(tables []Table) []Table {
	var withoutFK []Table
	var withFK []Table

	for _, t := range tables {
		hasFk := false
		for _, c := range t.Constraints {
			if c.Type == "f" {
				hasFk = true
				break
			}
		}
		if hasFk {
			withFK = append(withFK, t)
		} else {
			withoutFK = append(withoutFK, t)
		}
	}

	return append(withoutFK, withFK...)
}

// rewriteIndexSchema rewrites an index definition to use the branch schema
func rewriteIndexSchema(definition, schemaName string) string {
	return strings.ReplaceAll(definition, "ON public.", fmt.Sprintf("ON %s.", schemaName))
}

// PrintSnapshot prints a human readable summary of the schema
func PrintSnapshot(tables []Table) {
	fmt.Printf("\n  Schema snapshot — %d tables\n\n", len(tables))
	for _, t := range tables {
		fmt.Printf("  %-20s  %d columns", t.Name, len(t.Columns))
		if len(t.Indexes) > 0 {
			fmt.Printf("  %d indexes", len(t.Indexes))
		}
		fmt.Println()
		for _, col := range t.Columns {
			nullable := ""
			if !col.IsNullable {
				nullable = " NOT NULL"
			}
			fmt.Printf("    %-20s %s%s\n", col.Name, col.DataType, nullable)
		}
		fmt.Println()
	}
}

// EnsureMainBranch creates branch_main schema on branch DB
// mirroring the main DB schema — used as merge target
func EnsureMainBranch(ctx context.Context, mainPool, branchPool *pgxpool.Pool) error {
	tables, err := Snapshot(ctx, mainPool)
	if err != nil {
		return fmt.Errorf("failed to snapshot main schema: %w", err)
	}
	return Recreate(ctx, branchPool, "branch_main", tables)
}