package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestNoStringInterpolationInSQL reads the source code and verifies that
// SQL queries use parameterized placeholders instead of string interpolation.
func TestNoStringInterpolationInSQL(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal("cannot read main.go:", err)
	}
	src := string(data)

	// Check that fmt.Sprintf is NOT used to build SQL queries.
	// Look for the vulnerable pattern: fmt.Sprintf with SQL keywords.
	sqlSprintfRe := regexp.MustCompile(`fmt\.Sprintf\([^)]*(?i:SELECT|INSERT|UPDATE|DELETE|FROM|WHERE)`)
	if sqlSprintfRe.MatchString(src) {
		t.Fatal("SQL queries must not use fmt.Sprintf for string interpolation; use parameterized queries")
	}

	// Check that string concatenation is not used with SQL keywords.
	concatRe := regexp.MustCompile(`(?i)(?:SELECT|INSERT|UPDATE|DELETE|WHERE)\s.*['"]\s*\+`)
	if concatRe.MatchString(src) {
		t.Fatal("SQL queries must not use string concatenation; use parameterized queries")
	}

	// Verify that parameterized placeholders are present.
	// Accept either $1 (PostgreSQL style) or ? (MySQL/SQLite style).
	hasPostgresPlaceholder := strings.Contains(src, "$1")
	hasMysqlPlaceholder := regexp.MustCompile(`\?\s*\)`).MatchString(src) ||
		strings.Contains(src, `", name)`) ||
		strings.Contains(src, `", name,`)

	// Also accept db.QueryRow("...?...", name) or db.QueryRow("...$1...", name) patterns.
	paramQueryRe := regexp.MustCompile(`(?:QueryRow|Query|Exec)\([^)]*(?:\$\d+|\?)[^)]*,\s*\w+`)
	hasParamQuery := paramQueryRe.MatchString(src)

	if !hasPostgresPlaceholder && !hasMysqlPlaceholder && !hasParamQuery {
		t.Fatal("expected parameterized query placeholders ($1 or ?) in SQL queries, but none found")
	}
}
