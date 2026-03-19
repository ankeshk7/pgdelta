package pii

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Mask represents a single PII masking rule
type Mask struct {
	Column  string
	Pattern string
}

// Rewriter rewrites SQL queries to apply PII masks
type Rewriter struct {
	masks map[string]string // column -> mask pattern
}

// New creates a new PII rewriter from a masks config map
// masks format: "table.column" -> "mask_pattern"
func New(masks map[string]string) *Rewriter {
	normalized := make(map[string]string)
	for k, v := range masks {
		normalized[strings.ToLower(k)] = v
	}
	return &Rewriter{masks: normalized}
}

// RewriteQuery rewrites an extraction SQL query to mask PII columns
// Original: SELECT * FROM users WHERE plan = 'pro'
// Rewritten: SELECT id, 'masked_' || id || '@example.com' AS email, name, plan FROM users WHERE plan = 'pro'
func (r *Rewriter) RewriteQuery(
	ctx context.Context,
	pool *pgxpool.Pool,
	tableName string,
	query string,
) (string, error) {
	if len(r.masks) == 0 {
		return query, nil
	}

	// Check if any masks apply to this table
	tableMasks := r.getTableMasks(tableName)
	if len(tableMasks) == 0 {
		return query, nil
	}

	// Get column list for this table
	columns, err := getColumns(ctx, pool, tableName)
	if err != nil {
		// Can't rewrite without column info — return original
		return query, nil
	}

	// Build SELECT list with masks applied
	var selectParts []string
	for _, col := range columns {
		colLower := strings.ToLower(col)
		if pattern, masked := tableMasks[colLower]; masked {
			selectParts = append(selectParts,
				fmt.Sprintf("%s AS %s", applyMask(col, pattern), col))
		} else {
			selectParts = append(selectParts, col)
		}
	}

	// Replace SELECT * or SELECT col1, col2 with masked SELECT
	rewritten := replaceSelect(query, strings.Join(selectParts, ", "))
	return rewritten, nil
}

// getTableMasks returns masks that apply to a specific table
// Input masks are "table.column" format
// Returns map of column -> pattern for this table
func (r *Rewriter) getTableMasks(tableName string) map[string]string {
	result := make(map[string]string)
	tableLower := strings.ToLower(tableName)

	for key, pattern := range r.masks {
		parts := strings.SplitN(key, ".", 2)
		if len(parts) == 2 {
			// "table.column" format
			if strings.ToLower(parts[0]) == tableLower {
				result[strings.ToLower(parts[1])] = pattern
			}
		} else {
			// Just "column" format — applies to all tables
			result[strings.ToLower(parts[0])] = pattern
		}
	}

	return result
}

// applyMask converts a column name and pattern into a SQL expression
// Patterns:
//   "masked_{{id}}@example.com"  → 'masked_' || id || '@example.com'
//   "+10000000000"               → '+10000000000'
//   "***"                        → '***'
//   "REDACTED"                   → 'REDACTED'
func applyMask(columnName, pattern string) string {
	// Check if pattern contains {{id}} placeholder
	if strings.Contains(pattern, "{{id}}") {
		// Split on {{id}} and build concatenation
		parts := strings.Split(pattern, "{{id}}")
		var sqlParts []string
		for i, part := range parts {
			if part != "" {
				sqlParts = append(sqlParts, fmt.Sprintf("'%s'", escapeSQLString(part)))
			}
			if i < len(parts)-1 {
				sqlParts = append(sqlParts, "id::text")
			}
		}
		return strings.Join(sqlParts, " || ")
	}

	// Check for {{column}} placeholder — use the column's own value transformed
	if strings.Contains(pattern, "{{"+columnName+"}}") {
		replaced := strings.ReplaceAll(pattern, "{{"+columnName+"}}", columnName)
		return fmt.Sprintf("'%s'", escapeSQLString(replaced))
	}

	// Check for {{last4}} — last 4 chars of column
	if strings.Contains(pattern, "{{last4}}") {
		parts := strings.Split(pattern, "{{last4}}")
		var sqlParts []string
		for i, part := range parts {
			if part != "" {
				sqlParts = append(sqlParts, fmt.Sprintf("'%s'", escapeSQLString(part)))
			}
			if i < len(parts)-1 {
				sqlParts = append(sqlParts,
					fmt.Sprintf("RIGHT(%s::text, 4)", columnName))
			}
		}
		return strings.Join(sqlParts, " || ")
	}

	// Static replacement — return as SQL string literal
	return fmt.Sprintf("'%s'", escapeSQLString(pattern))
}

// replaceSelect rewrites the SELECT clause of a query
func replaceSelect(query, newSelectList string) string {
	upper := strings.ToUpper(strings.TrimSpace(query))

	// Find SELECT...FROM
	selectIdx := strings.Index(upper, "SELECT")
	fromIdx := strings.Index(upper, "FROM")

	if selectIdx == -1 || fromIdx == -1 {
		return query
	}

	// Rebuild: SELECT <newlist> FROM ...rest
	rest := query[fromIdx:]
	return fmt.Sprintf("SELECT %s %s", newSelectList, rest)
}

// getColumns returns column names for a table
func getColumns(ctx context.Context, pool *pgxpool.Pool, tableName string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position
	`, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	return cols, rows.Err()
}

// escapeSQLString escapes single quotes in SQL string literals
func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// DetectPIIColumns scans column names for likely PII
// Returns map of "table.column" -> suggested mask pattern
func DetectPIIColumns(tables map[string][]string) map[string]string {
	result := make(map[string]string)

	// Patterns that suggest PII
	emailPatterns := []string{"email", "mail", "e_mail"}
	phonePatterns := []string{"phone", "mobile", "cell", "tel", "telephone"}
	namePatterns  := []string{"first_name", "last_name", "full_name", "display_name"}
	ssnPatterns   := []string{"ssn", "social_security", "national_id", "tax_id"}
	cardPatterns  := []string{"card_number", "credit_card", "debit_card", "pan"}
	dobPatterns   := []string{"dob", "date_of_birth", "birth_date", "birthday"}
	addrPatterns  := []string{"address", "street", "city", "zip", "postal"}
	ipPatterns    := []string{"ip_address", "ip", "remote_addr", "client_ip"}

	for table, columns := range tables {
		for _, col := range columns {
			colLower := strings.ToLower(col)
			key := fmt.Sprintf("%s.%s", table, col)

			switch {
			case matchesAny(colLower, emailPatterns):
				result[key] = "masked_{{id}}@example.com"
			case matchesAny(colLower, phonePatterns):
				result[key] = "+10000000000"
			case matchesAny(colLower, ssnPatterns):
				result[key] = "***-**-{{last4}}"
			case matchesAny(colLower, cardPatterns):
				result[key] = "****-****-****-{{last4}}"
			case matchesAny(colLower, namePatterns):
				result[key] = "REDACTED"
			case matchesAny(colLower, dobPatterns):
				result[key] = "1970-01-01"
			case matchesAny(colLower, addrPatterns):
				result[key] = "REDACTED"
			case matchesAny(colLower, ipPatterns):
				result[key] = "0.0.0.0"
			}
		}
	}

	return result
}

// matchesAny checks if a column name contains any of the patterns
func matchesAny(colName string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(colName, p) {
			return true
		}
	}
	return false
}

// Summary prints a human readable summary of active masks
func Summary(masks map[string]string) {
	if len(masks) == 0 {
		fmt.Println("  No PII masks configured.")
		return
	}
	fmt.Printf("  %d PII mask(s) active:\n", len(masks))
	for col, pattern := range masks {
		fmt.Printf("    %-30s → %s\n", col, pattern)
	}
}
