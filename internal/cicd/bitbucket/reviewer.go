// reviewer.go — auto code-review pipeline for Bitbucket PRs (C5, T12).
//
// Mirrors `internal/cicd/github/reviewer.go` AutoReview one-for-one. The
// Finding / LLMFunc / ParserFunc / ParseFindings / DefaultReviewPrompt /
// RenderCommentBody primitives all come from internal/cicd/shared so the
// three adapters share a single implementation.

package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/cicd/shared"
)

// Reviewer wires the Bitbucket Client to an LLM for auto-review.
type Reviewer struct {
	c      *Client
	parser shared.ParserFunc
	prompt string // override default prompt template if non-empty
}

// NewReviewer constructs a Reviewer using shared.DefaultReviewPrompt and
// shared.ParseFindings.
func NewReviewer(c *Client) *Reviewer {
	return &Reviewer{c: c}
}

// SetPrompt overrides the default code-review prompt template. The template
// should contain "{{DIFF}}" where the unified diff goes. Returns the
// receiver for chaining.
func (r *Reviewer) SetPrompt(template string) *Reviewer {
	r.prompt = template
	return r
}

// SetParser overrides the response parser. Returns the receiver for chaining.
func (r *Reviewer) SetParser(p shared.ParserFunc) *Reviewer {
	r.parser = p
	return r
}

// AutoReview runs the end-to-end review pipeline:
//
//  1. Fetch diff + head commit.
//  2. Render prompt + call llm.
//  3. Parse findings.
//  4. Post each valid finding as an inline PR comment.
//
// Returns the parsed findings (whether or not posting succeeded). The post
// failure error, if any, is wrapped and returned alongside the findings so
// callers can decide whether to surface it.
func (r *Reviewer) AutoReview(ctx context.Context, workspace, repo string, prID int, llm shared.LLMFunc) ([]shared.Finding, error) {
	if llm == nil {
		return nil, errors.New("bitbucket: AutoReview: llm function required")
	}
	if workspace == "" || repo == "" || prID <= 0 {
		return nil, errors.New("bitbucket: AutoReview: workspace, repo, prID required")
	}

	diff, err := r.c.GetPullRequestDiff(ctx, workspace, repo, prID)
	if err != nil {
		return nil, fmt.Errorf("AutoReview: fetch diff: %w", err)
	}
	if strings.TrimSpace(diff) == "" {
		return nil, errors.New("bitbucket: AutoReview: PR diff is empty")
	}

	// Head commit fetched for completeness — Bitbucket PR comments are
	// anchored at the PR level, not the commit level (unlike GitHub's
	// review-comment API), but the runner needs the commit hash to set a
	// status row on the head.
	if _, err := r.c.GetPullRequestHead(ctx, workspace, repo, prID); err != nil {
		return nil, fmt.Errorf("AutoReview: fetch head commit: %w", err)
	}

	prompt := shared.RenderPrompt(r.prompt, diff)
	response, err := llm(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("AutoReview: llm call: %w", err)
	}

	parser := r.parser
	if parser == nil {
		parser = shared.ParseFindings
	}
	findings := parser(response)

	var postErrs []string
	for _, f := range findings {
		if !f.IsValid() {
			continue
		}
		body := shared.RenderCommentBody(f)
		if err := r.c.PostInlinePRComment(ctx, workspace, repo, prID, f.Path, f.Line, body); err != nil {
			postErrs = append(postErrs, fmt.Sprintf("%s:%d: %v", f.Path, f.Line, err))
		}
	}
	if len(postErrs) > 0 {
		return findings, fmt.Errorf("AutoReview: %d of %d comments failed to post: %s",
			len(postErrs), len(findings), strings.Join(postErrs, "; "))
	}
	return findings, nil
}
