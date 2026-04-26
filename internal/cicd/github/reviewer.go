// reviewer.go — auto code-review skill for GitHub PRs (T-R1P-021).
//
// Workflow:
//
//  1. Caller hands a PR number + LLM callback.
//  2. AutoReview fetches the diff via GET /repos/:o/:r/pulls/:n.diff
//     plus the per-file change summary via /pulls/:n/files.
//  3. The reviewer renders a CodeReviewPrompt with the diff embedded
//     and feeds it to the supplied LLMFunc.
//  4. Findings parsed out of the LLM response are formatted as inline
//     ReviewComments and posted via PostReviewCommentDirect.
//
// The LLM contract is intentionally narrow: a single function that
// takes a prompt string and returns a string response. Callers can
// wrap any model provider — anthropic, openai, local — without this
// package depending on a specific SDK.
//
// The Finding struct lets the LLM emit structured output without
// requiring tool-use or JSON-mode. The default ParseFindings reader
// accepts a relaxed format (one finding per markdown bullet); callers
// who want stricter parsing can substitute their own ParserFunc.

package github

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// LLMFunc is the model-call abstraction used by the auto-reviewer.
// Implementations should return a single string response (the model's
// reply to the prompt). The reviewer wraps the model call in its own
// retry / timeout policy via ctx — implementations should be
// stateless and side-effect-free.
type LLMFunc func(ctx context.Context, prompt string) (string, error)

// ParserFunc converts an LLM response into a list of Findings.
// AutoReview defaults to ParseFindings when nil is passed.
type ParserFunc func(response string) []Finding

// Finding is a single code-review observation produced by the LLM.
// Path + Line are required for inline posting; Severity + Body are
// surfaced in the rendered comment.
type Finding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Severity string `json:"severity"` // "info" | "warning" | "error"
	Body     string `json:"body"`
}

// IsValid reports whether the Finding has the minimum fields required
// to be posted as a review comment.
func (f Finding) IsValid() bool {
	return f.Path != "" && f.Line > 0 && strings.TrimSpace(f.Body) != ""
}

// Reviewer wires the GitHub Client to an LLM for auto-review.
type Reviewer struct {
	c      *Client
	parser ParserFunc
	prompt string // override default prompt template if non-empty
}

// NewReviewer constructs a Reviewer that uses the default prompt and
// parser. Use SetPrompt / SetParser to customize.
func NewReviewer(c *Client) *Reviewer {
	return &Reviewer{c: c}
}

// SetPrompt overrides the default code-review prompt template. The
// template should contain "{{DIFF}}" where the unified diff goes.
// Returns the receiver for chaining.
func (r *Reviewer) SetPrompt(template string) *Reviewer {
	r.prompt = template
	return r
}

// SetParser overrides the response parser. Returns the receiver for chaining.
func (r *Reviewer) SetParser(p ParserFunc) *Reviewer {
	r.parser = p
	return r
}

// AutoReview runs the end-to-end review pipeline:
//
//  1. Fetch diff + head SHA.
//  2. Render prompt + call llm.
//  3. Parse findings.
//  4. Post each finding as an inline comment.
//
// Returns the parsed findings (whether or not posting succeeded). The
// post-failure error, if any, is wrapped and returned alongside the
// findings so callers can decide whether to surface it.
func (r *Reviewer) AutoReview(ctx context.Context, owner, repo string, prNumber int, llm LLMFunc) ([]Finding, error) {
	if llm == nil {
		return nil, errors.New("github: AutoReview: llm function required")
	}
	if owner == "" || repo == "" || prNumber <= 0 {
		return nil, errors.New("github: AutoReview: owner, repo, prNumber required")
	}

	diff, err := r.c.GetPullRequestDiff(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("AutoReview: fetch diff: %w", err)
	}
	if strings.TrimSpace(diff) == "" {
		return nil, errors.New("github: AutoReview: PR diff is empty")
	}

	sha, err := r.c.GetPullRequestHeadSHA(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("AutoReview: fetch head sha: %w", err)
	}

	prompt := r.renderPrompt(diff)
	response, err := llm(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("AutoReview: llm call: %w", err)
	}

	parser := r.parser
	if parser == nil {
		parser = ParseFindings
	}
	findings := parser(response)

	var postErrs []string
	for _, f := range findings {
		if !f.IsValid() {
			continue
		}
		comment := ReviewComment{
			Body:     RenderCommentBody(f),
			CommitID: sha,
			Path:     f.Path,
			Line:     f.Line,
			Side:     "RIGHT",
		}
		if err := r.c.PostReviewCommentDirect(ctx, owner, repo, prNumber, comment); err != nil {
			postErrs = append(postErrs, fmt.Sprintf("%s:%d: %v", f.Path, f.Line, err))
		}
	}
	if len(postErrs) > 0 {
		return findings, fmt.Errorf("AutoReview: %d of %d comments failed to post: %s",
			len(postErrs), len(findings), strings.Join(postErrs, "; "))
	}
	return findings, nil
}

// renderPrompt fills the prompt template with the diff. Uses the
// default template when SetPrompt was not called.
func (r *Reviewer) renderPrompt(diff string) string {
	tpl := r.prompt
	if tpl == "" {
		tpl = DefaultReviewPrompt
	}
	return strings.ReplaceAll(tpl, "{{DIFF}}", diff)
}

// DefaultReviewPrompt is the default code-review template. Designed
// to elicit structured output that ParseFindings can read without
// requiring JSON mode.
const DefaultReviewPrompt = `You are an expert code reviewer. Review the following pull request diff.

For each issue you find, output ONE markdown bullet on its own line in this exact format:

- **<severity>** <path>:<line> — <message>

Where <severity> is one of: info, warning, error.

Only include real, actionable findings. Do NOT pad with stylistic nitpicks.
If the diff has no issues worth flagging, respond with the single line:

NO FINDINGS

PR diff:

` + "```diff\n{{DIFF}}\n```\n"

// ParseFindings reads an LLM response in the default format and
// returns the parsed findings. Lines that don't match the bullet
// shape are skipped silently. The parser is intentionally lenient —
// LLMs add prose around the bullets, and that's fine.
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
		// Strip "- "
		rest := strings.TrimPrefix(line, "- ")
		// Extract severity between "**...**"
		if !strings.HasPrefix(rest, "**") {
			continue
		}
		closeIdx := strings.Index(rest[2:], "**")
		if closeIdx < 0 {
			continue
		}
		sev := strings.ToLower(strings.TrimSpace(rest[2 : 2+closeIdx]))
		after := strings.TrimSpace(rest[2+closeIdx+2:])
		// after looks like "path:line — message" or "path:line - message"
		dashIdx := strings.Index(after, "—")
		if dashIdx < 0 {
			dashIdx = strings.Index(after, " - ")
			if dashIdx >= 0 {
				dashIdx++ // align past the space before "-"
			}
		}
		if dashIdx < 0 {
			continue
		}
		anchor := strings.TrimSpace(after[:dashIdx])
		message := strings.TrimSpace(strings.TrimLeft(after[dashIdx:], "—-"))
		// anchor should be path:line
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

// RenderCommentBody formats a Finding as a markdown comment body
// suitable for posting to the GitHub PR review API. The format is
// stable so tests can assert exact content.
func RenderCommentBody(f Finding) string {
	sev := strings.ToUpper(f.Severity)
	if sev == "" {
		sev = "INFO"
	}
	return fmt.Sprintf("**[r1-review · %s]** %s", sev, strings.TrimSpace(f.Body))
}

// validSeverity gates the severity field so we don't propagate
// arbitrary strings into the rendered comment.
func validSeverity(s string) bool {
	switch s {
	case "info", "warning", "error":
		return true
	}
	return false
}
