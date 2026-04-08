package tools

import (
	"fmt"
	"strings"
)

// ReplaceResult is the outcome of a str_replace attempt.
type ReplaceResult struct {
	NewContent   string
	Replacements int
	Method       string  // exact|whitespace|ellipsis|fuzzy
	Confidence   float64 // 0.0 - 1.0
}

// StrReplace performs the cascading str_replace algorithm:
//  1. Exact match
//  2. Whitespace-normalized match
//  3. Ellipsis expansion (handle "..." markers)
//  4. Fuzzy match (line-by-line similarity scoring)
//
// If oldStr appears multiple times in content and replaceAll is false, returns
// an error so the caller can ask for more context.
func StrReplace(content, oldStr, newStr string, replaceAll bool) (*ReplaceResult, error) {
	if oldStr == "" {
		return nil, fmt.Errorf("old_string cannot be empty")
	}

	// Method 1: exact match
	count := strings.Count(content, oldStr)
	if count > 0 {
		if count > 1 && !replaceAll {
			return nil, fmt.Errorf("old_string appears %d times in file; provide more context to make it unique, or set replace_all=true", count)
		}
		replacements := 1
		if replaceAll {
			replacements = count
			return &ReplaceResult{
				NewContent:   strings.ReplaceAll(content, oldStr, newStr),
				Replacements: replacements,
				Method:       "exact",
				Confidence:   1.0,
			}, nil
		}
		return &ReplaceResult{
			NewContent:   strings.Replace(content, oldStr, newStr, 1),
			Replacements: 1,
			Method:       "exact",
			Confidence:   1.0,
		}, nil
	}

	// Method 2: whitespace-normalized match
	if r := whitespaceNormalizedReplace(content, oldStr, newStr); r != nil {
		return r, nil
	}

	// Method 3: ellipsis expansion
	if strings.Contains(oldStr, "...") {
		if r := ellipsisReplace(content, oldStr, newStr); r != nil {
			return r, nil
		}
	}

	// Method 4: fuzzy match
	if r := fuzzyReplace(content, oldStr, newStr); r != nil {
		return r, nil
	}

	return nil, fmt.Errorf("old_string not found in content (tried exact, whitespace-normalized, ellipsis, fuzzy)")
}

func whitespaceNormalizedReplace(content, oldStr, newStr string) *ReplaceResult {
	normalize := func(s string) string {
		return strings.Join(strings.Fields(s), " ")
	}
	normContent := normalize(content)
	normOld := normalize(oldStr)
	if !strings.Contains(normContent, normOld) {
		return nil
	}
	// Find the original location by looking for the first non-empty line of oldStr
	oldFirstLine := firstNonEmptyLine(oldStr)
	if oldFirstLine == "" {
		return nil
	}
	idx := strings.Index(content, oldFirstLine)
	if idx < 0 {
		return nil
	}
	// Extract the matching block from content based on line count of oldStr
	oldLines := len(strings.Split(oldStr, "\n"))
	contentLines := strings.Split(content[idx:], "\n")
	if len(contentLines) < oldLines {
		return nil
	}
	matched := strings.Join(contentLines[:oldLines], "\n")
	if normalize(matched) != normOld {
		return nil
	}
	return &ReplaceResult{
		NewContent:   strings.Replace(content, matched, newStr, 1),
		Replacements: 1,
		Method:       "whitespace",
		Confidence:   0.85,
	}
}

func ellipsisReplace(content, oldStr, newStr string) *ReplaceResult {
	segments := strings.Split(oldStr, "...")
	if len(segments) < 2 {
		return nil
	}
	first := strings.TrimSpace(segments[0])
	last := strings.TrimSpace(segments[len(segments)-1])
	if first == "" || last == "" {
		return nil
	}
	startIdx := strings.Index(content, first)
	if startIdx < 0 {
		return nil
	}
	endStart := startIdx + len(first)
	endIdx := strings.Index(content[endStart:], last)
	if endIdx < 0 {
		return nil
	}
	matched := content[startIdx : endStart+endIdx+len(last)]
	return &ReplaceResult{
		NewContent:   strings.Replace(content, matched, newStr, 1),
		Replacements: 1,
		Method:       "ellipsis",
		Confidence:   0.75,
	}
}

func fuzzyReplace(content, oldStr, newStr string) *ReplaceResult {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(oldStr, "\n")
	if len(oldLines) > len(contentLines) || len(oldLines) == 0 {
		return nil
	}

	bestStart := -1
	bestScore := 0.0
	for i := 0; i <= len(contentLines)-len(oldLines); i++ {
		score := lineBlockSimilarity(contentLines[i:i+len(oldLines)], oldLines)
		if score > bestScore {
			bestScore = score
			bestStart = i
		}
	}
	if bestStart < 0 || bestScore < 0.7 {
		return nil
	}
	matched := strings.Join(contentLines[bestStart:bestStart+len(oldLines)], "\n")
	return &ReplaceResult{
		NewContent:   strings.Replace(content, matched, newStr, 1),
		Replacements: 1,
		Method:       "fuzzy",
		Confidence:   bestScore,
	}
}

func lineBlockSimilarity(a, b []string) float64 {
	if len(a) != len(b) {
		return 0
	}
	matches := 0
	for i := range a {
		if normalizedEqual(a[i], b[i]) {
			matches++
		}
	}
	return float64(matches) / float64(len(a))
}

func normalizedEqual(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}
