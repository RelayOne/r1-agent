// scaling.go implements complexity-based verification scaling.
// Inspired by OmX's verification system: scale rigor based on change size.
//
// Three tiers (from OmX):
// - Small  (≤3 files, <100 lines): typecheck + tests + basic confirmation
// - Standard (≤15 files, <500 lines): + lint + regression
// - Large (>15 files or >500 lines): + security review + performance + API compat
//
// This prevents over-verifying trivial changes while ensuring thorough review
// of large, risky changes. Integrates with the existing Pipeline.
package verify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Complexity classifies change size for verification scaling.
type Complexity string

const (
	ComplexitySmall    Complexity = "small"    // ≤3 files, <100 lines changed
	ComplexityStandard Complexity = "standard" // ≤15 files, <500 lines
	ComplexityLarge    Complexity = "large"    // >15 files or >500 lines
)

// ChangeStats describes the size of a change for complexity classification.
type ChangeStats struct {
	FilesChanged int
	LinesAdded   int
	LinesRemoved int
}

// TotalLines returns the total number of lines changed.
func (cs ChangeStats) TotalLines() int {
	return cs.LinesAdded + cs.LinesRemoved
}

// ClassifyComplexity determines the complexity tier for a change.
func ClassifyComplexity(stats ChangeStats) Complexity {
	if stats.FilesChanged <= 3 && stats.TotalLines() < 100 {
		return ComplexitySmall
	}
	if stats.FilesChanged <= 15 && stats.TotalLines() < 500 {
		return ComplexityStandard
	}
	return ComplexityLarge
}

// ScaledPipeline extends Pipeline with complexity-aware verification.
type ScaledPipeline struct {
	base            *Pipeline
	securityCmd     string // additional security scan command
	performanceCmd  string // performance regression check
	apiCompatCmd    string // API compatibility check
}

// NewScaledPipeline creates a complexity-scaled verification pipeline.
func NewScaledPipeline(base *Pipeline, securityCmd, performanceCmd, apiCompatCmd string) *ScaledPipeline {
	return &ScaledPipeline{
		base:           base,
		securityCmd:    securityCmd,
		performanceCmd: performanceCmd,
		apiCompatCmd:   apiCompatCmd,
	}
}

// RunScaled executes verification steps appropriate to the change complexity.
func (sp *ScaledPipeline) RunScaled(ctx context.Context, dir string, stats ChangeStats) ([]Outcome, Complexity, error) {
	complexity := ClassifyComplexity(stats)

	// Always run base pipeline (build + test + lint)
	outcomes, baseErr := sp.base.Run(ctx, dir)

	switch complexity {
	case ComplexitySmall:
		// Small: just build + test (lint may be skipped if base handles it)
		return outcomes, complexity, baseErr

	case ComplexityStandard:
		// Standard: base + ensure lint ran
		// Lint is already in base pipeline, so nothing extra
		return outcomes, complexity, baseErr

	case ComplexityLarge:
		// Large: base + security + performance + API compat
		for _, extra := range []struct {
			name string
			cmd  string
		}{
			{"security", sp.securityCmd},
			{"performance", sp.performanceCmd},
			{"api-compat", sp.apiCompatCmd},
		} {
			if strings.TrimSpace(extra.cmd) == "" {
				outcomes = append(outcomes, Outcome{Name: extra.name, Skipped: true, Success: true, Output: "no command configured"})
				continue
			}
			cmd := exec.CommandContext(ctx, "bash", "-lc", extra.cmd)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			outcome := Outcome{Name: extra.name, Success: err == nil, Output: string(out)}
			outcomes = append(outcomes, outcome)
			if err != nil && baseErr == nil {
				baseErr = fmt.Errorf("verification failed in %s", extra.name)
			}
		}
		return outcomes, complexity, baseErr
	}

	return outcomes, complexity, baseErr
}

// DiffStats extracts change statistics from a git diff.
// dir should be the worktree path.
func DiffStats(ctx context.Context, dir, base string) (ChangeStats, error) {
	if base == "" {
		base = "HEAD~1"
	}

	cmd := exec.CommandContext(ctx, "git", "diff", "--shortstat", base+"..HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ChangeStats{}, fmt.Errorf("git diff --shortstat: %w", err)
	}

	return parseShortstat(string(out)), nil
}

// parseShortstat extracts stats from git diff --shortstat output.
// Format: " 3 files changed, 42 insertions(+), 10 deletions(-)"
func parseShortstat(output string) ChangeStats {
	var stats ChangeStats
	output = strings.TrimSpace(output)
	if output == "" {
		return stats
	}

	// Parse "N files changed"
	fmt.Sscanf(extractNumber(output, "file"), "%d", &stats.FilesChanged)
	fmt.Sscanf(extractNumber(output, "insertion"), "%d", &stats.LinesAdded)
	fmt.Sscanf(extractNumber(output, "deletion"), "%d", &stats.LinesRemoved)

	return stats
}

func extractNumber(s, keyword string) string {
	idx := strings.Index(s, keyword)
	if idx < 0 {
		return "0"
	}
	// Walk backwards from keyword to find the number
	end := idx
	for end > 0 && s[end-1] == ' ' {
		end--
	}
	start := end
	for start > 0 && s[start-1] >= '0' && s[start-1] <= '9' {
		start--
	}
	if start == end {
		return "0"
	}
	return s[start:end]
}

// VerificationSummary returns a human-readable summary of the verification run.
func VerificationSummary(outcomes []Outcome, complexity Complexity) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Verification (%s complexity):\n", complexity))

	for _, o := range outcomes {
		status := "PASS"
		if o.Skipped {
			status = "SKIP"
		} else if !o.Success {
			status = "FAIL"
		}
		sb.WriteString(fmt.Sprintf("  [%s] %s\n", status, o.Name))
	}
	return sb.String()
}
