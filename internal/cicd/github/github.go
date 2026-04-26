// Package github is the GitHub Actions + Pull Requests adapter
// (T-R1P-021).
//
// It complements the YAML template generator in `internal/cicd` (which
// renders `.github/workflows/*.yml` files) by giving the agent a
// runtime API surface to actually trigger workflows, poll runs, fetch
// job logs, and post inline review comments against any GitHub repo.
//
// Built on top of github.com/google/go-github/v62 — the canonical Go
// SDK for the GitHub REST API. The wrapper narrows the SDK's surface
// to the operations the agent needs, normalises the polling /
// timeout / batching semantics, and provides the auto-reviewer
// pipeline in reviewer.go.
//
// Public surface:
//
//	c := github.New(github.Config{Token: os.Getenv("GITHUB_TOKEN")})
//	err := c.TriggerWorkflow(ctx, "owner", "repo", "ci.yml", "main", inputs)
//	st, err := c.GetRunStatus(ctx, "owner", "repo", runID)
//	final, err := c.WaitForCompletion(ctx, "owner", "repo", runID, 10*time.Minute)
//	logsURL, err := c.GetJobLogs(ctx, "owner", "repo", runID)
//	err = c.PostReviewComment(ctx, "owner", "repo", prNumber, body, line, path)
//
// And the auto code-review helper:
//
//	rev := github.NewReviewer(c)
//	findings, err := rev.AutoReview(ctx, "owner", "repo", prNumber, llmFunc)
//
// Auth: a Personal Access Token (PAT), fine-grained token, or GitHub
// App installation token is sourced from Config.Token. The token is
// handed to go-github via WithAuthToken, which assembles the
// Authorization header internally — no token literal appears in this
// package's source, keeping it clean for credential static scanners.
//
// All HTTP calls are timed by the supplied context. The default
// endpoint is api.github.com — swap via Config.BaseURL for GitHub
// Enterprise.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v62/github"
)

// parseURL is a thin wrapper around url.Parse used for clarity at
// the call site (gh.Client takes a *url.URL for BaseURL/UploadURL).
func parseURL(s string) (*url.URL, error) { return url.Parse(s) }

// DefaultBaseURL is the v3 REST endpoint for github.com. Mirrors
// gh.Client's default (api.github.com).
const DefaultBaseURL = "https://api.github.com/"

// Config configures a Client.
type Config struct {
	// BaseURL overrides the GitHub API endpoint. Empty = DefaultBaseURL.
	// For GitHub Enterprise, set this to the Enterprise REST root.
	BaseURL string
	// Token is the GitHub personal access token, fine-grained token,
	// or GitHub App installation token. Required for any non-public op.
	Token string
	// HTTPClient overrides the default http.Client used by go-github.
	// Useful in tests (httptest.NewServer hands you a Server.Client()).
	HTTPClient *http.Client
	// PollInterval is how often WaitForCompletion polls. Defaults to 5s.
	PollInterval time.Duration
	// UserAgent overrides the User-Agent header. Empty falls back to
	// the go-github default ("go-github" / version).
	UserAgent string
}

// Client wraps a gh.Client with the agent-friendly surface this
// package promises. The underlying *gh.Client is exposed via Raw so
// callers that need an SDK feature we don't wrap can drop down.
type Client struct {
	gh       *gh.Client
	pollEvry time.Duration
}

// New constructs a Client from cfg, applying defaults for empty
// fields. Auth wiring uses gh.Client.WithAuthToken so no token literal
// appears in this file.
func New(cfg Config) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	gc := gh.NewClient(httpClient)
	if cfg.Token != "" {
		gc = gc.WithAuthToken(cfg.Token)
	}
	if base := strings.TrimSpace(cfg.BaseURL); base != "" {
		// gh.Client expects a trailing slash on the base URL.
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		// SetBaseURL shape: gh.Client takes BaseURL via WithEnterpriseURLs
		// for GH Enterprise but for an arbitrary endpoint (e.g.,
		// httptest.NewServer) we set it directly via the public field.
		if u, err := parseURL(base); err == nil {
			gc.BaseURL = u
			gc.UploadURL = u
		}
	}
	if cfg.UserAgent != "" {
		gc.UserAgent = cfg.UserAgent
	}
	pollEvry := cfg.PollInterval
	if pollEvry <= 0 {
		pollEvry = 5 * time.Second
	}
	return &Client{gh: gc, pollEvry: pollEvry}
}

// Raw exposes the underlying *gh.Client for callers that need an SDK
// feature this wrapper does not surface directly.
func (c *Client) Raw() *gh.Client { return c.gh }

// --- types ---

// WorkflowRun is the trimmed view of a GitHub workflow run. Mirrors
// the fields of gh.WorkflowRun the agent cares about; the underlying
// SDK type carries many more (event, head sha, run attempt, etc.) and
// is reachable via Raw().Actions.
type WorkflowRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	HeadBranch string    `json:"head_branch"`
	HeadSHA    string    `json:"head_sha"`
	RunNumber  int       `json:"run_number"`
	Event      string    `json:"event"`
	Status     string    `json:"status"`     // queued / in_progress / completed
	Conclusion string    `json:"conclusion"` // success / failure / cancelled / skipped / neutral / action_required / timed_out
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// IsTerminal reports whether the run has reached a final state.
func (r WorkflowRun) IsTerminal() bool {
	return r.Status == "completed"
}

// fromSDK converts a *gh.WorkflowRun (rich SDK type) into our trimmed
// shape. Pointers in the SDK type are flattened with safe accessors.
func fromSDKWorkflowRun(r *gh.WorkflowRun) *WorkflowRun {
	if r == nil {
		return nil
	}
	out := &WorkflowRun{
		ID:         r.GetID(),
		Name:       r.GetName(),
		HeadBranch: r.GetHeadBranch(),
		HeadSHA:    r.GetHeadSHA(),
		RunNumber:  r.GetRunNumber(),
		Event:      r.GetEvent(),
		Status:     r.GetStatus(),
		Conclusion: r.GetConclusion(),
		HTMLURL:    r.GetHTMLURL(),
	}
	if r.CreatedAt != nil {
		out.CreatedAt = r.CreatedAt.Time
	}
	if r.UpdatedAt != nil {
		out.UpdatedAt = r.UpdatedAt.Time
	}
	return out
}

// PullRequestFile is the trimmed view of a PR file change. Mirrors
// the agent-relevant fields of gh.CommitFile.
type PullRequestFile struct {
	SHA       string `json:"sha"`
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Changes   int    `json:"changes"`
	Patch     string `json:"patch,omitempty"`
}

// fromSDKCommitFile converts gh.CommitFile to our trimmed shape.
func fromSDKCommitFile(f *gh.CommitFile) PullRequestFile {
	if f == nil {
		return PullRequestFile{}
	}
	return PullRequestFile{
		SHA:       f.GetSHA(),
		Filename:  f.GetFilename(),
		Status:    f.GetStatus(),
		Additions: f.GetAdditions(),
		Deletions: f.GetDeletions(),
		Changes:   f.GetChanges(),
		Patch:     f.GetPatch(),
	}
}

// ReviewComment describes an inline PR review comment. Uses the
// shape required by the GitHub PR comments REST endpoint, which
// go-github's *gh.PullRequestComment also mirrors.
type ReviewComment struct {
	Body     string `json:"body"`
	CommitID string `json:"commit_id"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Side     string `json:"side,omitempty"` // RIGHT (default) | LEFT
}

// toSDKComment converts to the SDK type needed by
// PullRequestsService.CreateComment.
func (rc ReviewComment) toSDKComment() *gh.PullRequestComment {
	side := rc.Side
	if side == "" {
		side = "RIGHT"
	}
	body := rc.Body
	commit := rc.CommitID
	path := rc.Path
	line := rc.Line
	return &gh.PullRequestComment{
		Body:     &body,
		CommitID: &commit,
		Path:     &path,
		Line:     &line,
		Side:     &side,
	}
}

// --- workflow operations ---

// TriggerWorkflow kicks off a workflow_dispatch event for the given
// workflow id (numeric id or filename like "ci.yml") on the given ref.
// Inputs are passed verbatim through to the workflow.
func (c *Client) TriggerWorkflow(ctx context.Context, owner, repo, workflowID, ref string, inputs map[string]interface{}) error {
	if owner == "" || repo == "" {
		return errors.New("github: TriggerWorkflow: owner and repo required")
	}
	if workflowID == "" {
		return errors.New("github: TriggerWorkflow: workflowID required")
	}
	if ref == "" {
		return errors.New("github: TriggerWorkflow: ref required")
	}
	event := gh.CreateWorkflowDispatchEventRequest{
		Ref:    ref,
		Inputs: inputs,
	}
	// If workflowID parses as an integer, use the by-id endpoint;
	// otherwise treat it as a filename ("ci.yml", "release.yaml").
	if id, err := strconv.ParseInt(workflowID, 10, 64); err == nil {
		_, err := c.gh.Actions.CreateWorkflowDispatchEventByID(ctx, owner, repo, id, event)
		if err != nil {
			return fmt.Errorf("github: TriggerWorkflow: %w", err)
		}
		return nil
	}
	_, err := c.gh.Actions.CreateWorkflowDispatchEventByFileName(ctx, owner, repo, workflowID, event)
	if err != nil {
		return fmt.Errorf("github: TriggerWorkflow: %w", err)
	}
	return nil
}

// GetRunStatus fetches a single workflow run by id.
func (c *Client) GetRunStatus(ctx context.Context, owner, repo string, runID int64) (*WorkflowRun, error) {
	r, _, err := c.gh.Actions.GetWorkflowRunByID(ctx, owner, repo, runID)
	if err != nil {
		return nil, fmt.Errorf("github: GetRunStatus: %w", err)
	}
	return fromSDKWorkflowRun(r), nil
}

// WaitForCompletion blocks until the run reaches a terminal status or
// the timeout expires. Returns the final WorkflowRun on success.
func (c *Client) WaitForCompletion(ctx context.Context, owner, repo string, runID int64, timeout time.Duration) (*WorkflowRun, error) {
	if timeout <= 0 {
		return nil, errors.New("github: WaitForCompletion: timeout must be > 0")
	}
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(c.pollEvry)
	defer tick.Stop()

	for {
		r, err := c.GetRunStatus(ctx, owner, repo, runID)
		if err != nil {
			return nil, err
		}
		if r.IsTerminal() {
			return r, nil
		}
		if time.Now().After(deadline) {
			return r, fmt.Errorf("github: WaitForCompletion: timed out after %s (last status: %s)", timeout, r.Status)
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-tick.C:
		}
	}
}

// GetJobLogs returns a signed URL pointing at the combined log
// archive for a run. GitHub answers /actions/runs/:id/logs with a
// 302 redirect to a short-lived signed object-storage URL; this
// helper returns that URL as a string. Callers can then HTTP GET it
// to download the zip.
//
// Returning the URL (not the bytes) keeps memory bounded for very
// large logs and matches go-github's idiom.
func (c *Client) GetJobLogs(ctx context.Context, owner, repo string, runID int64) (string, error) {
	u, _, err := c.gh.Actions.GetWorkflowRunLogs(ctx, owner, repo, runID, 5)
	if err != nil {
		return "", fmt.Errorf("github: GetJobLogs: %w", err)
	}
	if u == nil {
		return "", errors.New("github: GetJobLogs: nil URL from SDK")
	}
	return u.String(), nil
}

// --- pull-request operations ---

// GetPullRequestDiff fetches the unified diff for a PR via the
// SDK's GetRaw helper with RawType=Diff (which sends
// Accept: application/vnd.github.diff under the hood).
func (c *Client) GetPullRequestDiff(ctx context.Context, owner, repo string, prNumber int) (string, error) {
	if prNumber <= 0 {
		return "", errors.New("github: GetPullRequestDiff: prNumber must be > 0")
	}
	diff, _, err := c.gh.PullRequests.GetRaw(ctx, owner, repo, prNumber, gh.RawOptions{Type: gh.Diff})
	if err != nil {
		return "", fmt.Errorf("github: GetPullRequestDiff: %w", err)
	}
	return diff, nil
}

// ListPullRequestFiles returns the per-file change summary for a PR,
// including the patch hunks for each file.
func (c *Client) ListPullRequestFiles(ctx context.Context, owner, repo string, prNumber int) ([]PullRequestFile, error) {
	files, _, err := c.gh.PullRequests.ListFiles(ctx, owner, repo, prNumber, nil)
	if err != nil {
		return nil, fmt.Errorf("github: ListPullRequestFiles: %w", err)
	}
	out := make([]PullRequestFile, 0, len(files))
	for _, f := range files {
		out = append(out, fromSDKCommitFile(f))
	}
	return out, nil
}

// GetPullRequestHeadSHA fetches the head commit SHA of a PR. Required
// when posting inline review comments because the API binds the
// comment to a specific commit.
func (c *Client) GetPullRequestHeadSHA(ctx context.Context, owner, repo string, prNumber int) (string, error) {
	pr, _, err := c.gh.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return "", fmt.Errorf("github: GetPullRequestHeadSHA: %w", err)
	}
	if pr == nil || pr.Head == nil || pr.Head.GetSHA() == "" {
		return "", errors.New("github: GetPullRequestHeadSHA: head sha empty in response")
	}
	return pr.Head.GetSHA(), nil
}

// PostReviewComment posts a single line-anchored comment on a PR.
// The commit SHA is fetched automatically.
func (c *Client) PostReviewComment(ctx context.Context, owner, repo string, prNumber int, body, path string, line int) error {
	if body == "" {
		return errors.New("github: PostReviewComment: body required")
	}
	if path == "" {
		return errors.New("github: PostReviewComment: path required")
	}
	if line <= 0 {
		return errors.New("github: PostReviewComment: line must be > 0")
	}
	sha, err := c.GetPullRequestHeadSHA(ctx, owner, repo, prNumber)
	if err != nil {
		return err
	}
	return c.PostReviewCommentDirect(ctx, owner, repo, prNumber, ReviewComment{
		Body:     body,
		CommitID: sha,
		Path:     path,
		Line:     line,
		Side:     "RIGHT",
	})
}

// PostReviewCommentDirect is the SHA-known variant. Cheaper than
// PostReviewComment because it skips the head-sha lookup.
func (c *Client) PostReviewCommentDirect(ctx context.Context, owner, repo string, prNumber int, comment ReviewComment) error {
	if comment.Body == "" || comment.CommitID == "" || comment.Path == "" || comment.Line <= 0 {
		return errors.New("github: PostReviewCommentDirect: body, commit_id, path, line all required")
	}
	if _, _, err := c.gh.PullRequests.CreateComment(ctx, owner, repo, prNumber, comment.toSDKComment()); err != nil {
		return fmt.Errorf("github: PostReviewCommentDirect: %w", err)
	}
	return nil
}

// --- error classification ---

// IsNotFound reports whether the error wraps a 404 from the API.
// Bridges go-github's *gh.ErrorResponse into a stable predicate.
func IsNotFound(err error) bool {
	var er *gh.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		return er.Response.StatusCode == http.StatusNotFound
	}
	return false
}

// IsUnauthorized reports whether the error wraps a 401 from the API.
func IsUnauthorized(err error) bool {
	var er *gh.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		return er.Response.StatusCode == http.StatusUnauthorized
	}
	return false
}
