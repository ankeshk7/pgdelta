package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	apiURL    = "https://api.anthropic.com/v1/messages"
	model     = "claude-haiku-4-5-20251001"
	maxTokens = 1024
)

// Client is the AI client
type Client struct {
	apiKey     string
	httpClient *http.Client
	enabled    bool
}

// New creates a new AI client
func New() *Client {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	return &Client{
		apiKey:     apiKey,
		enabled:    apiKey != "",
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// IsEnabled returns true if AI is configured
func (c *Client) IsEnabled() bool {
	return c.enabled
}

// request sends a message to Claude and returns the text response
func (c *Client) request(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if !c.enabled {
		return "", fmt.Errorf("AI not configured — set ANTHROPIC_API_KEY")
	}

	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     systemPrompt,
		"messages": []map[string]any{
			{"role": "user", "content": userPrompt},
		},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBytes))
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.Unmarshal(respBytes, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	var texts []string
	for _, c := range result.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}

	return strings.Join(texts, "\n"), nil
}

// SuggestExtractionQuery suggests an extraction query for a table
// based on branch name and schema context
func (c *Client) SuggestExtractionQuery(
	ctx context.Context,
	branchName string,
	tableName string,
	columns []string,
	rowEstimate int64,
) (string, error) {
	system := `You are a PostgreSQL expert helping developers snapshot production data safely.
Generate a valid PostgreSQL SELECT query to extract a useful subset of data.
IMPORTANT: Use only valid PostgreSQL syntax.
- Intervals must be: INTERVAL '30 days' not INTERVAL 30 DAY
- Always include a LIMIT clause
- Never use semicolons at the end
- No markdown, no backticks, just the raw SQL query`

	user := fmt.Sprintf(`Branch name: %s
Table name: %s
Columns: %s
Estimated total rows: %d

Suggest an extraction query that gets a useful subset of data for this branch context.
Keep it under 10000 rows. Consider the branch name as a hint about what data is relevant.`,
		branchName,
		tableName,
		strings.Join(columns, ", "),
		rowEstimate,
	)

	query, err := c.request(ctx, system, user)
	if err != nil {
		return "", err
	}

	query = strings.TrimSuffix(strings.TrimSpace(query), ";")

	return strings.TrimSpace(query), nil
}

// AnalyzeMigrationRisk checks a migration for potential issues
func (c *Client) AnalyzeMigrationRisk(
	ctx context.Context,
	sql string,
	tableColumns map[string][]string,
) (string, bool, error) {
	system := `You are a database safety expert reviewing migrations before they run in production.
Identify risks like: dropping columns that might be in use, type changes that could lose data,
missing indexes on foreign keys, lock-heavy operations on large tables.
Be concise. If safe, say "SAFE". If risky, start with "WARNING:" and explain briefly.
Respond in plain text, no markdown.`

	// Build schema context
	var schemaCtx strings.Builder
	for table, cols := range tableColumns {
		schemaCtx.WriteString(fmt.Sprintf("Table %s: %s\n", table, strings.Join(cols, ", ")))
	}

	user := fmt.Sprintf(`Migration SQL:
%s

Current schema context:
%s

Is this migration safe to run?`, sql, schemaCtx.String())

	analysis, err := c.request(ctx, system, user)
	if err != nil {
		return "", false, err
	}

	analysis = strings.TrimSpace(analysis)
	isRisky := strings.HasPrefix(strings.ToUpper(analysis), "WARNING")

	return analysis, isRisky, nil
}

// DetectPII scans column names and types for likely PII
func (c *Client) DetectPII(
	ctx context.Context,
	tableName string,
	columns []string,
) ([]string, error) {
	system := `You are a data privacy expert identifying PII columns in database schemas.
Look for columns that likely contain: email addresses, phone numbers, SSNs, passwords,
credit card numbers, dates of birth, physical addresses, names, IP addresses.
Respond with ONLY a JSON array of column names that are likely PII.
Example: ["email", "phone", "ssn"]
If none found, respond with: []`

	user := fmt.Sprintf(`Table: %s
Columns: %s

Which columns likely contain PII?`,
		tableName,
		strings.Join(columns, ", "),
	)

	response, err := c.request(ctx, system, user)
	if err != nil {
		return nil, err
	}

	response = strings.TrimSpace(response)

	var piiColumns []string
	if err := json.Unmarshal([]byte(response), &piiColumns); err != nil {
		return nil, fmt.Errorf("failed to parse PII response: %w", err)
	}

	return piiColumns, nil
}

// ExplainConflict explains a merge conflict in plain English
func (c *Client) ExplainConflict(
	ctx context.Context,
	conflictType string,
	incomingSQL string,
	errorMessage string,
) (string, error) {
	system := `You are a database expert helping developers resolve merge conflicts.
Explain the conflict clearly and suggest how to fix it.
Be concise — 2-3 sentences maximum. No markdown.`

	user := fmt.Sprintf(`Conflict type: %s
Migration that failed: %s
Error: %s

Explain what went wrong and how to fix it.`,
		conflictType,
		incomingSQL,
		errorMessage,
	)

	explanation, err := c.request(ctx, system, user)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(explanation), nil
}

// SuggestSnapshotConfig suggests a full snapshot config for all tables
func (c *Client) SuggestSnapshotConfig(
	ctx context.Context,
	branchName string,
	tables map[string][]string,
	rowCounts map[string]int64,
) (map[string]string, error) {
	system := `You are a database expert helping developers snapshot production data safely.
Suggest extraction queries for each table based on the branch name context.
Always include LIMIT clauses. Never expose sensitive data unnecessarily.
Respond ONLY with a JSON object mapping table names to SQL queries.
Example: {"users": "SELECT * FROM users WHERE active = true LIMIT 5000"}
No explanation, no markdown, just the JSON object.`

	var tableInfo strings.Builder
	for table, cols := range tables {
		tableInfo.WriteString(fmt.Sprintf(
			"Table %s (%d rows): columns: %s\n",
			table, rowCounts[table], strings.Join(cols, ", "),
		))
	}

	user := fmt.Sprintf(`Branch name: %s

Tables:
%s

Suggest extraction queries for each table appropriate for this branch context.`,
		branchName,
		tableInfo.String(),
	)

	response, err := c.request(ctx, system, user)
	if err != nil {
		return nil, err
	}

	response = strings.TrimSpace(response)

	// Strip markdown if present
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var suggestions map[string]string
	if err := json.Unmarshal([]byte(response), &suggestions); err != nil {
		return nil, fmt.Errorf("failed to parse suggestions: %w", err)
	}

	return suggestions, nil
}
