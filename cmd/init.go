package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
	"github.com/ankeshkedia/pgdelta/internal/schema"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize pgDelta in the current repository",
	Long:  `Sets up pgDelta by connecting to your databases, creating the system schema, installing git hooks, and writing .pgdelta.yml.`,
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().String("main-url", "", "Main database URL (read only)")
	initCmd.Flags().String("branch-url", "", "Branch database URL (read/write)")
}

func runInit(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println()
	fmt.Println("  pgDelta — initializing")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()

	// Get main DB URL
	mainURL, _ := cmd.Flags().GetString("main-url")
	if mainURL == "" {
		mainURL = os.Getenv("MAIN_DATABASE_URL")
	}
	if mainURL == "" {
		mainURL = prompt("  Main DB URL (read only):    ")
	}

	// Get branch DB URL
	branchURL, _ := cmd.Flags().GetString("branch-url")
	if branchURL == "" {
		branchURL = os.Getenv("BRANCH_DATABASE_URL")
	}
	if branchURL == "" {
		branchURL = prompt("  Branch DB URL (read/write): ")
	}

	fmt.Println()

	// Step 1 — Test main DB connection
	fmt.Print("  Connecting to main DB...    ")
	dbManager, err := db.New(ctx, mainURL, branchURL)
	if err != nil {
		fmt.Println("✗")
		return fmt.Errorf("\n  Failed: %w\n  Check your DB URLs and try again", err)
	}
	defer dbManager.Close()

	mainVersion, err := dbManager.MainVersion(ctx)
	if err != nil {
		fmt.Println("✗")
		return fmt.Errorf("\n  Failed to query main DB: %w", err)
	}
	fmt.Printf("✓  (%s)\n", shortVersion(mainVersion))

	// Step 2 — Test branch DB connection
	fmt.Print("  Connecting to branch DB...  ")
	branchVersion, err := dbManager.BranchVersion(ctx)
	if err != nil {
		fmt.Println("✗")
		return fmt.Errorf("\n  Failed to query branch DB: %w", err)
	}
	fmt.Printf("✓  (%s)\n", shortVersion(branchVersion))

	// Step 3 — Create pgdelta system schema
	fmt.Print("  Creating pgdelta schema...  ")
	if err := dbManager.EnsurePgDeltaSchema(ctx); err != nil {
		fmt.Println("✗")
		return fmt.Errorf("\n  Failed to create schema: %w", err)
	}
	fmt.Println("✓")

	// Step 3b — Create branch_main schema mirroring main DB
	fmt.Print("  Creating branch_main...     ")
	if err := schema.EnsureMainBranch(ctx, dbManager.Main, dbManager.Branch); err != nil {
		fmt.Println("✗")
		return fmt.Errorf("\n  Failed to create branch_main: %w", err)
	}
	fmt.Println("✓")

	// Step 4 — Check for existing config
	if config.Exists() {
		fmt.Print("  Config already exists...    ")
		fmt.Println("✓  (skipping)")
	} else {
		fmt.Print("  Writing .pgdelta.yml...     ")
		if err := config.WriteDefault(mainURL, branchURL); err != nil {
			fmt.Println("✗")
			return fmt.Errorf("\n  Failed to write config: %w", err)
		}
		fmt.Println("✓")
	}

	// Step 5 — Install git hooks
	fmt.Print("  Installing git hooks...     ")
	if err := installGitHooks(); err != nil {
		fmt.Println("✗  (not a git repo — skipping)")
	} else {
		fmt.Println("✓")
	}

	// Success
	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println("  pgDelta initialized successfully.")
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Println("  1. Review .pgdelta.yml and add snapshot queries for your tables")
	fmt.Println("  2. Commit .pgdelta.yml to share config with your team")
	fmt.Println("  3. Run: git checkout -b <branch-name>")
	fmt.Println()

	return nil
}

// prompt reads a line from stdin
func prompt(label string) string {
	fmt.Print(label)
	var input string
	fmt.Scanln(&input)
	return input
}

// shortVersion extracts just "PostgreSQL 16.x" from the full version string
func shortVersion(full string) string {
	if len(full) > 30 {
		return full[:30]
	}
	return full
}

// installGitHooks writes pgDelta hooks into .git/hooks/
func installGitHooks() error {
	hooks := map[string]string{
		".git/hooks/post-checkout": postCheckoutHook,
		".git/hooks/post-merge":    postMergeHook,
		".git/hooks/post-rewrite":  postRewriteHook,
	}

	// Check .git exists
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		return fmt.Errorf("not a git repository")
	}

	for path, content := range hooks {
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			return fmt.Errorf("failed to write hook %s: %w", path, err)
		}
	}

	return nil
}

const postCheckoutHook = `#!/bin/sh
# pgDelta — auto-manage DB branch on git checkout
PREV_HEAD=$1
NEW_HEAD=$2
IS_BRANCH_CHECKOUT=$3

if [ "$IS_BRANCH_CHECKOUT" = "1" ]; then
  BRANCH=$(git symbolic-ref --short HEAD 2>/dev/null)
  if [ -n "$BRANCH" ]; then
    pgdelta create "$BRANCH" --auto 2>/dev/null || pgdelta switch "$BRANCH" --auto 2>/dev/null
  fi
fi
`

const postMergeHook = `#!/bin/sh
# pgDelta — auto-apply migrations on git merge
BRANCH=$(git symbolic-ref --short HEAD 2>/dev/null)
if [ -n "$BRANCH" ]; then
  pgdelta merge --auto 2>/dev/null
fi
`

const postRewriteHook = `#!/bin/sh
# pgDelta — auto-rebase DB migrations on git rebase
BRANCH=$(git symbolic-ref --short HEAD 2>/dev/null)
if [ -n "$BRANCH" ]; then
  pgdelta rebase --auto 2>/dev/null
fi
`