// comment.go — PR comment helpers for Bitbucket (C5, T11).
//
// Two posting paths:
//   - PostPRComment: unanchored summary comment on the PR conversation.
//   - PostInlinePRComment: line-anchored comment on a single file path.
//
// Plus BuildSummaryBody, which renders the shared `Finding` slice as the same
// markdown shape `internal/cicd/shared.RenderCommentBody` produces — so a
// reviewer that posts inline via Bitbucket and another that posts via GitHub
// emit byte-equivalent comment bodies.

package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/RelayOne/r1/internal/cicd/shared"
)

// PostPRComment posts an unanchored summary comment to a PR.
func (c *Client) PostPRComment(ctx context.Context, workspace, repo string, prID int, body string) error {
	if workspace == "" || repo == "" {
		return errors.New("bitbucket: PostPRComment: workspace and repo required")
	}
	if prID <= 0 {
		return errors.New("bitbucket: PostPRComment: prID must be > 0")
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("bitbucket: PostPRComment: body required")
	}
	endpoint := fmt.Sprintf("/repositories/%s/%s/pullrequests/%d/comments",
		url.PathEscape(workspace), url.PathEscape(repo), prID)
	payload := PRComment{
		Content: PRCommentContent{Raw: body},
	}
	return c.do(ctx, "POST", endpoint, payload, nil)
}

// PostInlinePRComment posts a line-anchored comment on a single file path
// in a PR. The line refers to the new-side line number (post-diff).
func (c *Client) PostInlinePRComment(ctx context.Context, workspace, repo string, prID int, path string, line int, body string) error {
	if workspace == "" || repo == "" {
		return errors.New("bitbucket: PostInlinePRComment: workspace and repo required")
	}
	if prID <= 0 {
		return errors.New("bitbucket: PostInlinePRComment: prID must be > 0")
	}
	if path == "" {
		return errors.New("bitbucket: PostInlinePRComment: path required")
	}
	if line <= 0 {
		return errors.New("bitbucket: PostInlinePRComment: line must be > 0")
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("bitbucket: PostInlinePRComment: body required")
	}
	endpoint := fmt.Sprintf("/repositories/%s/%s/pullrequests/%d/comments",
		url.PathEscape(workspace), url.PathEscape(repo), prID)
	payload := PRComment{
		Content: PRCommentContent{Raw: body},
		Inline: &PRCommentInline{
			Path: path,
			To:   line,
		},
	}
	return c.do(ctx, "POST", endpoint, payload, nil)
}

// BuildSummaryBody renders a markdown summary of a finding slice plus an
// optional artifact URL. Shape matches the multi-line aggregate body
// pattern used by other adapter Reviewers; the per-finding body within is
// shared.RenderCommentBody output verbatim.
func BuildSummaryBody(findings []shared.Finding, artifactURL string) string {
	if len(findings) == 0 {
		base := "**R1 Verify** — no findings."
		if artifactURL != "" {
			base += "\n\nArtifact: " + artifactURL
		}
		return base
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "**R1 Verify** — %d finding(s):\n\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(&sb, "- %s `%s:%d`\n", shared.RenderCommentBody(f), f.Path, f.Line)
	}
	if artifactURL != "" {
		fmt.Fprintf(&sb, "\nArtifact: %s\n", artifactURL)
	}
	return sb.String()
}
