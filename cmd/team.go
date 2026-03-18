package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var teamCmd = &cobra.Command{
	Use:   "team",
	Short: "Team configuration and management",
}

var teamInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate team-ready .pgdelta.yml with shared snapshot config",
	RunE:  runTeamInit,
}

var teamStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show team branch activity overview",
	RunE:  runTeamStatus,
}

func init() {
	rootCmd.AddCommand(teamCmd)
	teamCmd.AddCommand(teamInitCmd)
	teamCmd.AddCommand(teamStatusCmd)
	teamInitCmd.Flags().String("main-url", "", "Main DB URL (uses env var if not set)")
	teamInitCmd.Flags().String("branch-url", "", "Branch DB URL (uses env var if not set)")
}

func runTeamInit(cmd *cobra.Command, args []string) error {
	fmt.Println()
	fmt.Println("  pgDelta — team init")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Println("  Generating team-ready configuration...")
	fmt.Println()

	teamConfig := `version: 1

# ─────────────────────────────────────────────────────
# pgDelta Team Configuration
# Commit this file to share DB branch config with team
# Never commit credentials — use environment variables
# ─────────────────────────────────────────────────────

# Main database — read only source of truth
# Set MAIN_DATABASE_URL in your environment or CI secrets
main:
  url: ${MAIN_DATABASE_URL}
  access: readonly

# Branch database — shared team instance
# Set BRANCH_DATABASE_URL in your environment or CI secrets
branch_db:
  url: ${BRANCH_DATABASE_URL}
  access: readwrite

# Branching — mirrors git topology automatically
branching:
  schema_from: git-parent
  auto_create: true
  auto_cleanup: true
  auto_merge: true

# Snapshot queries — shared across the whole team
# Run 'pgdelta configure' to auto-generate these from your schema
# Customize per table as needed
snapshots:
  default_row_limit: 5000
  chunk_size: 10000
  parallel_tables: 3
  resume_on_interrupt: true
  tables: {}
  exclude:
    - audit_logs
    - sessions
    - password_reset_tokens
    - user_tokens
    - api_keys

# PII masking — protects sensitive data in branches
pii:
  auto_detect: true
  masks: {}

# AI features — each team member uses their own key
# Or set a shared key in CI via ANTHROPIC_API_KEY secret
ai:
  enabled: true
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}
  features:
    extraction_suggestions: true
    migration_risk: true
    conflict_resolution: true
    pii_detection: true

# Git hooks — auto-installed for each team member on clone
git:
  hooks: true
  status_line: true

# Protected branches — prevent accidental merges
# Add branches here that require pgdelta merge (not git merge)
# Use: pgdelta protect add main
protect:
  - main
  - production

# Anonymous telemetry — helps improve pgDelta
telemetry:
  enabled: true
  anonymous: true
`

	if err := os.WriteFile(".pgdelta.yml", []byte(teamConfig), 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Println("  ✓  .pgdelta.yml written")
	fmt.Println()
	fmt.Println("  Team onboarding steps:")
	fmt.Println()
	fmt.Println("  1. Commit .pgdelta.yml to your repo")
	fmt.Println("     git add .pgdelta.yml")
	fmt.Println("     git commit -m 'chore: add pgDelta team config'")
	fmt.Println()
	fmt.Println("  2. Each team member sets environment variables:")
	fmt.Println("     export MAIN_DATABASE_URL='postgres://...'")
	fmt.Println("     export BRANCH_DATABASE_URL='postgres://...'")
	fmt.Println("     export ANTHROPIC_API_KEY='sk-ant-...'")
	fmt.Println()
	fmt.Println("  3. Each team member runs:")
	fmt.Println("     pgdelta init")
	fmt.Println("     pgdelta configure")
	fmt.Println()
	fmt.Println("  4. Add CI secrets to GitHub:")
	fmt.Println("     MAIN_DATABASE_URL")
	fmt.Println("     BRANCH_DATABASE_URL")
	fmt.Println("     ANTHROPIC_API_KEY (optional)")
	fmt.Println()
	fmt.Println("  5. Generate CI workflow:")
	fmt.Println("     pgdelta ci github")
	fmt.Println()

	return nil
}

func runTeamStatus(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	rows, err := dbManager.Branch.Query(ctx, `
		SELECT
			b.name,
			b.parent_branch,
			b.status,
			b.created_at,
			COUNT(DISTINCT m.id) AS mig_count,
			COUNT(DISTINCT s.id) AS snap_count
		FROM pgdelta.branches b
		LEFT JOIN pgdelta.branch_migrations m ON m.branch_id = b.id
		LEFT JOIN pgdelta.branch_data_snapshots s
			ON s.branch_id = b.id AND s.status = 'ready'
		WHERE b.status = 'active'
		GROUP BY b.id, b.name, b.parent_branch, b.status, b.created_at
		ORDER BY b.created_at DESC
	`)
	if err != nil {
		return fmt.Errorf("failed to query branches: %w", err)
	}
	defer rows.Close()

	fmt.Println()
	fmt.Println("  pgDelta — team status")
	fmt.Println("  ─────────────────────────────────────────────────────────")
	fmt.Printf("  %-25s  %-15s  %-10s  %-8s  %s\n",
		"BRANCH", "PARENT", "MIGRATIONS", "SNAPSHOTS", "AGE")
	fmt.Println("  ─────────────────────────────────────────────────────────")

	count := 0
	for rows.Next() {
		var name, parent, status string
		var createdAt time.Time
		var migCount, snapCount int
		rows.Scan(&name, &parent, &status, &createdAt, &migCount, &snapCount)

		icon := "⑂"
		if status == "merged" {
			icon = "✓"
		}

		fmt.Printf("  %s %-23s  %-15s  %-10d  %-8d  %s\n",
			icon, name, parent, migCount, snapCount,
			formatAge(time.Since(createdAt)))
		count++
	}

	if count == 0 {
		fmt.Println("  No active branches.")
	}

	fmt.Println()
	fmt.Printf("  %d active branch(es)\n", count)
	fmt.Println()
	return nil
}
