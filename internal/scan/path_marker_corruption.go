// path_marker_corruption.go — TIER 1-B: detect files whose content is the
// pathological "@<path>" marker pattern (an LLM hallucination that has
// shipped to actium main twice), or production files that are suspiciously
// short.
//
// This complements the regex-based DefaultRules() by operating on whole
// files rather than line patterns: regex-only detection misses the case
// where the entire file contents are a single short string.
package scan

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PathMarkerRule identifies the "@<path>" corruption pattern: a source
// file whose content is exactly a path-like string starting with "@".
//
// Example incident: apps/shopify/src/logger.ts in actium-git was found
// containing the literal `@apps/shopify/src/logger.ts` instead of code.
type PathMarkerRule struct {
	// MinSizeForExt maps a file extension to the smallest byte size we
	// consider plausible for production code. Files smaller than this AND
	// matching the path-marker pattern will be flagged at high severity.
	MinSizeForExt map[string]int
}

// DefaultPathMarkerRule returns a rule with sensible defaults.
func DefaultPathMarkerRule() PathMarkerRule {
	return PathMarkerRule{MinSizeForExt: map[string]int{
		".ts":  100,
		".tsx": 100,
		".js":  100,
		".jsx": 100,
		".go":  100,
		".py":  60,
		".rs":  100,
		".java": 100,
	}}
}

var pathMarkerRE = regexp.MustCompile(`^@[A-Za-z0-9_./-]+$`)

// ScanPathMarker walks dir and returns Findings for every file whose
// trimmed content matches the path-marker pattern, or whose size is below
// the per-extension minimum AND the file looks like it should hold code.
//
// modifiedOnly, if non-empty, restricts the scan to those relative paths.
func (r PathMarkerRule) ScanPathMarker(dir string, modifiedOnly []string) ([]Finding, error) {
	var findings []Finding
	check := func(path string) error {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = path
		}
		ext := strings.ToLower(filepath.Ext(path))
		min, watched := r.MinSizeForExt[ext]
		if !watched {
			return nil
		}
		// Skip auto-generated, vendored, and build artifacts.
		if shouldSkipForPathMarker(rel) {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		// Only read small files in full; large files can't be path-marker corrupt.
		if info.Size() > 4096 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		trimmed := strings.TrimSpace(string(data))
		if pathMarkerRE.MatchString(trimmed) {
			findings = append(findings, Finding{
				Rule:     "path-marker-corruption",
				Severity: SeverityCritical,
				File:     rel,
				Line:     1,
				Message:  "file content is literally a path marker (@<path>) instead of code — likely LLM hallucination",
				Fix:      "restore from git history: git show <last-good-sha>:" + rel,
			})
			return nil
		}
		if int(info.Size()) < min && looksLikeProductionPath(rel) {
			findings = append(findings, Finding{
				Rule:     "suspiciously-short-source",
				Severity: "high",
				File:     rel,
				Line:     1,
				Message:  "production-path source file is suspiciously short (" + sizeStr(int(info.Size())) + " < " + sizeStr(min) + ") — verify it isn't a stub or corruption",
				Fix:      "open the file and confirm it contains real code; check git log for last meaningful commit",
			})
		}
		return nil
	}

	if len(modifiedOnly) > 0 {
		for _, p := range modifiedOnly {
			full := p
			if !filepath.IsAbs(p) {
				full = filepath.Join(dir, p)
			}
			_ = check(full)
		}
		return findings, nil
	}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		return check(path)
	})
	return findings, err
}

func shouldSkipForPathMarker(rel string) bool {
	for _, prefix := range []string{
		"node_modules/", "vendor/", "dist/", "build/", ".git/",
		"target/", ".next/", ".turbo/", "coverage/", "__pycache__/",
	} {
		if strings.HasPrefix(rel, prefix) || strings.Contains(rel, "/"+prefix) {
			return true
		}
	}
	for _, suffix := range []string{
		".d.ts", ".gen.go", ".pb.go", ".min.js", "_pb2.py",
	} {
		if strings.HasSuffix(rel, suffix) {
			return true
		}
	}
	return false
}

// looksLikeProductionPath returns true if rel sits in a directory we
// expect to hold production code (apps/, packages/, internal/, src/, lib/,
// cmd/, server/, services/). Test files, scripts, and configs are skipped.
func looksLikeProductionPath(rel string) bool {
	if strings.Contains(rel, "_test.") || strings.Contains(rel, ".test.") || strings.Contains(rel, ".spec.") {
		return false
	}
	for _, prefix := range []string{
		"apps/", "packages/", "internal/", "src/", "lib/",
		"cmd/", "server/", "services/", "modules/",
	} {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

func sizeStr(n int) string {
	if n < 1024 {
		return itoaSimple(n) + "B"
	}
	return itoaSimple(n/1024) + "KB"
}

func itoaSimple(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
