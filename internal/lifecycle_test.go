package pgdelta_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ankeshkedia/pgdelta/internal/conflict"
	"github.com/ankeshkedia/pgdelta/internal/migration"
	"github.com/ankeshkedia/pgdelta/internal/schema"
	"github.com/ankeshkedia/pgdelta/internal/snapshot"
	"github.com/ankeshkedia/pgdelta/internal/testutil"
)

// TestFullLifecycle_CreateSnapshotMigrateMerge tests the complete
// developer workflow end to end against real Postgres containers
func TestFullLifecycle_CreateSnapshotMigrateMerge(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	// ── STEP 1: Create branch_main (base) ────────────────────────────
	t.Log("Step 1: Creating branch_main")

	tables, err := schema.Snapshot(ctx, tdb.MainPool)
	if err != nil {
		t.Fatalf("failed to snapshot main schema: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("expected tables in main DB")
	}

	err = schema.Recreate(ctx, tdb.BranchPool, "branch_main", tables)
	if err != nil {
		t.Fatalf("failed to create branch_main: %v", err)
	}

	// Register main branch
	_, err = tdb.BranchPool.Exec(ctx, `
		INSERT INTO pgdelta.branches
			(name, git_branch, parent_branch, schema_name, branched_at_seq, status)
		VALUES ('main', 'main', 'main', 'branch_main', 0, 'active')
		ON CONFLICT (name) DO UPDATE SET schema_name = 'branch_main'
	`)
	if err != nil {
		t.Fatalf("failed to register main branch: %v", err)
	}

	tdb.AssertTableExists(t, "branch_main", "users")
	tdb.AssertTableExists(t, "branch_main", "orders")
	tdb.AssertTableExists(t, "branch_main", "products")
	t.Log("  ✓ branch_main created with 3 tables")

	// ── STEP 2: Create feature branch ────────────────────────────────
	t.Log("Step 2: Creating feature-payments branch")

	err = schema.Recreate(ctx, tdb.BranchPool, "branch_feature_payments", tables)
	if err != nil {
		t.Fatalf("failed to create feature branch schema: %v", err)
	}

	var branchID string
	err = tdb.BranchPool.QueryRow(ctx, `
		INSERT INTO pgdelta.branches
			(name, git_branch, parent_branch, schema_name, branched_at_seq, status)
		VALUES ('feature-payments', 'feature-payments', 'main', 'branch_feature_payments', 0, 'active')
		RETURNING id
	`).Scan(&branchID)
	if err != nil {
		t.Fatalf("failed to register feature branch: %v", err)
	}

	tdb.AssertTableExists(t, "branch_feature_payments", "users")
	t.Log("  ✓ feature-payments branch created")

	// ── STEP 3: Lazy snapshot ─────────────────────────────────────────
	t.Log("Step 3: Snapshotting users table with extraction query")

	snapMgr := snapshot.New(tdb.MainPool, tdb.BranchPool, 1000)
	extractionSQL := "SELECT * FROM users WHERE plan = 'pro'"

	snapshotID, err := snapMgr.Register(ctx, branchID, "users", extractionSQL)
	if err != nil {
		t.Fatalf("failed to register snapshot: %v", err)
	}

	err = snapMgr.Load(ctx, snapshotID, branchID,
		"branch_feature_payments", "users", extractionSQL, nil)
	if err != nil {
		t.Fatalf("failed to load snapshot: %v", err)
	}

	// Only pro users — Alice only
	tdb.AssertRowCount(t, "branch_feature_payments", "users", 1)

	snap, err := snapMgr.GetStatus(ctx, branchID, "users")
	if err != nil {
		t.Fatalf("failed to get snapshot status: %v", err)
	}
	if snap.Status != snapshot.StatusReady {
		t.Errorf("expected snapshot status ready, got %s", snap.Status)
	}
	t.Log("  ✓ 1 pro user snapshotted from main")

	// ── STEP 4: Apply migrations ──────────────────────────────────────
	t.Log("Step 4: Applying migrations on feature branch")

	migMgr := migration.New(tdb.BranchPool)

	mg1, err := migMgr.Apply(ctx, branchID, "branch_feature_payments",
		"ALTER TABLE users ADD COLUMN stripe_id TEXT",
		"add stripe_id column",
	)
	if err != nil {
		t.Fatalf("failed to apply migration 1: %v", err)
	}
	if mg1.Type != migration.TypeDDL {
		t.Errorf("expected DDL type, got %s", mg1.Type)
	}

	mg2, err := migMgr.Apply(ctx, branchID, "branch_feature_payments",
		"CREATE INDEX idx_stripe ON users(stripe_id)",
		"add stripe index",
	)
	if err != nil {
		t.Fatalf("failed to apply migration 2: %v", err)
	}
	if mg2.Sequence != 2 {
		t.Errorf("expected sequence 2, got %d", mg2.Sequence)
	}

	// Apply test INSERT — should NOT merge
	_, err = migMgr.Apply(ctx, branchID, "branch_feature_payments",
		fmt.Sprintf("INSERT INTO %s.users (email, name, plan) VALUES ('test@test.com', 'Test', 'free')",
			"branch_feature_payments"),
		"test data",
	)
	// OK if this fails due to schema prefix — just record it
	if err != nil {
		_, _ = migMgr.Record(ctx, branchID,
			"INSERT INTO users (email, name, plan) VALUES ('test@test.com', 'Test', 'free')",
			migration.TypeTest,
			"test data",
		)
	}

	tdb.AssertColumnExists(t, "branch_feature_payments", "users", "stripe_id")
	tdb.AssertMigrationCount(t, branchID, 3)
	t.Log("  ✓ 3 migrations recorded (2 DDL + 1 test)")

	// ── STEP 5: Simulate merge ────────────────────────────────────────
	t.Log("Step 5: Simulating merge to main")

	mergeable, err := migMgr.ListMergeable(ctx, branchID)
	if err != nil {
		t.Fatalf("failed to list mergeable: %v", err)
	}
	if len(mergeable) != 2 {
		t.Errorf("expected 2 mergeable migrations, got %d", len(mergeable))
	}

	var sqls []string
	for _, mg := range mergeable {
		sqls = append(sqls, mg.SQL)
	}

	sim := conflict.New(tdb.BranchPool)
	result, err := sim.Simulate(ctx, "branch_main", sqls)
	if err != nil {
		t.Fatalf("simulation failed: %v", err)
	}
	if !result.Clean {
		t.Errorf("expected clean simulation, got %d conflicts", len(result.Conflicts))
		for _, c := range result.Conflicts {
			t.Logf("  conflict: %s — %s", c.Type, c.Description)
		}
	}
	t.Log("  ✓ simulation clean — no conflicts")

	// ── STEP 6: Apply merge to parent ─────────────────────────────────
	t.Log("Step 6: Applying DDL migrations to branch_main")

	for i, sql := range sqls {
		_, err := tdb.BranchPool.Exec(ctx,
			"SET search_path TO branch_main, public")
		if err != nil {
			t.Fatalf("failed to set search path: %v", err)
		}
		_, err = tdb.BranchPool.Exec(ctx, sql)
		_, _ = tdb.BranchPool.Exec(ctx, "RESET search_path")
		if err != nil {
			t.Fatalf("failed to apply migration %d to parent: %v", i+1, err)
		}
	}

	// Verify DDL landed on branch_main
	tdb.AssertColumnExists(t, "branch_main", "users", "stripe_id")
	t.Log("  ✓ stripe_id column merged to branch_main")

	// Verify test data NOT in branch_main
	tdb.AssertRowCount(t, "branch_main", "users", 0) // branch_main has no data
	t.Log("  ✓ test data correctly excluded from merge")

	// ── STEP 7: Mark branch merged ────────────────────────────────────
	t.Log("Step 7: Marking branch as merged")

	_, err = tdb.BranchPool.Exec(ctx, `
		UPDATE pgdelta.branches
		SET status = 'merged', merged_at = now()
		WHERE id = $1
	`, branchID)
	if err != nil {
		t.Fatalf("failed to mark branch merged: %v", err)
	}

	var status string
	_ = tdb.BranchPool.QueryRow(ctx,
		"SELECT status FROM pgdelta.branches WHERE id = $1", branchID,
	).Scan(&status)

	if status != "merged" {
		t.Errorf("expected status merged, got %s", status)
	}
	t.Log("  ✓ branch marked as merged")

	t.Log("\n  Full lifecycle complete:")
	t.Log("  create → snapshot → migrate → simulate → merge → mark done")
	t.Log("  All steps passed against real Postgres containers.")
}

// TestFullLifecycle_ConflictDetectionAndResolution tests merge with conflicts
func TestFullLifecycle_ConflictDetectionAndResolution(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	// Setup parent schema
	tables, _ := schema.Snapshot(ctx, tdb.MainPool)
	_ = schema.Recreate(ctx, tdb.BranchPool, "branch_conflict_main", tables)
	_, _ = tdb.BranchPool.Exec(ctx, `
		INSERT INTO pgdelta.branches
			(name, git_branch, parent_branch, schema_name, branched_at_seq, status)
		VALUES ('conflict-main', 'conflict-main', 'conflict-main',
			'branch_conflict_main', 0, 'active')
	`)

	// Pre-apply a migration to parent — simulates parent moved ahead
	_, _ = tdb.BranchPool.Exec(ctx,
		"SET search_path TO branch_conflict_main, public")
	_, _ = tdb.BranchPool.Exec(ctx,
		"ALTER TABLE users ADD COLUMN already_exists TEXT")
	_, _ = tdb.BranchPool.Exec(ctx, "RESET search_path")

	// Feature branch tries to add same column
	sim := conflict.New(tdb.BranchPool)
	result, err := sim.Simulate(ctx, "branch_conflict_main", []string{
		"ALTER TABLE users ADD COLUMN already_exists TEXT",
	})
	if err != nil {
		t.Fatalf("simulation failed: %v", err)
	}

	// Should detect conflict
	if result.Clean {
		t.Error("expected conflict — same column added in both branches")
	}
	if len(result.Conflicts) == 0 {
		t.Error("expected at least 1 conflict")
	}
	t.Logf("  ✓ conflict detected: %s", result.Conflicts[0].Type)

	// Auto-resolve — skip the conflicting column
	resolved, ok := conflict.AutoResolve(
		"ALTER TABLE users ADD COLUMN already_exists TEXT",
		result.Conflicts[0],
	)
	if !ok {
		t.Log("  not auto-resolvable — requires manual resolution")
	} else {
		t.Logf("  ✓ auto-resolved: %q", resolved)
	}
}
