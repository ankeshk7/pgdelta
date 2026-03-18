package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Manager struct {
	Main   *pgxpool.Pool
	Branch *pgxpool.Pool
}

func New(ctx context.Context, mainURL, branchURL string) (*Manager, error) {
	mainPool, err := connect(ctx, mainURL)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to main DB: %w", err)
	}
	if err := mainPool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("main DB ping failed: %w", err)
	}

	branchPool, err := connect(ctx, branchURL)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to branch DB: %w", err)
	}
	if err := branchPool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("branch DB ping failed: %w", err)
	}

	return &Manager{Main: mainPool, Branch: branchPool}, nil
}

func connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("invalid connection URL: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}
	return pool, nil
}

func (m *Manager) Close() {
	if m.Main != nil {
		m.Main.Close()
	}
	if m.Branch != nil {
		m.Branch.Close()
	}
}

func (m *Manager) MainVersion(ctx context.Context) (string, error) {
	var version string
	err := m.Main.QueryRow(ctx, "SELECT version()").Scan(&version)
	if err != nil {
		return "", fmt.Errorf("failed to get main DB version: %w", err)
	}
	return version, nil
}

func (m *Manager) BranchVersion(ctx context.Context) (string, error) {
	var version string
	err := m.Branch.QueryRow(ctx, "SELECT version()").Scan(&version)
	if err != nil {
		return "", fmt.Errorf("failed to get branch DB version: %w", err)
	}
	return version, nil
}

func (m *Manager) EnsurePgDeltaSchema(ctx context.Context) error {
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS pgdelta`,

		`CREATE TABLE IF NOT EXISTS pgdelta.branches (
			id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name              TEXT UNIQUE NOT NULL,
			git_branch        TEXT NOT NULL,
			parent_branch     TEXT NOT NULL DEFAULT 'main',
			schema_name       TEXT NOT NULL,
			branched_at_seq   INT NOT NULL DEFAULT 0,
			status            TEXT NOT NULL DEFAULT 'active',
			created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
			merged_at         TIMESTAMPTZ,
			deleted_at        TIMESTAMPTZ
		)`,

		`CREATE TABLE IF NOT EXISTS pgdelta.branch_migrations (
			id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			branch_id     UUID NOT NULL REFERENCES pgdelta.branches(id),
			sequence      INT NOT NULL,
			sql           TEXT NOT NULL,
			type          TEXT NOT NULL DEFAULT 'ddl',
			description   TEXT,
			checksum      TEXT NOT NULL,
			applied_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE(branch_id, sequence)
		)`,

		`CREATE TABLE IF NOT EXISTS pgdelta.branch_data_snapshots (
			id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			branch_id         UUID NOT NULL REFERENCES pgdelta.branches(id),
			table_name        TEXT NOT NULL,
			extraction_sql    TEXT NOT NULL,
			row_count         BIGINT NOT NULL DEFAULT 0,
			rows_loaded       BIGINT NOT NULL DEFAULT 0,
			last_cursor       TEXT,
			status            TEXT NOT NULL DEFAULT 'pending',
			ai_suggested      BOOLEAN NOT NULL DEFAULT false,
			ai_accepted       BOOLEAN NOT NULL DEFAULT false,
			started_at        TIMESTAMPTZ,
			completed_at      TIMESTAMPTZ,
			UNIQUE(branch_id, table_name)
		)`,

		`CREATE TABLE IF NOT EXISTS pgdelta.conflict_log (
			id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			branch_id         UUID NOT NULL REFERENCES pgdelta.branches(id),
			migration_id      UUID REFERENCES pgdelta.branch_migrations(id),
			conflict_type     TEXT NOT NULL,
			incoming_sql      TEXT NOT NULL,
			current_state     TEXT NOT NULL,
			resolution        TEXT,
			resolved_sql      TEXT,
			ai_suggested      BOOLEAN NOT NULL DEFAULT false,
			resolved_at       TIMESTAMPTZ
		)`,

		`CREATE TABLE IF NOT EXISTS pgdelta.telemetry (
			id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			event           TEXT NOT NULL,
			org_id          UUID,
			schema_hash     TEXT,
			table_count     INT,
			row_estimate    BIGINT,
			payload         JSONB,
			ai_suggested    BOOLEAN,
			ai_accepted     BOOLEAN,
			duration_ms     INT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	}

	for _, stmt := range statements {
		if _, err := m.Branch.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("failed to create pgdelta schema: %w\nSQL: %s", err, stmt)
		}
	}

	// Register main branch in metadata if not already there
	_, err := m.Branch.Exec(ctx, `
		INSERT INTO pgdelta.branches
			(name, git_branch, parent_branch, schema_name, branched_at_seq, status)
		VALUES
			('main', 'main', 'main', 'branch_main', 0, 'active')
		ON CONFLICT (name) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("failed to register main branch: %w", err)
	}

	return nil
}
