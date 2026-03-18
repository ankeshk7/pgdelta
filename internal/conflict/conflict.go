package conflict

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	TypeColumnExists     = "column_exists"
	TypeTypeMismatch     = "type_mismatch"
	TypeIndexExists      = "index_exists"
	TypeTableExists      = "table_exists"
	TypeDroppedRef       = "dropped_reference"
	TypeConstraintExists = "constraint_exists"
)

const (
	ResolutionTheirs = "theirs"
	ResolutionOurs   = "ours"
	ResolutionManual = "manual"
)

type Conflict struct {
	MigrationSeq   int
	MigrationSQL   string
	Type           string
	Description    string
	IncomingSQL    string
	CurrentState   string
	Suggestion     string
	AutoResolvable bool
}

type Result struct {
	Clean     bool
	Conflicts []Conflict
	Safe      []string
}

type Simulator struct {
	BranchPool *pgxpool.Pool
}

func New(branchPool *pgxpool.Pool) *Simulator {
	return &Simulator{BranchPool: branchPool}
}

func (s *Simulator) Simulate(
	ctx context.Context,
	parentSchemaName string,
	migrations []string,
) (*Result, error) {
	result := &Result{Clean: true}

	tx, err := s.BranchPool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin simulation: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, fmt.Sprintf("SET search_path TO %s, public", parentSchemaName))
	if err != nil {
		return nil, fmt.Errorf("failed to set search path: %w", err)
	}

	for i, sql := range migrations {
		_, err := tx.Exec(ctx, sql)
		if err != nil {
			c := classifyError(i+1, sql, err.Error())
			result.Conflicts = append(result.Conflicts, c)
			result.Clean = false
			_, _ = tx.Exec(ctx, fmt.Sprintf("SAVEPOINT sp_%d", i))
			continue
		}
		result.Safe = append(result.Safe, sql)
	}

	return result, nil
}

func classifyError(seq int, sql, errMsg string) Conflict {
	c := Conflict{
		MigrationSeq: seq,
		MigrationSQL: sql,
		IncomingSQL:  sql,
		CurrentState: errMsg,
	}

	errLower := strings.ToLower(errMsg)

	switch {
	case strings.Contains(errLower, "already exists") && strings.Contains(errLower, "column"):
		c.Type = TypeColumnExists
		c.Description = "Column already exists in parent schema"
		c.Suggestion = "Remove duplicate column or rename it"
		c.AutoResolvable = true
	case strings.Contains(errLower, "already exists") && strings.Contains(errLower, "relation"):
		c.Type = TypeIndexExists
		c.Description = "Index or table already exists"
		c.Suggestion = "Use CREATE INDEX IF NOT EXISTS"
		c.AutoResolvable = true
	case strings.Contains(errLower, "already exists") && strings.Contains(errLower, "table"):
		c.Type = TypeTableExists
		c.Description = "Table already exists in parent schema"
		c.Suggestion = "Use CREATE TABLE IF NOT EXISTS"
		c.AutoResolvable = true
	case strings.Contains(errLower, "does not exist"):
		c.Type = TypeDroppedRef
		c.Description = "References a column or table that no longer exists"
		c.Suggestion = "Check if parent branch dropped this object"
		c.AutoResolvable = false
	case strings.Contains(errLower, "type") && strings.Contains(errLower, "cannot"):
		c.Type = TypeTypeMismatch
		c.Description = "Type mismatch — cannot cast"
		c.Suggestion = "Add explicit USING clause for type conversion"
		c.AutoResolvable = false
	default:
		c.Type = "unknown"
		c.Description = "Migration failed — manual review required"
		c.Suggestion = errMsg
		c.AutoResolvable = false
	}

	return c
}

func AutoResolve(sql string, c Conflict) (string, bool) {
	switch c.Type {
	case TypeIndexExists:
		resolved := strings.Replace(sql, "CREATE INDEX ", "CREATE INDEX IF NOT EXISTS ", 1)
		if resolved != sql {
			return resolved, true
		}
	case TypeTableExists:
		resolved := strings.Replace(sql, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1)
		if resolved != sql {
			return resolved, true
		}
	case TypeColumnExists:
		return "", true
	}
	return sql, false
}

func Print(conflicts []Conflict) {
	for _, c := range conflicts {
		fmt.Println()
		fmt.Printf("  ✗  CONFLICT at migration #%d\n", c.MigrationSeq)
		fmt.Printf("     Type:        %s\n", c.Type)
		fmt.Printf("     Description: %s\n", c.Description)
		fmt.Println()
		fmt.Printf("     Incoming SQL:\n")
		for _, line := range strings.Split(c.IncomingSQL, "\n") {
			fmt.Printf("       %s\n", strings.TrimSpace(line))
		}
		fmt.Println()
		fmt.Printf("     Error: %s\n", c.CurrentState)
		if c.Suggestion != "" && c.Suggestion != c.CurrentState {
			fmt.Printf("     Suggestion: %s\n", c.Suggestion)
		}
		if c.AutoResolvable {
			fmt.Printf("     ✓ Auto-resolvable\n")
		} else {
			fmt.Printf("     ⚠ Requires manual resolution\n")
		}
	}
}
