package snapshot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Status constants
const (
	StatusPending  = "pending"
	StatusLoading  = "loading"
	StatusPaused   = "paused"
	StatusReady    = "ready"
	StatusFailed   = "failed"
)

// Snapshot represents a data snapshot for one table in one branch
type Snapshot struct {
	ID             string
	BranchID       string
	TableName      string
	ExtractionSQL  string
	RowCount       int64
	RowsLoaded     int64
	LastCursor     string
	Status         string
	AISuggested    bool
	AIAccepted     bool
}

// Progress is sent on a channel during loading
type Progress struct {
	TableName  string
	RowsLoaded int64
	RowsTotal  int64
	BytesLoaded int64
	Done       bool
	Error      error
}

// Manager handles all snapshot operations
type Manager struct {
	MainPool   *pgxpool.Pool
	BranchPool *pgxpool.Pool
	ChunkSize  int
}

// New creates a new snapshot manager
func New(mainPool, branchPool *pgxpool.Pool, chunkSize int) *Manager {
	if chunkSize <= 0 {
		chunkSize = 10000
	}
	return &Manager{
		MainPool:   mainPool,
		BranchPool: branchPool,
		ChunkSize:  chunkSize,
	}
}

// Exists checks if a snapshot exists for a table in a branch
func (m *Manager) Exists(ctx context.Context, branchID, tableName string) (bool, error) {
	var exists bool
	err := m.BranchPool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM pgdelta.branch_data_snapshots
			WHERE branch_id = $1 AND table_name = $2
		)
	`, branchID, tableName).Scan(&exists)
	return exists, err
}

// GetStatus returns the current snapshot status for a table
func (m *Manager) GetStatus(ctx context.Context, branchID, tableName string) (*Snapshot, error) {
	var s Snapshot
	err := m.BranchPool.QueryRow(ctx, `
		SELECT id, branch_id, table_name, extraction_sql,
		       row_count, rows_loaded, COALESCE(last_cursor, ''),
		       status, ai_suggested, ai_accepted
		FROM pgdelta.branch_data_snapshots
		WHERE branch_id = $1 AND table_name = $2
	`, branchID, tableName).Scan(
		&s.ID, &s.BranchID, &s.TableName, &s.ExtractionSQL,
		&s.RowCount, &s.RowsLoaded, &s.LastCursor,
		&s.Status, &s.AISuggested, &s.AIAccepted,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Register creates a new snapshot record in pending state
func (m *Manager) Register(ctx context.Context, branchID, tableName, extractionSQL string) (string, error) {
	// Estimate row count from main DB
	rowCount, _ := m.estimateRowCount(ctx, tableName)

	var id string
	err := m.BranchPool.QueryRow(ctx, `
		INSERT INTO pgdelta.branch_data_snapshots
			(branch_id, table_name, extraction_sql, row_count, status, started_at)
		VALUES
			($1, $2, $3, $4, 'loading', now())
		ON CONFLICT (branch_id, table_name)
		DO UPDATE SET
			extraction_sql = EXCLUDED.extraction_sql,
			status = 'loading',
			rows_loaded = 0,
			last_cursor = NULL,
			started_at = now()
		RETURNING id
	`, branchID, tableName, extractionSQL, rowCount).Scan(&id)
	return id, err
}

// Load streams data from main DB into branch schema using chunked COPY
func (m *Manager) Load(
	ctx context.Context,
	snapshotID string,
	branchID string,
	schemaName string,
	tableName string,
	extractionSQL string,
	progress chan<- Progress,
) error {
	// Get columns from main DB
	columns, err := m.getColumns(ctx, tableName)
	if err != nil {
		m.markFailed(ctx, snapshotID)
		return fmt.Errorf("failed to get columns: %w", err)
	}

	// Detect cursor column (primary key for keyset pagination)
	cursorCol, err := m.detectCursorColumn(ctx, tableName)
	if err != nil {
		// Fall back to simple load without cursor
		cursorCol = ""
	}

	// Get resume cursor if any
	resumeCursor := m.getResumeCursor(ctx, snapshotID)

	var totalLoaded int64
	start := time.Now()

	if cursorCol != "" {
		// Chunked load with keyset pagination
		totalLoaded, err = m.chunkedLoad(
			ctx, snapshotID, schemaName, tableName,
			extractionSQL, columns, cursorCol,
			resumeCursor, progress,
		)
	} else {
		// Simple load for tables without a clear PK
		totalLoaded, err = m.simpleLoad(
			ctx, snapshotID, schemaName, tableName,
			extractionSQL, columns, progress,
		)
	}

	if err != nil {
		m.markFailed(ctx, snapshotID)
		return err
	}

	// Mark complete
	_, _ = m.BranchPool.Exec(ctx, `
		UPDATE pgdelta.branch_data_snapshots
		SET status = 'ready', rows_loaded = $1, completed_at = now()
		WHERE id = $2
	`, totalLoaded, snapshotID)

	elapsed := time.Since(start)
	if progress != nil {
		progress <- Progress{
			TableName:  tableName,
			RowsLoaded: totalLoaded,
			Done:       true,
		}
	}

	_ = elapsed
	return nil
}

// chunkedLoad streams data in chunks using keyset pagination
func (m *Manager) chunkedLoad(
	ctx context.Context,
	snapshotID string,
	schemaName string,
	tableName string,
	extractionSQL string,
	columns []string,
	cursorCol string,
	resumeCursor string,
	progress chan<- Progress,
) (int64, error) {
	var totalLoaded int64
	lastCursor := resumeCursor
	targetSchema := fmt.Sprintf("%s.%s", schemaName, tableName)

	for {
		// Build chunk query using keyset pagination
		chunkSQL := buildChunkQuery(extractionSQL, cursorCol, lastCursor, m.ChunkSize)

		// Read chunk from main DB
		rows, err := m.MainPool.Query(ctx, chunkSQL)
		if err != nil {
			return totalLoaded, fmt.Errorf("failed to query chunk: %w", err)
		}

		// Copy chunk to branch DB
		rowsCopied, lastVal, err := m.copyRows(ctx, rows, targetSchema, columns)
		rows.Close()

		if err != nil {
			return totalLoaded, fmt.Errorf("failed to copy chunk: %w", err)
		}

		totalLoaded += rowsCopied

		// Update progress in metadata
		if lastVal != "" {
			_, _ = m.BranchPool.Exec(ctx, `
				UPDATE pgdelta.branch_data_snapshots
				SET rows_loaded = $1, last_cursor = $2
				WHERE id = $3
			`, totalLoaded, lastVal, snapshotID)
		}

		// Send progress update
		if progress != nil {
			progress <- Progress{
				TableName:  tableName,
				RowsLoaded: totalLoaded,
			}
		}

		// If we got fewer rows than chunk size, we're done
		if rowsCopied < int64(m.ChunkSize) {
			break
		}

		lastCursor = lastVal

		// Check for context cancellation (pause support)
		select {
		case <-ctx.Done():
			// Mark as paused
			_, _ = m.BranchPool.Exec(context.Background(), `
				UPDATE pgdelta.branch_data_snapshots
				SET status = 'paused', last_cursor = $1, rows_loaded = $2
				WHERE id = $3
			`, lastCursor, totalLoaded, snapshotID)
			return totalLoaded, ctx.Err()
		default:
		}
	}

	return totalLoaded, nil
}

// simpleLoad loads all rows at once for small tables without a clear PK
func (m *Manager) simpleLoad(
	ctx context.Context,
	snapshotID string,
	schemaName string,
	tableName string,
	extractionSQL string,
	columns []string,
	progress chan<- Progress,
) (int64, error) {
	targetSchema := fmt.Sprintf("%s.%s", schemaName, tableName)

	rows, err := m.MainPool.Query(ctx, extractionSQL)
	if err != nil {
		return 0, fmt.Errorf("failed to query main DB: %w", err)
	}
	defer rows.Close()

	copied, _, err := m.copyRows(ctx, rows, targetSchema, columns)
	if err != nil {
		return 0, err
	}

	if progress != nil {
		progress <- Progress{
			TableName:  tableName,
			RowsLoaded: copied,
		}
	}

	return copied, nil
}

// copyRows uses COPY protocol to bulk insert rows into branch schema
func (m *Manager) copyRows(
	ctx context.Context,
	rows pgx.Rows,
	targetTable string,
	columns []string,
) (int64, string, error) {
	var batch [][]any
	var lastVal string

	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return 0, "", err
		}
		batch = append(batch, vals)

		// Track last value of first column (cursor)
		if len(vals) > 0 && vals[0] != nil {
			lastVal = fmt.Sprintf("%v", vals[0])
		}
	}

	if err := rows.Err(); err != nil {
		return 0, "", err
	}

	if len(batch) == 0 {
		return 0, lastVal, nil
	}

	// Use CopyFrom for maximum performance
	rowCount, err := m.BranchPool.CopyFrom(
		ctx,
		pgx.Identifier(strings.Split(targetTable, ".")),
		columns,
		pgx.CopyFromRows(batch),
	)
	if err != nil {
		return 0, "", fmt.Errorf("COPY failed: %w", err)
	}

	return rowCount, lastVal, nil
}

// getColumns returns column names for a table from main DB
func (m *Manager) getColumns(ctx context.Context, tableName string) ([]string, error) {
	rows, err := m.MainPool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = $1
		ORDER BY ordinal_position
	`, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		columns = append(columns, col)
	}
	return columns, rows.Err()
}

// detectCursorColumn finds the primary key column for keyset pagination
func (m *Manager) detectCursorColumn(ctx context.Context, tableName string) (string, error) {
	var colName string
	err := m.MainPool.QueryRow(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid
			AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass
		  AND i.indisprimary
		LIMIT 1
	`, "public."+tableName).Scan(&colName)
	if err != nil {
		return "", err
	}
	return colName, nil
}

// estimateRowCount gets an approximate row count for a table
func (m *Manager) estimateRowCount(ctx context.Context, tableName string) (int64, error) {
	var count int64
	err := m.MainPool.QueryRow(ctx, `
		SELECT reltuples::BIGINT
		FROM pg_class
		WHERE relname = $1 AND relnamespace = 'public'::regnamespace
	`, tableName).Scan(&count)
	return count, err
}

// getResumeCursor gets the last cursor position for a paused snapshot
func (m *Manager) getResumeCursor(ctx context.Context, snapshotID string) string {
	var cursor string
	_ = m.BranchPool.QueryRow(ctx,
		"SELECT COALESCE(last_cursor, '') FROM pgdelta.branch_data_snapshots WHERE id = $1",
		snapshotID,
	).Scan(&cursor)
	return cursor
}

// markFailed marks a snapshot as failed
func (m *Manager) markFailed(ctx context.Context, snapshotID string) {
	_, _ = m.BranchPool.Exec(ctx,
		"UPDATE pgdelta.branch_data_snapshots SET status = 'failed' WHERE id = $1",
		snapshotID,
	)
}

// buildChunkQuery wraps a user extraction query with keyset pagination
func buildChunkQuery(baseSQL, cursorCol, lastCursor string, chunkSize int) string {
	// Wrap the user's query as a subquery with cursor
	if lastCursor == "" {
		return fmt.Sprintf(`
			SELECT * FROM (%s) AS _pgdelta_base
			ORDER BY %s
			LIMIT %d
		`, baseSQL, cursorCol, chunkSize)
	}
	return fmt.Sprintf(`
		SELECT * FROM (%s) AS _pgdelta_base
		WHERE %s > %s
		ORDER BY %s
		LIMIT %d
	`, baseSQL, cursorCol, lastCursor, cursorCol, chunkSize)
}

// PrintProgress renders a progress bar to stdout
func PrintProgress(tableName string, loaded, total int64, done bool) {
	if total <= 0 {
		fmt.Printf("\r  %-20s  %d rows loaded", tableName, loaded)
		if done {
			fmt.Println()
		}
		return
	}

	pct := float64(loaded) / float64(total)
	barWidth := 30
	filled := int(pct * float64(barWidth))

	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	fmt.Printf("\r  %-15s  [%s]  %d / %d rows  (%.0f%%)",
		tableName, bar, loaded, total, pct*100)

	if done {
		fmt.Printf("  ✓\n")
	}
}

// SyncSequences updates all sequences in a branch schema to match the max id
// Call this after loading snapshot data to prevent PK conflicts
func SyncSequences(ctx context.Context, branchPool *pgxpool.Pool, schemaName string, tables []string) error {
	for _, table := range tables {
		sql := fmt.Sprintf(`
			SELECT setval(
				'%s.%s_id_seq',
				COALESCE((SELECT MAX(id) FROM %s.%s), 0) + 1,
				false
			)
		`, schemaName, table, schemaName, table)

		_, err := branchPool.Exec(ctx, sql)
		if err != nil {
			// Not all tables have id sequences — skip silently
			continue
		}
	}
	return nil
}