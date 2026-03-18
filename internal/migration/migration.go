package migration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Type constants
const (
	TypeDDL  = "ddl"  // travels to parent on merge
	TypeSeed = "seed" // travels to parent on merge — intentional data
	TypeTest = "test" // stays in branch, discarded on merge
)

// Migration represents a single recorded migration
type Migration struct {
	ID          string
	BranchID    string
	Sequence    int
	SQL         string
	Type        string
	Description string
	Checksum    string
	AppliedAt   time.Time
}

// Manager handles migration recording and application
type Manager struct {
	BranchPool *pgxpool.Pool
}

// New creates a new migration manager
func New(branchPool *pgxpool.Pool) *Manager {
	return &Manager{BranchPool: branchPool}
}

// Record captures a SQL statement as a migration on a branch
func (m *Manager) Record(ctx context.Context, branchID, sql, migrationType, description string) (*Migration, error) {
	// Normalize SQL
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return nil, fmt.Errorf("migration SQL cannot be empty")
	}

	// Auto-detect type if not specified
	if migrationType == "" {
		migrationType = detectType(sql)
	}

	// Generate checksum
	checksum := generateChecksum(sql)

	// Get next sequence number
	var nextSeq int
	err := m.BranchPool.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1
		FROM pgdelta.branch_migrations
		WHERE branch_id = $1
	`, branchID).Scan(&nextSeq)
	if err != nil {
		return nil, fmt.Errorf("failed to get next sequence: %w", err)
	}

	// Auto-generate description if not provided
	if description == "" {
		description = generateDescription(sql)
	}

	// Record migration
	var id string
	err = m.BranchPool.QueryRow(ctx, `
		INSERT INTO pgdelta.branch_migrations
			(branch_id, sequence, sql, type, description, checksum)
		VALUES
			($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, branchID, nextSeq, sql, migrationType, description, checksum).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("failed to record migration: %w", err)
	}

	return &Migration{
		ID:          id,
		BranchID:    branchID,
		Sequence:    nextSeq,
		SQL:         sql,
		Type:        migrationType,
		Description: description,
		Checksum:    checksum,
		AppliedAt:   time.Now(),
	}, nil
}

// Apply executes a SQL statement on the branch schema and records it
func (m *Manager) Apply(ctx context.Context, branchID, schemaName, sql, description string) (*Migration, error) {
	// Set search_path to branch schema so DDL applies there
	_, err := m.BranchPool.Exec(ctx,
		fmt.Sprintf("SET search_path TO %s, public", schemaName),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to set search_path: %w", err)
	}

	// Execute the SQL
	_, err = m.BranchPool.Exec(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	// Reset search_path
	_, _ = m.BranchPool.Exec(ctx, "RESET search_path")

	// Record it
	migrationType := detectType(sql)
	migration, err := m.Record(ctx, branchID, sql, migrationType, description)
	if err != nil {
		return nil, fmt.Errorf("migration applied but failed to record: %w", err)
	}

	return migration, nil
}

// List returns all migrations for a branch in sequence order
func (m *Manager) List(ctx context.Context, branchID string) ([]Migration, error) {
	rows, err := m.BranchPool.Query(ctx, `
		SELECT id, branch_id, sequence, sql, type,
		       COALESCE(description, ''), checksum, applied_at
		FROM pgdelta.branch_migrations
		WHERE branch_id = $1
		ORDER BY sequence ASC
	`, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var migrations []Migration
	for rows.Next() {
		var mg Migration
		if err := rows.Scan(
			&mg.ID, &mg.BranchID, &mg.Sequence, &mg.SQL,
			&mg.Type, &mg.Description, &mg.Checksum, &mg.AppliedAt,
		); err != nil {
			return nil, err
		}
		migrations = append(migrations, mg)
	}
	return migrations, rows.Err()
}

// ListMergeable returns only migrations that should travel to parent on merge
// DDL and seed migrations travel — test DML stays in branch
func (m *Manager) ListMergeable(ctx context.Context, branchID string) ([]Migration, error) {
	rows, err := m.BranchPool.Query(ctx, `
		SELECT id, branch_id, sequence, sql, type,
		       COALESCE(description, ''), checksum, applied_at
		FROM pgdelta.branch_migrations
		WHERE branch_id = $1
		  AND type IN ('ddl', 'seed')
		ORDER BY sequence ASC
	`, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var migrations []Migration
	for rows.Next() {
		var mg Migration
		if err := rows.Scan(
			&mg.ID, &mg.BranchID, &mg.Sequence, &mg.SQL,
			&mg.Type, &mg.Description, &mg.Checksum, &mg.AppliedAt,
		); err != nil {
			return nil, err
		}
		migrations = append(migrations, mg)
	}
	return migrations, rows.Err()
}

// Count returns total migration count for a branch
func (m *Manager) Count(ctx context.Context, branchID string) (int, error) {
	var count int
	err := m.BranchPool.QueryRow(ctx,
		"SELECT COUNT(*) FROM pgdelta.branch_migrations WHERE branch_id = $1",
		branchID,
	).Scan(&count)
	return count, err
}

// detectType determines if SQL is DDL, seed, or test
func detectType(sql string) string {
	// Check for explicit seed marker comment
	if strings.Contains(sql, "pgdelta:seed") {
		return TypeSeed
	}

	upper := strings.ToUpper(strings.TrimSpace(sql))

	// DDL statements
	ddlPrefixes := []string{
		"CREATE TABLE", "DROP TABLE", "ALTER TABLE",
		"CREATE INDEX", "DROP INDEX",
		"CREATE SEQUENCE", "DROP SEQUENCE",
		"CREATE TYPE", "DROP TYPE",
		"CREATE VIEW", "DROP VIEW",
		"CREATE FUNCTION", "DROP FUNCTION",
		"CREATE TRIGGER", "DROP TRIGGER",
		"CREATE SCHEMA", "DROP SCHEMA",
		"TRUNCATE",
	}

	for _, prefix := range ddlPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return TypeDDL
		}
	}

	// Everything else is test DML (INSERT, UPDATE, DELETE)
	return TypeTest
}

// generateChecksum creates a SHA256 checksum of the SQL
func generateChecksum(sql string) string {
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(sql)))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// generateDescription creates a human readable description from SQL
func generateDescription(sql string) string {
	upper := strings.ToUpper(strings.TrimSpace(sql))
	words := strings.Fields(sql)

	if len(words) < 3 {
		return sql
	}

	switch {
	case strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "ADD COLUMN"):
		return fmt.Sprintf("add column to %s", extractTableName(words, 2))
	case strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "DROP COLUMN"):
		return fmt.Sprintf("drop column from %s", extractTableName(words, 2))
	case strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "RENAME"):
		return fmt.Sprintf("rename in %s", extractTableName(words, 2))
	case strings.HasPrefix(upper, "ALTER TABLE"):
		return fmt.Sprintf("alter %s", extractTableName(words, 2))
	case strings.HasPrefix(upper, "CREATE TABLE"):
		return fmt.Sprintf("create table %s", extractTableName(words, 2))
	case strings.HasPrefix(upper, "DROP TABLE"):
		return fmt.Sprintf("drop table %s", extractTableName(words, 2))
	case strings.HasPrefix(upper, "CREATE INDEX"):
		return fmt.Sprintf("create index %s", words[2])
	case strings.HasPrefix(upper, "DROP INDEX"):
		return fmt.Sprintf("drop index %s", words[2])
	case strings.HasPrefix(upper, "INSERT"):
		return "insert seed data"
	case strings.HasPrefix(upper, "UPDATE"):
		return "update data"
	default:
		// First 50 chars of SQL
		if len(sql) > 50 {
			return sql[:50] + "..."
		}
		return sql
	}
}

// extractTableName safely extracts table name from word list
func extractTableName(words []string, idx int) string {
	if idx < len(words) {
		return strings.ToLower(words[idx])
	}
	return "unknown"
}

// Print prints a migration list in a readable format
func Print(migrations []Migration) {
	if len(migrations) == 0 {
		fmt.Println("  No migrations recorded.")
		return
	}

	fmt.Printf("\n  %-4s  %-6s  %-30s  %s\n", "SEQ", "TYPE", "DESCRIPTION", "APPLIED")
	fmt.Println("  ─────────────────────────────────────────────────────")

	for _, mg := range migrations {
		typeIcon := "⟳"
		switch mg.Type {
		case TypeDDL:
			typeIcon = "⬆"
		case TypeSeed:
			typeIcon = "🌱"
		case TypeTest:
			typeIcon = "🧪"
		}

		fmt.Printf("  %-4d  %s %-4s  %-30s  %s\n",
			mg.Sequence,
			typeIcon,
			mg.Type,
			truncate(mg.Description, 30),
			mg.AppliedAt.Format("15:04:05"),
		)
	}
	fmt.Println()
}

// truncate shortens a string to max length
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}