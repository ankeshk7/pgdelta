package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestDB holds two real Postgres instances for testing
type TestDB struct {
	MainPool   *pgxpool.Pool
	BranchPool *pgxpool.Pool
	Manager    *db.Manager
	MainURL    string
	BranchURL  string
	cleanup    func()
}

// Setup spins up two real Postgres containers for testing
func Setup(t *testing.T) *TestDB {
	t.Helper()
	ctx := context.Background()

	// Start main DB container
	mainContainer, err := postgres.Run(ctx,
		"postgres:16",
		postgres.WithDatabase("maindb"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("failed to start main DB container: %v", err)
	}

	// Start branch DB container
	branchContainer, err := postgres.Run(ctx,
		"postgres:16",
		postgres.WithDatabase("branchdb"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		mainContainer.Terminate(ctx)
		t.Fatalf("failed to start branch DB container: %v", err)
	}

	// Get connection strings
	mainURL, err := mainContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get main DB URL: %v", err)
	}

	branchURL, err := branchContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get branch DB URL: %v", err)
	}

	// Create DB manager
	manager, err := db.New(ctx, mainURL, branchURL)
	if err != nil {
		t.Fatalf("failed to create DB manager: %v", err)
	}

	// Initialize pgdelta schema on branch DB
	if err := manager.EnsurePgDeltaSchema(ctx); err != nil {
		t.Fatalf("failed to create pgdelta schema: %v", err)
	}

	// Seed main DB with test tables
	if err := seedMainDB(ctx, manager.Main); err != nil {
		t.Fatalf("failed to seed main DB: %v", err)
	}

	cleanup := func() {
		manager.Close()
		mainContainer.Terminate(ctx)
		branchContainer.Terminate(ctx)
	}

	t.Cleanup(cleanup)

	return &TestDB{
		MainPool:   manager.Main,
		BranchPool: manager.Branch,
		Manager:    manager,
		MainURL:    mainURL,
		BranchURL:  branchURL,
		cleanup:    cleanup,
	}
}

// seedMainDB creates realistic test tables on main DB
func seedMainDB(ctx context.Context, pool *pgxpool.Pool) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id         SERIAL PRIMARY KEY,
			email      TEXT UNIQUE NOT NULL,
			name       TEXT NOT NULL,
			plan       TEXT NOT NULL DEFAULT 'free',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			status     TEXT NOT NULL DEFAULT 'active'
		)`,
		`CREATE TABLE IF NOT EXISTS orders (
			id         SERIAL PRIMARY KEY,
			user_id    INT NOT NULL REFERENCES users(id),
			amount     DECIMAL(10,2) NOT NULL,
			status     TEXT NOT NULL DEFAULT 'pending',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS products (
			id         SERIAL PRIMARY KEY,
			name       TEXT NOT NULL,
			price      DECIMAL(10,2) NOT NULL,
			stock      INT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`INSERT INTO users (email, name, plan) VALUES
			('alice@example.com', 'Alice', 'pro'),
			('bob@example.com', 'Bob', 'free'),
			('charlie@example.com', 'Charlie', 'enterprise')`,
		`INSERT INTO products (name, price, stock) VALUES
			('Widget A', 9.99, 100),
			('Widget B', 19.99, 50)`,
		`INSERT INTO orders (user_id, amount, status) VALUES
			(1, 9.99, 'completed'),
			(3, 19.99, 'pending')`,
	}

	for _, stmt := range statements {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("failed to seed: %w\nSQL: %s", err, stmt)
		}
	}

	return nil
}

// CreateBranch is a test helper that creates a branch and returns its ID
func (tdb *TestDB) CreateBranch(t *testing.T, name, parent string) string {
	t.Helper()
	ctx := context.Background()

	schemaName := "branch_" + name
	var id string
	err := tdb.BranchPool.QueryRow(ctx, `
		INSERT INTO pgdelta.branches
			(name, git_branch, parent_branch, schema_name, branched_at_seq, status)
		VALUES ($1, $2, $3, $4, 0, 'active')
		RETURNING id
	`, name, name, parent, schemaName).Scan(&id)
	if err != nil {
		t.Fatalf("failed to create test branch: %v", err)
	}

	return id
}

// AssertTableExists checks a table exists in a schema
func (tdb *TestDB) AssertTableExists(t *testing.T, schema, table string) {
	t.Helper()
	ctx := context.Background()

	var exists bool
	err := tdb.BranchPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = $1 AND table_name = $2
		)
	`, schema, table).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to check table existence: %v", err)
	}
	if !exists {
		t.Errorf("expected table %s.%s to exist but it doesn't", schema, table)
	}
}

// AssertRowCount checks the number of rows in a table
func (tdb *TestDB) AssertRowCount(t *testing.T, schema, table string, expected int) {
	t.Helper()
	ctx := context.Background()

	var count int
	err := tdb.BranchPool.QueryRow(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s.%s", schema, table),
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count rows in %s.%s: %v", schema, table, err)
	}
	if count != expected {
		t.Errorf("expected %d rows in %s.%s but got %d", expected, schema, table, count)
	}
}

// AssertColumnExists checks a column exists in a table
func (tdb *TestDB) AssertColumnExists(t *testing.T, schema, table, column string) {
	t.Helper()
	ctx := context.Background()

	var exists bool
	err := tdb.BranchPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = $1
			  AND table_name = $2
			  AND column_name = $3
		)
	`, schema, table, column).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to check column existence: %v", err)
	}
	if !exists {
		t.Errorf("expected column %s.%s.%s to exist", schema, table, column)
	}
}

// AssertMigrationCount checks migration count for a branch
func (tdb *TestDB) AssertMigrationCount(t *testing.T, branchID string, expected int) {
	t.Helper()
	ctx := context.Background()

	var count int
	err := tdb.BranchPool.QueryRow(ctx,
		"SELECT COUNT(*) FROM pgdelta.branch_migrations WHERE branch_id = $1",
		branchID,
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count migrations: %v", err)
	}
	if count != expected {
		t.Errorf("expected %d migrations but got %d", expected, count)
	}
}
