// Package team implements parallel multi-agent review and coordination.
// Inspired by oh-my-codex's $team mode, which enables parallel code review
// and architecture feedback from multiple AI agents simultaneously.
//
// The core idea: instead of a single agent reviewing code, dispatch multiple
// agents with different perspectives (security, performance, correctness)
// in parallel, then synthesize their findings into a unified verdict.
package team

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ReviewPerspective defines what a single reviewer focuses on.
type ReviewPerspective struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Focus       string   `json:"focus"`
	SystemPrompt string  `json:"system_prompt"`
	Model       string   `json:"model,omitempty"`       // override model for this perspective
	MaxTokens   int      `json:"max_tokens,omitempty"`  // override budget
	Critical    bool     `json:"critical"`               // if true, failing this review blocks merge
}

// Finding is one issue identified by a reviewer.
type Finding struct {
	PerspectiveID string `json:"perspective_id"`
	Severity      string `json:"severity"` // critical, high, medium, low, info
	File          string `json:"file,omitempty"`
	Line          int    `json:"line,omitempty"`
	Issue         string `json:"issue"`
	Suggestion    string `json:"suggestion,omitempty"`
	Confidence    float64 `json:"confidence"` // 0.0-1.0
}

// ReviewResult is the output of one perspective's review.
type ReviewResult struct {
	PerspectiveID string    `json:"perspective_id"`
	Pass          bool      `json:"pass"`
	Findings      []Finding `json:"findings"`
	Summary       string    `json:"summary"`
	DurationMs    int64     `json:"duration_ms"`
	TokensUsed    int       `json:"tokens_used"`
	Error         string    `json:"error,omitempty"`
}

// TeamVerdict is the synthesized outcome from all parallel reviews.
type TeamVerdict struct {
	Pass             bool           `json:"pass"`
	Reviews          []ReviewResult `json:"reviews"`
	CriticalFindings []Finding      `json:"critical_findings"`
	AllFindings      []Finding      `json:"all_findings"`
	Consensus        string         `json:"consensus"` // unanimous, majority, split
	Summary          string         `json:"summary"`
	TotalDurationMs  int64          `json:"total_duration_ms"`
	WallClockMs      int64          `json:"wall_clock_ms"` // actual elapsed (parallel)
}

// ReviewFunc executes a single review for a given perspective and diff.
// Implementations should call the appropriate AI model and parse the response.
type ReviewFunc func(ctx context.Context, perspective ReviewPerspective, diff string) ReviewResult

// ParallelReview dispatches multiple review perspectives in parallel and synthesizes results.
// This is the core of $team mode: fan-out to N reviewers, fan-in the results.
func ParallelReview(ctx context.Context, perspectives []ReviewPerspective, diff string, reviewFn ReviewFunc) TeamVerdict {
	start := time.Now()

	results := make([]ReviewResult, len(perspectives))
	var wg sync.WaitGroup

	for i, p := range perspectives {
		wg.Add(1)
		go func(idx int, perspective ReviewPerspective) {
			defer wg.Done()
			results[idx] = reviewFn(ctx, perspective, diff)
		}(i, p)
	}

	wg.Wait()
	wallClock := time.Since(start).Milliseconds()

	return synthesize(perspectives, results, wallClock)
}

// synthesize combines individual review results into a team verdict.
func synthesize(perspectives []ReviewPerspective, results []ReviewResult, wallClockMs int64) TeamVerdict {
	verdict := TeamVerdict{
		Pass:        true,
		Reviews:     results,
		WallClockMs: wallClockMs,
	}

	passCount := 0
	failCount := 0
	criticalFailCount := 0

	for i, r := range results {
		verdict.TotalDurationMs += r.DurationMs

		if r.Pass {
			passCount++
		} else {
			failCount++
			if perspectives[i].Critical {
				criticalFailCount++
			}
		}

		for _, f := range r.Findings {
			verdict.AllFindings = append(verdict.AllFindings, f)
			if f.Severity == "critical" || f.Severity == "high" {
				verdict.CriticalFindings = append(verdict.CriticalFindings, f)
			}
		}
	}

	// Verdict logic:
	// - Any critical perspective fails → overall fail
	// - All pass → pass
	// - Majority pass (no critical fails) → pass with warnings
	if criticalFailCount > 0 {
		verdict.Pass = false
		verdict.Consensus = "critical_fail"
	} else if failCount == 0 {
		verdict.Consensus = "unanimous"
	} else if passCount > failCount {
		verdict.Consensus = "majority"
		// Majority pass but has non-critical failures — still pass but note it
	} else {
		verdict.Pass = false
		verdict.Consensus = "split"
	}

	verdict.Summary = formatVerdictSummary(verdict, perspectives)
	return verdict
}

func formatVerdictSummary(v TeamVerdict, perspectives []ReviewPerspective) string {
	var sb strings.Builder
	passStr := "PASS"
	if !v.Pass {
		passStr = "FAIL"
	}
	fmt.Fprintf(&sb, "Team review: %s (%s)\n", passStr, v.Consensus)
	fmt.Fprintf(&sb, "Reviewers: %d | Critical findings: %d | Total findings: %d\n",
		len(v.Reviews), len(v.CriticalFindings), len(v.AllFindings))
	fmt.Fprintf(&sb, "Wall clock: %dms | Total compute: %dms (%.1fx parallel speedup)\n",
		v.WallClockMs, v.TotalDurationMs, float64(v.TotalDurationMs)/float64(max(v.WallClockMs, 1)))

	for i, r := range v.Reviews {
		status := "✓"
		if !r.Pass {
			status = "✗"
		}
		if r.Error != "" {
			status = "⚠"
		}
		fmt.Fprintf(&sb, "  %s %s: %d findings", status, perspectives[i].Name, len(r.Findings))
		if r.Error != "" {
			fmt.Fprintf(&sb, " (error: %s)", r.Error)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// DefaultPerspectives returns the standard team review perspectives.
// These map to the most impactful review angles from the audit package.
func DefaultPerspectives() []ReviewPerspective {
	return []ReviewPerspective{
		{
			ID: "correctness", Name: "Correctness Reviewer",
			Focus: "Does the code actually do what the task requires? Are all edge cases handled?",
			SystemPrompt: `You are a correctness reviewer. Your ONLY job is to verify that the code change correctly implements what was asked for.

Check:
1. Does every stated requirement have a corresponding implementation?
2. Are edge cases handled (nil, empty, overflow, concurrent access)?
3. Are error paths correct (not swallowed, not panic)?
4. Does the code match the test expectations?

Respond with JSON: {"pass": bool, "findings": [{"severity": "critical|high|medium|low", "file": "path", "issue": "what's wrong", "suggestion": "how to fix"}]}`,
			Critical: true,
		},
		{
			ID: "security", Name: "Security Reviewer",
			Focus: "Are there any security vulnerabilities introduced?",
			SystemPrompt: `You are a security reviewer. Look ONLY for security issues.

Check:
1. Injection (SQL, command, template, path traversal)
2. Auth/authz bypasses
3. Sensitive data exposure (secrets, PII in logs)
4. Cryptographic misuse
5. SSRF, CSRF, XSS

Respond with JSON: {"pass": bool, "findings": [{"severity": "critical|high|medium|low", "file": "path", "issue": "what's wrong", "suggestion": "how to fix"}]}`,
			Critical: true,
		},
		{
			ID: "quality", Name: "Quality Reviewer",
			Focus: "Code quality, maintainability, and best practices",
			SystemPrompt: `You are a quality reviewer. Check for maintainability and best practices.

Check:
1. Test quality (not tautological, good coverage, no .skip/.only)
2. Error handling (no empty catches, proper propagation)
3. Naming and clarity
4. No type bypasses (@ts-ignore, as any, eslint-disable)
5. No debug artifacts (console.log, print statements)

Respond with JSON: {"pass": bool, "findings": [{"severity": "critical|high|medium|low", "file": "path", "issue": "what's wrong", "suggestion": "how to fix"}]}`,
			Critical: false, // quality issues don't block merge
		},
	}
}

// ArchitecturePerspectives returns perspectives for architecture review.
func ArchitecturePerspectives() []ReviewPerspective {
	return []ReviewPerspective{
		{
			ID: "api-design", Name: "API Design Reviewer",
			Focus:    "REST conventions, error responses, backwards compatibility",
			Critical: false,
		},
		{
			ID: "scalability", Name: "Scalability Reviewer",
			Focus:    "N+1 queries, unbounded allocations, missing pagination, hot paths",
			Critical: false,
		},
		{
			ID: "data-integrity", Name: "Data Integrity Reviewer",
			Focus:    "Transaction boundaries, constraint enforcement, migration safety",
			Critical: true,
		},
	}
}
