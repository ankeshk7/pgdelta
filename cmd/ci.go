package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var ciCmd = &cobra.Command{
	Use:   "ci",
	Short: "Generate CI/CD integration files",
}

var ciGithubCmd = &cobra.Command{
	Use:   "github",
	Short: "Generate GitHub Actions workflow for per-PR database environments",
	RunE:  runCIGithub,
}

func init() {
	rootCmd.AddCommand(ciCmd)
	ciCmd.AddCommand(ciGithubCmd)
	ciGithubCmd.Flags().String("out", ".github/workflows/pgdelta.yml",
		"Output path for workflow file")
	ciGithubCmd.Flags().String("test-cmd", "go test ./...",
		"Command to run tests")
	ciGithubCmd.Flags().String("migrate-cmd", "",
		"Migration command to run after branch create (optional)")
}

func runCIGithub(cmd *cobra.Command, args []string) error {
	outPath, _ := cmd.Flags().GetString("out")
	testCmd, _ := cmd.Flags().GetString("test-cmd")
	migrateCmd, _ := cmd.Flags().GetString("migrate-cmd")

	migrateStep := ""
	if migrateCmd != "" {
		migrateStep = fmt.Sprintf(`
      - name: Run migrations
        run: %s
        env:
          DATABASE_URL: ${{ steps.db-url.outputs.url }}
`, migrateCmd)
	}

	workflow := fmt.Sprintf(`name: pgDelta — PR Database Environment

on:
  pull_request:
    types: [opened, synchronize, reopened]
  pull_request_target:
    types: [closed]

env:
  PGDELTA_BRANCH: pr-${{ github.event.pull_request.number }}

jobs:
  # Create isolated DB branch for this PR
  setup-db-branch:
    if: github.event.action != 'closed'
    runs-on: ubuntu-latest
    outputs:
      db-url: ${{ steps.db-url.outputs.url }}

    steps:
      - uses: actions/checkout@v4

      - name: Install pgDelta
        run: |
          curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_Linux_x86_64.tar.gz | tar xz
          sudo mv pgdelta /usr/local/bin/

      - name: Create DB branch for PR
        run: pgdelta create $PGDELTA_BRANCH
        env:
          MAIN_DATABASE_URL: ${{ secrets.MAIN_DATABASE_URL }}
          BRANCH_DATABASE_URL: ${{ secrets.BRANCH_DATABASE_URL }}
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}

      - name: Snapshot configured tables
        run: pgdelta snapshot --all --branch $PGDELTA_BRANCH
        env:
          MAIN_DATABASE_URL: ${{ secrets.MAIN_DATABASE_URL }}
          BRANCH_DATABASE_URL: ${{ secrets.BRANCH_DATABASE_URL }}
%s
      - name: Get branch connection string
        id: db-url
        run: |
          URL=$(pgdelta url $PGDELTA_BRANCH)
          echo "url=$URL" >> $GITHUB_OUTPUT
        env:
          MAIN_DATABASE_URL: ${{ secrets.MAIN_DATABASE_URL }}
          BRANCH_DATABASE_URL: ${{ secrets.BRANCH_DATABASE_URL }}

      - name: Run tests against branch DB
        run: %s
        env:
          DATABASE_URL: ${{ steps.db-url.outputs.url }}

  # Clean up DB branch when PR is closed
  cleanup-db-branch:
    if: github.event.action == 'closed'
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4

      - name: Install pgDelta
        run: |
          curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_Linux_x86_64.tar.gz | tar xz
          sudo mv pgdelta /usr/local/bin/

      - name: Delete DB branch
        run: pgdelta delete $PGDELTA_BRANCH --force
        env:
          MAIN_DATABASE_URL: ${{ secrets.MAIN_DATABASE_URL }}
          BRANCH_DATABASE_URL: ${{ secrets.BRANCH_DATABASE_URL }}
`, migrateStep, testCmd)

	// Create directory if needed
	if err := os.MkdirAll(".github/workflows", 0755); err != nil {
		return fmt.Errorf("failed to create .github/workflows: %w", err)
	}

	if err := os.WriteFile(outPath, []byte(workflow), 0644); err != nil {
		return fmt.Errorf("failed to write workflow: %w", err)
	}

	fmt.Println()
	fmt.Println("  pgDelta — GitHub Actions")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  Workflow written to: %s\n", outPath)
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Println("  1. Add these secrets to your GitHub repo:")
	fmt.Println("     MAIN_DATABASE_URL   → your main DB connection string")
	fmt.Println("     BRANCH_DATABASE_URL → your branch DB connection string")
	fmt.Println("     ANTHROPIC_API_KEY   → your Anthropic API key (optional)")
	fmt.Println()
	fmt.Println("  2. Commit the workflow file:")
	fmt.Println("     git add .github/workflows/pgdelta.yml")
	fmt.Println("     git commit -m \"ci: add pgDelta PR database environments\"")
	fmt.Println()
	fmt.Println("  Every PR will now get its own isolated database branch.")
	fmt.Println()

	return nil
}
