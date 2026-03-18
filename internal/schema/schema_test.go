package schema_test

import (
	"context"
	"testing"

	"github.com/ankeshkedia/pgdelta/internal/schema"
	"github.com/ankeshkedia/pgdelta/internal/testutil"
)

func TestSnapshot_ReadsTablesFromMainDB(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	tables, err := schema.Snapshot(ctx, tdb.MainPool)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	if len(tables) != 3 {
		t.Errorf("expected 3 tables, got %d", len(tables))
	}

	// Check expected tables exist
	tableNames := make(map[string]bool)
	for _, table := range tables {
		tableNames[table.Name] = true
	}

	for _, expected := range []string{"users", "orders", "products"} {
		if !tableNames[expected] {
			t.Errorf("expected table %s not found in snapshot", expected)
		}
	}
}

func TestSnapshot_ReadsColumnsCorrectly(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	tables, err := schema.Snapshot(ctx, tdb.MainPool)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	// Find users table
	var usersTable *schema.Table
	for i := range tables {
		if tables[i].Name == "users" {
			usersTable = &tables[i]
			break
		}
	}

	if usersTable == nil {
		t.Fatal("users table not found")
	}

	if len(usersTable.Columns) != 6 {
		t.Errorf("expected 6 columns in users, got %d", len(usersTable.Columns))
	}

	// Check column names
	colNames := make(map[string]bool)
	for _, col := range usersTable.Columns {
		colNames[col.Name] = true
	}

	for _, expected := range []string{"id", "email", "name", "plan", "created_at", "status"} {
		if !colNames[expected] {
			t.Errorf("expected column %s not found", expected)
		}
	}
}

func TestRecreate_CreatesSchemaOnBranchDB(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	tables, err := schema.Snapshot(ctx, tdb.MainPool)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	err = schema.Recreate(ctx, tdb.BranchPool, "branch_test", tables)
	if err != nil {
		t.Fatalf("Recreate failed: %v", err)
	}

	// Verify all tables exist in branch schema
	tdb.AssertTableExists(t, "branch_test", "users")
	tdb.AssertTableExists(t, "branch_test", "orders")
	tdb.AssertTableExists(t, "branch_test", "products")
}

func TestRecreate_PreservesColumnTypes(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	tables, err := schema.Snapshot(ctx, tdb.MainPool)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	err = schema.Recreate(ctx, tdb.BranchPool, "branch_types_test", tables)
	if err != nil {
		t.Fatalf("Recreate failed: %v", err)
	}

	// Verify we can insert data with correct types
	_, err = tdb.BranchPool.Exec(ctx, `
		INSERT INTO branch_types_test.users (email, name, plan)
		VALUES ('test@test.com', 'Test', 'free')
	`)
	if err != nil {
		t.Errorf("failed to insert into recreated table: %v", err)
	}
}

func TestRecreate_HandlesTablesWithForeignKeys(t *testing.T) {
	tdb := testutil.Setup(t)
	ctx := context.Background()

	tables, err := schema.Snapshot(ctx, tdb.MainPool)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	// Should not fail even though orders has FK to users
	err = schema.Recreate(ctx, tdb.BranchPool, "branch_fk_test", tables)
	if err != nil {
		t.Fatalf("Recreate with FK tables failed: %v", err)
	}

	tdb.AssertTableExists(t, "branch_fk_test", "users")
	tdb.AssertTableExists(t, "branch_fk_test", "orders")
}
