// gh_tools.go — GitHub CLI tool handlers.
//
// T-R1P-016: Worktree hook events — exposes GitHub Actions and PR data via the
// `gh` CLI so the agent can react to CI status, PR reviews, and run logs.
//
// Tools:
//   - gh_pr_list:  list open PRs for the current repo (or a named repo)
//   - gh_pr_diff:  fetch the diff of a PR by number
//   - gh_run_list: list recent GitHub Actions workflow runs
//   - gh_run_view: view the log of a specific workflow run
//
// All tools require the `gh` CLI to be installed and authenticated.
// Graceful no-op: returns "gh not available" string (not a Go error) when
// the binary is absent so the model can report and continue.
//
// Security: all arguments are passed as separate argv slices (no shell).
// Output is truncated at 30KB.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	// maxGHOutput caps gh CLI output at 30KB.
	maxGHOutput = 30 * 1024
	// defaultGHTimeout is the default gh CLI timeout.
	defaultGHTimeout = 30 * time.Second
)

// ghAvailable returns true if the gh CLI binary is on PATH.
func ghAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

// runGH executes a gh command with the given args and returns combined output.
// Returns (output, nil) even on non-zero exit so the model sees the error text.
func (r *Registry) runGH(ctx context.Context, args ...string) (string, error) {
	if !ghAvailable() {
		return "gh not available: install the GitHub CLI (https://cli.github.com) and run `gh auth login`.", nil
	}

	execCtx, cancel := context.WithTimeout(ctx, defaultGHTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "gh", args...) // #nosec G204 — binary is hardcoded; args are vetted
	cmd.Dir = r.workDir
	out, err := cmd.CombinedOutput()

	outStr := string(out)
	if len(outStr) > maxGHOutput {
		mid := maxGHOutput / 2
		outStr = outStr[:mid] + "\n...(middle truncated)...\n" + outStr[len(outStr)-mid:]
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Sprintf("gh exit %d:\n%s", exitErr.ExitCode(), outStr), nil
		}
		return fmt.Sprintf("gh error: %v\n%s", err, outStr), nil
	}
	return outStr, nil
}

// handleGHPRList implements the gh_pr_list tool (T-R1P-016).
func (r *Registry) handleGHPRList(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Repo  string `json:"repo"`   // optional: owner/repo
		Limit int    `json:"limit"`  // default 20
		State string `json:"state"`  // open|closed|merged|all (default: open)
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	ghArgs := []string{"pr", "list", "--json", "number,title,author,state,headRefName,createdAt,url"}
	if args.Repo != "" {
		ghArgs = append(ghArgs, "-R", args.Repo)
	}
	state := strings.ToLower(strings.TrimSpace(args.State))
	if state == "" {
		state = "open"
	}
	ghArgs = append(ghArgs, "--state", state)
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	ghArgs = append(ghArgs, "--limit", fmt.Sprintf("%d", limit))

	return r.runGH(ctx, ghArgs...)
}

// handleGHPRDiff implements the gh_pr_diff tool (T-R1P-016).
func (r *Registry) handleGHPRDiff(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Number int    `json:"number"` // PR number
		Repo   string `json:"repo"`   // optional: owner/repo
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if args.Number <= 0 {
		return "", fmt.Errorf("number is required and must be a positive integer")
	}

	ghArgs := []string{"pr", "diff", fmt.Sprintf("%d", args.Number)}
	if args.Repo != "" {
		ghArgs = append(ghArgs, "-R", args.Repo)
	}
	return r.runGH(ctx, ghArgs...)
}

// handleGHRunList implements the gh_run_list tool (T-R1P-016).
func (r *Registry) handleGHRunList(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Repo     string `json:"repo"`     // optional: owner/repo
		Workflow string `json:"workflow"` // optional: workflow file or name
		Limit    int    `json:"limit"`    // default 10
		Branch   string `json:"branch"`   // optional: filter by branch
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	ghArgs := []string{"run", "list", "--json", "databaseId,name,status,conclusion,headBranch,createdAt,url"}
	if args.Repo != "" {
		ghArgs = append(ghArgs, "-R", args.Repo)
	}
	if args.Workflow != "" {
		ghArgs = append(ghArgs, "--workflow", args.Workflow)
	}
	if args.Branch != "" {
		ghArgs = append(ghArgs, "--branch", args.Branch)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	ghArgs = append(ghArgs, "--limit", fmt.Sprintf("%d", limit))

	return r.runGH(ctx, ghArgs...)
}

// handleGHRunView implements the gh_run_view tool (T-R1P-016).
func (r *Registry) handleGHRunView(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		RunID int    `json:"run_id"` // workflow run database ID
		Repo  string `json:"repo"`   // optional: owner/repo
		Log   bool   `json:"log"`    // if true, fetch failed job log
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if args.RunID <= 0 {
		return "", fmt.Errorf("run_id is required and must be a positive integer")
	}

	if args.Log {
		ghArgs := []string{"run", "view", "--log-failed", fmt.Sprintf("%d", args.RunID)}
		if args.Repo != "" {
			ghArgs = append(ghArgs, "-R", args.Repo)
		}
		return r.runGH(ctx, ghArgs...)
	}

	ghArgs := []string{"run", "view", "--json", "databaseId,name,status,conclusion,jobs,url",
		fmt.Sprintf("%d", args.RunID)}
	if args.Repo != "" {
		ghArgs = append(ghArgs, "-R", args.Repo)
	}
	return r.runGH(ctx, ghArgs...)
}
