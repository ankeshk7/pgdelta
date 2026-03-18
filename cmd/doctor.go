package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check pgDelta setup and diagnose issues",
	RunE:  runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().Bool("fix", false, "Attempt to fix issues automatically")
}

type check struct {
	name    string
	status  string // ok | warn | fail
	message string
	fix     string // how to fix if failed
}

func runDoctor(cmd *cobra.Command, args []string) error {
	autoFix, _ := cmd.Flags().GetBool("fix")

	fmt.Println()
	fmt.Println("  pgDelta — doctor")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()

	var checks []check

	// ── Check 1: Config file ─────────────────────────────────────────
	if config.Exists() {
		checks = append(checks, check{
			name:    ".pgdelta.yml",
			status:  "ok",
			message: "config file found",
		})
	} else {
		checks = append(checks, check{
			name:    ".pgdelta.yml",
			status:  "fail",
			message: "config file not found",
			fix:     "pgdelta init --main-url <url> --branch-url <url>",
		})
	}

	// ── Check 2: Git repository ──────────────────────────────────────
	if _, err := os.Stat(".git"); err == nil {
		checks = append(checks, check{
			name:    "git repository",
			status:  "ok",
			message: "git repo detected",
		})
	} else {
		checks = append(checks, check{
			name:    "git repository",
			status:  "warn",
			message: "not a git repository — hooks won't work",
			fix:     "git init",
		})
	}

	// ── Check 3: Git hooks ───────────────────────────────────────────
	hooks := []string{
		".git/hooks/post-checkout",
		".git/hooks/post-merge",
		".git/hooks/post-rewrite",
	}
	allHooks := true
	for _, hook := range hooks {
		if _, err := os.Stat(hook); err != nil {
			allHooks = false
			break
		}
	}
	if allHooks {
		checks = append(checks, check{
			name:    "git hooks",
			status:  "ok",
			message: "post-checkout, post-merge, post-rewrite installed",
		})
	} else {
		checks = append(checks, check{
			name:    "git hooks",
			status:  "warn",
			message: "one or more hooks missing",
			fix:     "pgdelta init (re-run to reinstall hooks)",
		})
		if autoFix {
			installGitHooks()
		}
	}

	// ── Check 4 + 5: DB connections ──────────────────────────────────
	cfg, cfgErr := config.Load()
	if cfgErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Main DB
		dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
		if err != nil {
			if strings.Contains(err.Error(), "main") {
				checks = append(checks, check{
					name:    "main DB connection",
					status:  "fail",
					message: err.Error(),
					fix:     "check MAIN_DATABASE_URL in .pgdelta.yml",
				})
			} else {
				checks = append(checks, check{
					name:    "main DB connection",
					status:  "ok",
					message: "connected",
				})
				checks = append(checks, check{
					name:    "branch DB connection",
					status:  "fail",
					message: err.Error(),
					fix:     "check BRANCH_DATABASE_URL in .pgdelta.yml",
				})
			}
		} else {
			defer dbManager.Close()

			mainVer, _ := dbManager.MainVersion(ctx)
			checks = append(checks, check{
				name:    "main DB connection",
				status:  "ok",
				message: shortVersion(mainVer),
			})

			branchVer, _ := dbManager.BranchVersion(ctx)
			checks = append(checks, check{
				name:    "branch DB connection",
				status:  "ok",
				message: shortVersion(branchVer),
			})

			// Check pgdelta schema exists
			var schemaExists bool
			_ = dbManager.Branch.QueryRow(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM information_schema.schemata
					WHERE schema_name = 'pgdelta'
				)
			`).Scan(&schemaExists)

			if schemaExists {
				checks = append(checks, check{
					name:    "pgdelta schema",
					status:  "ok",
					message: "system tables present on branch DB",
				})
			} else {
				checks = append(checks, check{
					name:    "pgdelta schema",
					status:  "fail",
					message: "pgdelta schema missing on branch DB",
					fix:     "pgdelta init (re-run to recreate schema)",
				})
				if autoFix {
					_ = dbManager.EnsurePgDeltaSchema(ctx)
				}
			}

			// Check branch_main exists
			var branchMainExists bool
			_ = dbManager.Branch.QueryRow(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM information_schema.schemata
					WHERE schema_name = 'branch_main'
				)
			`).Scan(&branchMainExists)

			if branchMainExists {
				checks = append(checks, check{
					name:    "branch_main schema",
					status:  "ok",
					message: "base schema exists",
				})
			} else {
				checks = append(checks, check{
					name:    "branch_main schema",
					status:  "warn",
					message: "branch_main schema missing",
					fix:     "pgdelta init (re-run to recreate)",
				})
			}

			// Count active branches
			var branchCount int
			_ = dbManager.Branch.QueryRow(ctx,
				"SELECT COUNT(*) FROM pgdelta.branches WHERE status = 'active'",
			).Scan(&branchCount)

			checks = append(checks, check{
				name:    "active branches",
				status:  "ok",
				message: fmt.Sprintf("%d active branches", branchCount),
			})
		}
	} else {
		checks = append(checks, check{
			name:    "DB connections",
			status:  "fail",
			message: "cannot check — config not loaded",
			fix:     "pgdelta init first",
		})
	}

	// ── Check 6: AI configuration ─────────────────────────────────────
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey != "" {
		checks = append(checks, check{
			name:    "AI (Anthropic)",
			status:  "ok",
			message: "ANTHROPIC_API_KEY set",
		})
	} else {
		checks = append(checks, check{
			name:    "AI (Anthropic)",
			status:  "warn",
			message: "ANTHROPIC_API_KEY not set — AI features disabled",
			fix:     "export ANTHROPIC_API_KEY=sk-ant-...",
		})
	}

	// ── Check 7: pgdelta binary in PATH ───────────────────────────────
	if _, err := exec.LookPath("pgdelta"); err == nil {
		checks = append(checks, check{
			name:    "pgdelta in PATH",
			status:  "ok",
			message: "binary accessible",
		})
	} else {
		checks = append(checks, check{
			name:    "pgdelta in PATH",
			status:  "warn",
			message: "pgdelta not found in PATH — git hooks won't work",
			fix:     "add pgdelta to /usr/local/bin or update PATH",
		})
	}

	// ── Print results ─────────────────────────────────────────────────
	okCount := 0
	warnCount := 0
	failCount := 0

	for _, c := range checks {
		icon := "✓"
		switch c.status {
		case "warn":
			icon = "⚠"
			warnCount++
		case "fail":
			icon = "✗"
			failCount++
		default:
			okCount++
		}

		fmt.Printf("  %s  %-25s  %s\n", icon, c.name, c.message)

		if c.status != "ok" && c.fix != "" {
			fmt.Printf("     └─ fix: %s\n", c.fix)
		}
	}

	// ── Summary ───────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Printf("  %d passed · %d warnings · %d failed\n",
		okCount, warnCount, failCount)
	fmt.Println()

	if failCount > 0 {
		fmt.Println("  ✗  pgDelta is not fully configured. Fix the issues above.")
		if !autoFix {
			fmt.Println("     Run with --fix to attempt automatic fixes.")
		}
	} else if warnCount > 0 {
		fmt.Println("  ⚠  pgDelta is working but some features may be limited.")
	} else {
		fmt.Println("  ✓  pgDelta is fully configured and ready.")
	}
	fmt.Println()

	return nil
}
