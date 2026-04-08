// Package diffcomp implements diff compression for compact change representation.
// Inspired by Aider's unified diff mode and claw-code's change summaries:
//
// When reviewing agent changes, full diffs are noisy. Compressed diffs:
// - Show only meaningful changes (skip whitespace-only, comment-only)
// - Collapse large unchanged sections
// - Provide token-budget-aware truncation
// - Generate human-readable change summaries
//
// This is critical for cross-model review: the reviewer needs to understand
// changes without drowning in context.
package diffcomp

import (
	"fmt"
	"strings"
)

// Hunk is a section of changes.
type Hunk struct {
	OldStart int      `json:"old_start"`
	OldCount int      `json:"old_count"`
	NewStart int      `json:"new_start"`
	NewCount int      `json:"new_count"`
	Lines    []Line   `json:"lines"`
	Context  string   `json:"context,omitempty"` // e.g., function name
}

// Line is a single diff line.
type Line struct {
	Op      Op     `json:"op"`      // Add, Remove, Context
	Content string `json:"content"`
	OldNum  int    `json:"old_num,omitempty"`
	NewNum  int    `json:"new_num,omitempty"`
}

// Op is a diff operation.
type Op string

const (
	OpAdd     Op = "+"
	OpRemove  Op = "-"
	OpContext Op = " "
)

// FileDiff represents all changes to a single file.
type FileDiff struct {
	Path    string `json:"path"`
	Hunks   []Hunk `json:"hunks"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary"`
}

// Diff computes a simple line-level diff between old and new content.
func Diff(oldContent, newContent string) FileDiff {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	// Simple LCS-based diff
	ops := computeOps(oldLines, newLines)

	var hunks []Hunk
	var currentHunk *Hunk
	contextLines := 3
	added, removed := 0, 0

	oldNum, newNum := 1, 1
	for i, op := range ops {
		switch op.op {
		case OpContext:
			if currentHunk != nil {
				// Check if we should close the hunk
				remaining := countRemainingChanges(ops[i:])
				if remaining == 0 || countContextRun(ops[i:]) > contextLines*2 {
					// Add trailing context
					currentHunk.Lines = append(currentHunk.Lines, Line{Op: OpContext, Content: op.text, OldNum: oldNum, NewNum: newNum})
					currentHunk.OldCount++
					currentHunk.NewCount++
					hunks = append(hunks, *currentHunk)
					currentHunk = nil
				} else {
					currentHunk.Lines = append(currentHunk.Lines, Line{Op: OpContext, Content: op.text, OldNum: oldNum, NewNum: newNum})
					currentHunk.OldCount++
					currentHunk.NewCount++
				}
			}
			oldNum++
			newNum++

		case OpAdd:
			added++
			if currentHunk == nil {
				currentHunk = startHunk(oldNum, newNum, ops, i, contextLines, oldLines, newLines)
			}
			currentHunk.Lines = append(currentHunk.Lines, Line{Op: OpAdd, Content: op.text, NewNum: newNum})
			currentHunk.NewCount++
			newNum++

		case OpRemove:
			removed++
			if currentHunk == nil {
				currentHunk = startHunk(oldNum, newNum, ops, i, contextLines, oldLines, newLines)
			}
			currentHunk.Lines = append(currentHunk.Lines, Line{Op: OpRemove, Content: op.text, OldNum: oldNum})
			currentHunk.OldCount++
			oldNum++
		}
	}

	if currentHunk != nil {
		hunks = append(hunks, *currentHunk)
	}

	return FileDiff{
		Hunks:   hunks,
		Added:   added,
		Removed: removed,
	}
}

// Compress reduces a diff by removing trivial changes.
func Compress(diff FileDiff, opts CompressOpts) FileDiff {
	var hunks []Hunk
	for _, h := range diff.Hunks {
		compressed := compressHunk(h, opts)
		if len(compressed.Lines) > 0 {
			hunks = append(hunks, compressed)
		}
	}
	diff.Hunks = hunks
	return diff
}

// CompressOpts controls what gets filtered.
type CompressOpts struct {
	SkipWhitespace bool // ignore whitespace-only changes
	SkipComments   bool // ignore comment-only changes
	MaxContext      int  // max context lines around changes (default 3)
}

// Render produces a unified diff string.
func Render(diff FileDiff) string {
	var b strings.Builder
	if diff.Path != "" {
		fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", diff.Path, diff.Path)
	}
	for _, h := range diff.Hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@", h.OldStart, h.OldCount, h.NewStart, h.NewCount)
		if h.Context != "" {
			fmt.Fprintf(&b, " %s", h.Context)
		}
		b.WriteString("\n")
		for _, l := range h.Lines {
			fmt.Fprintf(&b, "%s%s\n", string(l.Op), l.Content)
		}
	}
	return b.String()
}

// Summarize produces a short human-readable description.
func Summarize(diff FileDiff) string {
	if diff.Binary {
		return fmt.Sprintf("%s: binary file changed", diff.Path)
	}
	hunks := len(diff.Hunks)
	if hunks == 0 {
		return fmt.Sprintf("%s: no changes", diff.Path)
	}
	return fmt.Sprintf("%s: %d hunks, +%d -%d lines", diff.Path, hunks, diff.Added, diff.Removed)
}

// TruncateToTokens limits diff output to approximately maxTokens.
func TruncateToTokens(diff FileDiff, maxTokens int) FileDiff {
	rendered := Render(diff)
	est := len(rendered) / 3 // rough token estimate
	if est <= maxTokens {
		return diff
	}

	// Keep hunks until budget exhausted
	var kept []Hunk
	used := 0
	for _, h := range diff.Hunks {
		hunkStr := renderHunk(h)
		hunkTokens := len(hunkStr) / 3
		if used+hunkTokens > maxTokens && len(kept) > 0 {
			break
		}
		kept = append(kept, h)
		used += hunkTokens
	}
	diff.Hunks = kept
	return diff
}

// Stats returns aggregate statistics for multiple diffs.
func Stats(diffs []FileDiff) (files, added, removed int) {
	for _, d := range diffs {
		files++
		added += d.Added
		removed += d.Removed
	}
	return
}

// --- internals ---

type diffOp struct {
	op   Op
	text string
}

func computeOps(old, new []string) []diffOp {
	// Simple O(NM) LCS for correctness; fine for typical file sizes
	m, n := len(old), len(new)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if old[i-1] == new[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack
	var ops []diffOp
	i, j := m, n
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && old[i-1] == new[j-1] {
			ops = append(ops, diffOp{OpContext, old[i-1]})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			ops = append(ops, diffOp{OpAdd, new[j-1]})
			j--
		} else {
			ops = append(ops, diffOp{OpRemove, old[i-1]})
			i--
		}
	}

	// Reverse
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}
	return ops
}

func countRemainingChanges(ops []diffOp) int {
	count := 0
	for _, op := range ops {
		if op.op != OpContext {
			count++
		}
	}
	return count
}

func countContextRun(ops []diffOp) int {
	count := 0
	for _, op := range ops {
		if op.op != OpContext {
			break
		}
		count++
	}
	return count
}

func startHunk(oldNum, newNum int, ops []diffOp, idx, contextLines int, oldLines, newLines []string) *Hunk {
	return &Hunk{
		OldStart: oldNum,
		NewStart: newNum,
	}
}

func compressHunk(h Hunk, opts CompressOpts) Hunk {
	var lines []Line
	for _, l := range h.Lines {
		if l.Op != OpContext {
			trimmed := strings.TrimSpace(l.Content)
			if opts.SkipWhitespace && trimmed == "" {
				continue
			}
			if opts.SkipComments && isComment(trimmed) {
				continue
			}
		}
		lines = append(lines, l)
	}
	h.Lines = lines

	// Trim excess context
	if opts.MaxContext > 0 {
		h.Lines = trimContext(h.Lines, opts.MaxContext)
	}
	return h
}

func isComment(line string) bool {
	return strings.HasPrefix(line, "//") ||
		strings.HasPrefix(line, "#") ||
		strings.HasPrefix(line, "/*") ||
		strings.HasPrefix(line, "*") ||
		strings.HasPrefix(line, "'''") ||
		strings.HasPrefix(line, "\"\"\"")
}

func trimContext(lines []Line, maxCtx int) []Line {
	if len(lines) == 0 {
		return lines
	}

	// Find first and last change
	firstChange, lastChange := -1, -1
	for i, l := range lines {
		if l.Op != OpContext {
			if firstChange == -1 {
				firstChange = i
			}
			lastChange = i
		}
	}

	if firstChange == -1 {
		return nil // no changes
	}

	start := firstChange - maxCtx
	if start < 0 {
		start = 0
	}
	end := lastChange + maxCtx + 1
	if end > len(lines) {
		end = len(lines)
	}

	return lines[start:end]
}

func renderHunk(h Hunk) string {
	var b strings.Builder
	for _, l := range h.Lines {
		fmt.Fprintf(&b, "%s%s\n", string(l.Op), l.Content)
	}
	return b.String()
}
