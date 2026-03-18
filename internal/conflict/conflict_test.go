package conflict_test

import (
	"context"
	"testing"

	"github.com/ankeshkedia/pgdelta/internal/conflict"
	"github.com/ankeshkedia/pgdelta/internal/schema"
	"github.com/ankeshkedia/pgdelta/internal/testutil"
)

func setupParentSchema(t *testing.T, tdb *testutil.TestDB, schemaName string) {
	t.Helper()
	ctx := context.Background()

	tables, err := schema.Snapshot(ctx, tdb.MainPool)
	if err != nil {
		t.Fatalf("failed to snapshot: %v", err)
	}
	if err := schema.Recreate(ctx, tdb.BranchPool, schemaName, tables); err != nil {
		t.Fatalf("failed to recreate: %v", err)
	}
}

func TestSimulate_CleanMigrations(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	setupParentSchema(t, tdb, "branch_parent_clean")
	sim := conflict.New(tdb.BranchPool)

	result, err := sim.Simulate(ctx, "branch_parent_clean", []string{
		"ALTER TABLE users ADD COLUMN stripe_id TEXT",
		"CREATE INDEX idx_stripe ON users(stripe_id)",
	})
	if err != nil {
		t.Fatalf("Simulate failed: %v", err)
	}

	if !result.Clean {
		t.Errorf("expected clean simulation, got %d conflicts", len(result.Conflicts))
	}
	if len(result.Safe) != 2 {
		t.Errorf("expected 2 safe migrations, got %d", len(result.Safe))
	}
}

func TestSimulate_DetectsColumnExists(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	setupParentSchema(t, tdb, "branch_parent_col")

	// Pre-add the column to parent
	_, err := tdb.BranchPool.Exec(ctx,
		"SET search_path TO branch_parent_col, public")
	if err != nil {
		t.Fatalf("failed to set search path: %v", err)
	}
	_, err = tdb.BranchPool.Exec(ctx,
		"ALTER TABLE users ADD COLUMN existing_col TEXT")
	if err != nil {
		t.Fatalf("failed to pre-add column: %v", err)
	}
	_, _ = tdb.BranchPool.Exec(ctx, "RESET search_path")

	sim := conflict.New(tdb.BranchPool)

	// Try to add same column again
	result, err := sim.Simulate(ctx, "branch_parent_col", []string{
		"ALTER TABLE users ADD COLUMN existing_col TEXT",
	})
	if err != nil {
		t.Fatalf("Simulate failed: %v", err)
	}

	if result.Clean {
		t.Error("expected conflict but simulation was clean")
	}
	if len(result.Conflicts) != 1 {
		t.Errorf("expected 1 conflict, got %d", len(result.Conflicts))
	}
}

func TestSimulate_DetectsIndexExists(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	setupParentSchema(t, tdb, "branch_parent_idx")

	// Pre-create the index
	_, _ = tdb.BranchPool.Exec(ctx,
		"SET search_path TO branch_parent_idx, public")
	_, _ = tdb.BranchPool.Exec(ctx,
		"CREATE INDEX idx_existing ON branch_parent_idx.users(name)")
	_, _ = tdb.BranchPool.Exec(ctx, "RESET search_path")

	sim := conflict.New(tdb.BranchPool)

	result, err := sim.Simulate(ctx, "branch_parent_idx", []string{
		"CREATE INDEX idx_existing ON users(name)",
	})
	if err != nil {
		t.Fatalf("Simulate failed: %v", err)
	}

	if result.Clean {
		t.Error("expected conflict for duplicate index")
	}
	if len(result.Conflicts) == 0 {
		t.Error("expected at least 1 conflict")
	}
}

func TestSimulate_NeverAppliesChanges(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	setupParentSchema(t, tdb, "branch_parent_rollback")
	sim := conflict.New(tdb.BranchPool)

	// Simulate adding a column
	_, err := sim.Simulate(ctx, "branch_parent_rollback", []string{
		"ALTER TABLE users ADD COLUMN sim_only TEXT",
	})
	if err != nil {
		t.Fatalf("Simulate failed: %v", err)
	}

	// Column should NOT exist — simulation rolls back
	var exists bool
	_ = tdb.BranchPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'branch_parent_rollback'
			  AND table_name = 'users'
			  AND column_name = 'sim_only'
		)
	`).Scan(&exists)

	if exists {
		t.Error("simulation should have rolled back — column should not exist")
	}
}

func TestAutoResolve_IndexExists(t *testing.T) {
	c := conflict.Conflict{
		Type:           conflict.TypeIndexExists,
		AutoResolvable: true,
	}

	sql := "CREATE INDEX idx_test ON users(email)"
	resolved, ok := conflict.AutoResolve(sql, c)

	if !ok {
		t.Error("expected auto-resolve to succeed")
	}
	if resolved != "CREATE INDEX IF NOT EXISTS idx_test ON users(email)" {
		t.Errorf("unexpected resolved SQL: %s", resolved)
	}
}

func TestAutoResolve_ColumnExists_SkipsMigration(t *testing.T) {
	c := conflict.Conflict{
		Type:           conflict.TypeColumnExists,
		AutoResolvable: true,
	}

	sql := "ALTER TABLE users ADD COLUMN existing TEXT"
	resolved, ok := conflict.AutoResolve(sql, c)

	if !ok {
		t.Error("expected auto-resolve to succeed")
	}
	if resolved != "" {
		t.Errorf("expected empty SQL for skipped migration, got: %s", resolved)
	}
}

func TestSimulate_MultipleConflictsDetected(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	setupParentSchema(t, tdb, "branch_parent_multi")
	sim := conflict.New(tdb.BranchPool)

	// Both migrations reference non-existent table
	result, err := sim.Simulate(ctx, "branch_parent_multi", []string{
		"ALTER TABLE nonexistent ADD COLUMN x TEXT",
		"ALTER TABLE alsononexistent ADD COLUMN y TEXT",
	})
	if err != nil {
		t.Fatalf("Simulate failed: %v", err)
	}

	if result.Clean {
		t.Error("expected conflicts")
	}
	if len(result.Conflicts) < 1 {
		t.Errorf("expected at least 1 conflict, got %d", len(result.Conflicts))
	}
}
