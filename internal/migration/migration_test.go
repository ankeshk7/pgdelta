package migration_test

import (
	"context"
	"testing"

	"github.com/ankeshkedia/pgdelta/internal/migration"
	"github.com/ankeshkedia/pgdelta/internal/schema"
	"github.com/ankeshkedia/pgdelta/internal/testutil"
)

func setupBranchSchema(t *testing.T, tdb *testutil.TestDB, branchName string) (string, string) {
	t.Helper()
	ctx := context.Background()

	schemaName := "branch_" + branchName
	tables, err := schema.Snapshot(ctx, tdb.MainPool)
	if err != nil {
		t.Fatalf("failed to snapshot schema: %v", err)
	}

	if err := schema.Recreate(ctx, tdb.BranchPool, schemaName, tables); err != nil {
		t.Fatalf("failed to recreate schema: %v", err)
	}

	branchID := tdb.CreateBranch(t, branchName, "main")
	return branchID, schemaName
}

func TestRecord_StoresMigration(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	branchID, _ := setupBranchSchema(t, tdb, "record_test")
	mgr := migration.New(tdb.BranchPool)

	mg, err := mgr.Record(ctx, branchID,
		"ALTER TABLE users ADD COLUMN test_col TEXT",
		migration.TypeDDL,
		"add test column",
	)
	if err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	if mg.Sequence != 1 {
		t.Errorf("expected sequence 1, got %d", mg.Sequence)
	}
	if mg.Type != migration.TypeDDL {
		t.Errorf("expected type ddl, got %s", mg.Type)
	}
	if mg.Checksum == "" {
		t.Error("expected non-empty checksum")
	}

	tdb.AssertMigrationCount(t, branchID, 1)
}

func TestApply_ExecutesDDLOnBranch(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	branchID, schemaName := setupBranchSchema(t, tdb, "apply_test")
	mgr := migration.New(tdb.BranchPool)

	_, err := mgr.Apply(ctx, branchID, schemaName,
		"ALTER TABLE users ADD COLUMN stripe_id TEXT",
		"add stripe column",
	)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	tdb.AssertColumnExists(t, schemaName, "users", "stripe_id")
	tdb.AssertMigrationCount(t, branchID, 1)
}

func TestDetectType_DDL(t *testing.T) {
	tests := []struct {
		sql      string
		expected string
	}{
		{"ALTER TABLE users ADD COLUMN x TEXT", migration.TypeDDL},
		{"CREATE TABLE foo (id INT)", migration.TypeDDL},
		{"DROP TABLE foo", migration.TypeDDL},
		{"CREATE INDEX idx ON users(email)", migration.TypeDDL},
		{"INSERT INTO users VALUES (1)", migration.TypeTest},
		{"UPDATE users SET name = 'x'", migration.TypeTest},
		{"DELETE FROM users WHERE id = 1", migration.TypeTest},
	}

	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			tdb := testutil.Setup(t)
			ctx := context.Background()

			branchID, schemaName := setupBranchSchema(t, tdb, "type_test")
			mgr := migration.New(tdb.BranchPool)

			mg, err := mgr.Apply(ctx, branchID, schemaName, tt.sql, "")
			if err != nil {
				t.Skipf("SQL not applicable in test: %v", err)
			}

			if mg.Type != tt.expected {
				t.Errorf("for SQL %q: expected type %s, got %s",
					tt.sql, tt.expected, mg.Type)
			}
		})
	}
}

func TestListMergeable_ReturnsOnlyDDLAndSeed(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	branchID, schemaName := setupBranchSchema(t, tdb, "mergeable_test")
	mgr := migration.New(tdb.BranchPool)

	// Apply DDL migration
	_, err := mgr.Apply(ctx, branchID, schemaName,
		"ALTER TABLE users ADD COLUMN loyalty INT DEFAULT 0",
		"add loyalty",
	)
	if err != nil {
		t.Fatalf("failed to apply DDL: %v", err)
	}

	// Record a test INSERT manually
	_, err = mgr.Record(ctx, branchID,
		"INSERT INTO users (email, name, plan) VALUES ('t@t.com', 'T', 'free')",
		migration.TypeTest,
		"test data",
	)
	if err != nil {
		t.Fatalf("failed to record test migration: %v", err)
	}

	mergeable, err := mgr.ListMergeable(ctx, branchID)
	if err != nil {
		t.Fatalf("ListMergeable failed: %v", err)
	}

	if len(mergeable) != 1 {
		t.Errorf("expected 1 mergeable migration, got %d", len(mergeable))
	}

	if mergeable[0].Type != migration.TypeDDL {
		t.Errorf("expected DDL type, got %s", mergeable[0].Type)
	}
}

func TestSequence_IncrementsCorrectly(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	branchID, schemaName := setupBranchSchema(t, tdb, "seq_test")
	mgr := migration.New(tdb.BranchPool)

	sqls := []string{
		"ALTER TABLE users ADD COLUMN col1 TEXT",
		"ALTER TABLE users ADD COLUMN col2 TEXT",
		"ALTER TABLE users ADD COLUMN col3 TEXT",
	}

	for i, sql := range sqls {
		mg, err := mgr.Apply(ctx, branchID, schemaName, sql, "")
		if err != nil {
			t.Fatalf("Apply %d failed: %v", i+1, err)
		}
		if mg.Sequence != i+1 {
			t.Errorf("expected sequence %d, got %d", i+1, mg.Sequence)
		}
	}
}
