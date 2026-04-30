// post_merge_tree_check.go — TIER 1-C: detect catastrophic squash-merge
// data loss by comparing post-merge origin/main tree count against a
// remembered baseline. Maps to the actium-git incidents where PRs #180,
// #201, #240, and #241 each silently wiped 2500+ files.
package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// TreeBaseline holds the pre-merge file count for a repo's main branch.
type TreeBaseline struct {
	Repo    string `json:"repo"`     // e.g. RelayOne/actium
	Branch  string `json:"branch"`   // e.g. main
	Files   int    `json:"files"`    // file count from `git ls-tree -r <ref> | wc -l` or gh trees API
	Captured time.Time `json:"captured"`
}

// TreeCheckResult is what PostMergeTreeCheck returns. Drop is positive
// when the tree shrank, negative when it grew.
type TreeCheckResult struct {
	Baseline    TreeBaseline `json:"baseline"`
	Current     int          `json:"current"`
	Drop        int          `json:"drop"`         // baseline.Files - current
	DropPercent int          `json:"drop_percent"` // 0..100
	Verdict     string       `json:"verdict"`      // ok | warning | critical
	Reason      string       `json:"reason,omitempty"`
}

// MaxAcceptableDropPercent is the threshold above which the tree drop is
// flagged critical. Five percent matches actium's documented threshold.
const MaxAcceptableDropPercent = 5

// CaptureBaseline measures the current tree size and records a baseline.
// Two implementations: gh-API-backed (preferred when token is available)
// and git-CLI-backed (works in any local checkout).
func CaptureBaseline(ctx context.Context, repo, branch string) (TreeBaseline, error) {
	if repo == "" {
		return TreeBaseline{}, fmt.Errorf("repo required")
	}
	if branch == "" {
		branch = "main"
	}
	n, err := fetchTreeCountGH(ctx, repo, branch)
	if err != nil {
		return TreeBaseline{}, err
	}
	return TreeBaseline{
		Repo: repo, Branch: branch, Files: n,
		Captured: time.Now().UTC(),
	}, nil
}

// PostMergeTreeCheck refetches the tree count and compares against the
// captured baseline. Returns critical if the drop exceeds threshold.
func PostMergeTreeCheck(ctx context.Context, baseline TreeBaseline) (TreeCheckResult, error) {
	cur, err := fetchTreeCountGH(ctx, baseline.Repo, baseline.Branch)
	if err != nil {
		return TreeCheckResult{}, err
	}
	drop := baseline.Files - cur
	pct := 0
	if baseline.Files > 0 {
		pct = drop * 100 / baseline.Files
	}
	res := TreeCheckResult{
		Baseline: baseline, Current: cur, Drop: drop, DropPercent: pct,
	}
	switch {
	case drop <= 0:
		res.Verdict = "ok"
	case pct >= MaxAcceptableDropPercent:
		res.Verdict = "critical"
		res.Reason = fmt.Sprintf("tree dropped %d files (%d%%) — matches destructive-squash pattern; investigate before merging more", drop, pct)
	default:
		res.Verdict = "warning"
		res.Reason = fmt.Sprintf("tree dropped %d files (%d%%) — under threshold but worth a glance", drop, pct)
	}
	return res, nil
}

// fetchTreeCountGH queries https://api.github.com/repos/<repo>/git/trees/<branch>?recursive=1
// and counts the .tree array entries. Falls back to `git ls-tree` if the
// gh CLI is available locally and the API errors out.
func fetchTreeCountGH(ctx context.Context, repo, branch string) (int, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", repo, branch)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fetchTreeCountGitCLI(ctx, repo, branch)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fetchTreeCountGitCLI(ctx, repo, branch)
	}
	var body struct {
		Tree []json.RawMessage `json:"tree"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode trees: %w", err)
	}
	return len(body.Tree), nil
}

// fetchTreeCountGitCLI shells out to `gh api` if available, then to
// `git ls-tree -r <branch> | wc -l`.
func fetchTreeCountGitCLI(ctx context.Context, repo, branch string) (int, error) {
	if _, err := exec.LookPath("gh"); err == nil {
		out, err := exec.CommandContext(ctx, "gh", "api",
			fmt.Sprintf("repos/%s/git/trees/%s?recursive=1", repo, branch),
			"--jq", ".tree | length",
		).Output()
		if err == nil {
			n := 0
			fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
			if n > 0 {
				return n, nil
			}
		}
	}
	if _, err := exec.LookPath("git"); err == nil {
		out, err := exec.CommandContext(ctx, "git", "ls-tree", "-r", branch).Output()
		if err == nil {
			return strings.Count(string(out), "\n"), nil
		}
	}
	return 0, fmt.Errorf("could not fetch tree count for %s/%s — gh API failed and no local git/gh CLI", repo, branch)
}
