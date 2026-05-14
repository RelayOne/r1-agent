// Package shared houses the cross-adapter primitives that the GitHub Actions,
// GitLab CI, and BitBucket Pipelines CI/CD adapters all consume.
//
// The package extracts the small handful of types and helpers that — prior to
// the BitBucket adapter (C5) — lived in `internal/cicd/github/reviewer.go`.
// Promoting them here keeps each adapter's auto-reviewer pipeline shaped
// identically, which the parity-audit test (T23) treats as a contract.
//
// Public surface (stable across adapters):
//
//	type Finding              // path + line + severity + body
//	type LLMFunc              // model-call abstraction (ctx, prompt) → reply
//	type ParserFunc           // LLM-response → []Finding
//	type Mode                 // re-exported from internal/cicd for adapter consumers
//	const CommitStatusName    // single source of truth for the PR status row
//	const DefaultReviewPrompt // prompt template with {{DIFF}} substitution
//	func  ParseFindings       // default parser for the markdown-bullet shape
//	func  RenderCommentBody   // markdown body for an inline review comment
package shared

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// CommitStatusName is the user-visible label on the head-commit status row that
// every CI adapter writes. The byte-for-byte value is asserted in tests so a
// regression — e.g., one adapter renaming the status — fails the build instead
// of silently producing two different rows on a PR.
const CommitStatusName = "R1 Verify"

// Mode is the integration pattern an adapter generates. It is a thin alias for
// `internal/cicd.Mode` so subpackages can refer to the canonical values without
// importing the parent package and risking a cycle.
type Mode string

// Canonical mode values. Must stay in lock-step with `internal/cicd.Mode*`.
const (
	ModeReview  Mode = "review"
	ModeAutoFix Mode = "autofix"
	ModeMission Mode = "mission"
)

// LLMFunc is the model-call abstraction used by every auto-reviewer.
// Implementations should return a single string response — the model's reply
// to the prompt — and remain stateless / side-effect-free; the reviewer wraps
// retry / timeout policy in its own ctx.
type LLMFunc func(ctx context.Context, prompt string) (string, error)

// ParserFunc converts an LLM response into a list of Findings. Adapters fall
// back to ParseFindings when nil is supplied.
type ParserFunc func(response string) []Finding

// Finding is a single code-review observation produced by the LLM. Path + Line
// are required for inline posting; Severity + Body are surfaced in the
// rendered comment.
type Finding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Severity string `json:"severity"` // "info" | "warning" | "error"
	Body     string `json:"body"`
}

// IsValid reports whether the Finding has the minimum fields required to be
// posted as a review comment.
func (f Finding) IsValid() bool {
	return f.Path != "" && f.Line > 0 && strings.TrimSpace(f.Body) != ""
}

// DefaultReviewPrompt is the markdown-bullet prompt template every adapter
// shares. {{DIFF}} is substituted with the unified diff before the LLM call.
const DefaultReviewPrompt = `You are an expert code reviewer. Review the following pull request diff.

For each issue you find, output ONE markdown bullet on its own line in this exact format:

- **<severity>** <path>:<line> — <message>

Where <severity> is one of: info, warning, error.

Only include real, actionable findings. Do NOT pad with stylistic nitpicks.
If the diff has no issues worth flagging, respond with the single line:

NO FINDINGS

PR diff:

` + "```diff\n{{DIFF}}\n```\n"

// ParseFindings reads an LLM response in the default bullet format and returns
// the parsed findings. Lines that don't match the bullet shape are skipped
// silently. The parser is intentionally lenient — LLMs add prose around the
// bullets, and that's fine.
func ParseFindings(response string) []Finding {
	if strings.Contains(response, "NO FINDINGS") {
		return nil
	}
	var out []Finding
	for _, line := range strings.Split(response, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- **") {
			continue
		}
		rest := strings.TrimPrefix(line, "- ")
		if !strings.HasPrefix(rest, "**") {
			continue
		}
		closeIdx := strings.Index(rest[2:], "**")
		if closeIdx < 0 {
			continue
		}
		sev := strings.ToLower(strings.TrimSpace(rest[2 : 2+closeIdx]))
		after := strings.TrimSpace(rest[2+closeIdx+2:])
		dashIdx := strings.Index(after, "—")
		if dashIdx < 0 {
			dashIdx = strings.Index(after, " - ")
			if dashIdx >= 0 {
				dashIdx++
			}
		}
		if dashIdx < 0 {
			continue
		}
		anchor := strings.TrimSpace(after[:dashIdx])
		message := strings.TrimSpace(strings.TrimLeft(after[dashIdx:], "—-"))
		colon := strings.LastIndex(anchor, ":")
		if colon <= 0 {
			continue
		}
		path := strings.TrimSpace(anchor[:colon])
		lineStr := strings.TrimSpace(anchor[colon+1:])
		lineNum, err := strconv.Atoi(lineStr)
		if err != nil || lineNum <= 0 {
			continue
		}
		if !validSeverity(sev) {
			sev = "info"
		}
		out = append(out, Finding{
			Path:     path,
			Line:     lineNum,
			Severity: sev,
			Body:     message,
		})
	}
	return out
}

// RenderCommentBody formats a Finding as a markdown comment body suitable for
// posting to any provider's PR review API. The format is stable so tests can
// assert exact content and so a tracebundle that quotes a comment body
// matches the rendered output byte-for-byte.
func RenderCommentBody(f Finding) string {
	sev := strings.ToUpper(f.Severity)
	if sev == "" {
		sev = "INFO"
	}
	return fmt.Sprintf("**[r1-review · %s]** %s", sev, strings.TrimSpace(f.Body))
}

// validSeverity gates the severity field so callers can't propagate arbitrary
// strings into the rendered comment.
func validSeverity(s string) bool {
	switch s {
	case "info", "warning", "error":
		return true
	}
	return false
}

// RenderPrompt fills the prompt template with the diff. Adapters call this
// from their AutoReview pipeline. An empty template falls back to
// DefaultReviewPrompt.
func RenderPrompt(template, diff string) string {
	if template == "" {
		template = DefaultReviewPrompt
	}
	return strings.ReplaceAll(template, "{{DIFF}}", diff)
}
