package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var urlCmd = &cobra.Command{
	Use:   "url [branch-name]",
	Short: "Output the connection string for a branch",
	RunE:  runURL,
}

func init() {
	rootCmd.AddCommand(urlCmd)
	urlCmd.Flags().String("format", "url", "Output format: url | env | json")
}

func runURL(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	format, _ := cmd.Flags().GetString("format")

	// Determine branch name
	branchName := ""
	if len(args) > 0 {
		branchName = args[0]
	} else {
		branchName = currentGitBranch()
	}
	if branchName == "" {
		return fmt.Errorf("could not detect branch — specify branch name")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer dbManager.Close()

	// Look up branch schema
	var schemaName string
	err = dbManager.Branch.QueryRow(ctx, `
		SELECT schema_name FROM pgdelta.branches
		WHERE name = $1 AND status = 'active'
	`, branchName).Scan(&schemaName)
	if err != nil {
		return fmt.Errorf("branch '%s' not found", branchName)
	}

	// Build connection string with search_path set to branch schema
	// This makes the app connect directly to the branch schema
	branchURL := fmt.Sprintf("%s?search_path=%s,public",
		cfg.BranchDB.URL, schemaName)

	switch format {
	case "env":
		fmt.Printf("DATABASE_URL=%s\n", branchURL)
	case "json":
		fmt.Printf(`{"branch":"%s","schema":"%s","url":"%s"}`,
			branchName, schemaName, branchURL)
		fmt.Println()
	default:
		fmt.Println(branchURL)
	}

	return nil
}
