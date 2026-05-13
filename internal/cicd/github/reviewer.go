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
//
// C5: the Finding / LLMFunc / ParserFunc / ParseFindings / RenderCommentBody /
// DefaultReviewPrompt primitives now live in `internal/cicd/shared` so that
// the GitHub Actions, GitLab CI, and BitBucket Pipelines adapters share one
// implementation. This file re-exports them as type aliases / variable
// re-bindings so existing callers compile unchanged.

package github

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/cicd/shared"
)

// LLMFunc is the model-call abstraction used by the auto-reviewer. Aliased
// from internal/cicd/shared so all adapters speak the same contract.
type LLMFunc = shared.LLMFunc

// ParserFunc converts an LLM response into a list of Findings. Aliased
// from internal/cicd/shared.
type ParserFunc = shared.ParserFunc

// Finding is a single code-review observation produced by the LLM. Aliased
// from internal/cicd/shared.
type Finding = shared.Finding

// ParseFindings reads an LLM response in the default format and returns the
// parsed findings. Re-exported from internal/cicd/shared.
var ParseFindings = shared.ParseFindings

// RenderCommentBody formats a Finding as a markdown comment body. Re-exported
// from internal/cicd/shared.
var RenderCommentBody = shared.RenderCommentBody

// DefaultReviewPrompt is the default code-review prompt template. Re-exported
// from internal/cicd/shared.
const DefaultReviewPrompt = shared.DefaultReviewPrompt

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

	prompt := shared.RenderPrompt(r.prompt, diff)
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
