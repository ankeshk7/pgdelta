package snapshot_test

import (
	"context"
	"testing"

	"github.com/ankeshkedia/pgdelta/internal/schema"
	"github.com/ankeshkedia/pgdelta/internal/snapshot"
	"github.com/ankeshkedia/pgdelta/internal/testutil"
)

func setupBranchForSnapshot(t *testing.T, tdb *testutil.TestDB, branchName string) (string, string) {
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

func TestLoad_StreamsDataFromMainToBranch(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	branchID, schemaName := setupBranchForSnapshot(t, tdb, "load_test")
	mgr := snapshot.New(tdb.MainPool, tdb.BranchPool, 1000)

	// Register snapshot
	snapshotID, err := mgr.Register(ctx, branchID, "users", "SELECT * FROM users")
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Load data
	err = mgr.Load(ctx, snapshotID, branchID, schemaName, "users", "SELECT * FROM users", nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify 3 rows loaded (from seed data)
	tdb.AssertRowCount(t, schemaName, "users", 3)
}

func TestLoad_RespectsExtractionQuery(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	branchID, schemaName := setupBranchForSnapshot(t, tdb, "filter_test")
	mgr := snapshot.New(tdb.MainPool, tdb.BranchPool, 1000)

	// Only load pro users
	extractionSQL := "SELECT * FROM users WHERE plan = 'pro'"
	snapshotID, err := mgr.Register(ctx, branchID, "users", extractionSQL)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	err = mgr.Load(ctx, snapshotID, branchID, schemaName, "users", extractionSQL, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Only Alice has plan='pro'
	tdb.AssertRowCount(t, schemaName, "users", 1)
}

func TestLoad_MarksStatusReady(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	branchID, schemaName := setupBranchForSnapshot(t, tdb, "status_test")
	mgr := snapshot.New(tdb.MainPool, tdb.BranchPool, 1000)

	snapshotID, err := mgr.Register(ctx, branchID, "users", "SELECT * FROM users")
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	err = mgr.Load(ctx, snapshotID, branchID, schemaName, "users", "SELECT * FROM users", nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	snap, err := mgr.GetStatus(ctx, branchID, "users")
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	if snap.Status != snapshot.StatusReady {
		t.Errorf("expected status ready, got %s", snap.Status)
	}
	if snap.RowsLoaded != 3 {
		t.Errorf("expected 3 rows loaded, got %d", snap.RowsLoaded)
	}
}

func TestLoad_HandlesEmptyResult(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	branchID, schemaName := setupBranchForSnapshot(t, tdb, "empty_test")
	mgr := snapshot.New(tdb.MainPool, tdb.BranchPool, 1000)

	// Query that returns no rows
	extractionSQL := "SELECT * FROM users WHERE plan = 'nonexistent'"
	snapshotID, err := mgr.Register(ctx, branchID, "users", extractionSQL)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	err = mgr.Load(ctx, snapshotID, branchID, schemaName, "users", extractionSQL, nil)
	if err != nil {
		t.Fatalf("Load with empty result failed: %v", err)
	}

	tdb.AssertRowCount(t, schemaName, "users", 0)
}

func TestExists_ReturnsFalseBeforeSnapshot(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	branchID, _ := setupBranchForSnapshot(t, tdb, "exists_test")
	mgr := snapshot.New(tdb.MainPool, tdb.BranchPool, 1000)

	exists, err := mgr.Exists(ctx, branchID, "users")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if exists {
		t.Error("expected exists=false before snapshot")
	}
}

func TestExists_ReturnsTrueAfterSnapshot(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	branchID, schemaName := setupBranchForSnapshot(t, tdb, "exists_after_test")
	mgr := snapshot.New(tdb.MainPool, tdb.BranchPool, 1000)

	snapshotID, _ := mgr.Register(ctx, branchID, "users", "SELECT * FROM users")
	_ = mgr.Load(ctx, snapshotID, branchID, schemaName, "users", "SELECT * FROM users", nil)

	exists, err := mgr.Exists(ctx, branchID, "users")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("expected exists=true after snapshot")
	}
}
