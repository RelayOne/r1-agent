// This file adds a deterministic git-context assembler for stoke's
// repair flows.
//
// WHY THIS EXISTS:
//
// An LLM repair worker dispatched to "fix" a failing file frequently
// rewrites code that it itself wrote one or two turns ago, silently
// re-introducing the exact bug a prior turn just fixed. This failure
// mode has been observed repeatedly in stoke's multi-turn sessions:
// turn N adds a null-check, the integration reviewer flags an
// unrelated gap in the same file, turn N+2 rewrites the function
// from scratch and drops the null-check.
//
// The fix is deterministic, not prompt-based: before dispatching a
// repair worker, stoke reads recent commit history for the files
// that worker is about to touch and injects it verbatim into the
// system prompt. The worker then sees "commit a1b2c3d (2h ago): fix
// null check in usePatient" right next to its repair directive —
// making re-introduction of that bug a visibly wrong move, not an
// invisible regression.
//
// The assembler shells out to the `git` binary directly; no LLM is
// involved in producing the summary. It fails closed (returns empty
// string / nil) when git is unavailable, the working tree is not a
// repo, or the file has no history, rather than injecting bogus
// context. All byte budgets are hard caps with visible truncation.

package plan

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitInvocationTimeout caps how long any single git sub-command may
// run. Deterministic repair context is nice-to-have, not essential,
// so we never want a stuck git call to block dispatch.
const gitInvocationTimeout = 5 * time.Second

// Default and cap constants for AssembleFileHistory. Exported as
// package-private because callers pass their own bounds explicitly;
// these only drive the "pass 0, get the default" and "pass huge, get
// clamped" behavior.
const (
	defaultMaxCommits  = 5
	capMaxCommits      = 20
	defaultMaxDiffByte = 4000
	capMaxDiffByte     = 16000
)

// GitFileHistory is a deterministic snapshot of a single file's
// recent change history. Produced by AssembleFileHistory.
type GitFileHistory struct {
	// Path is the file this history covers, relative to repoRoot.
	Path string

	// RecentCommits is the last N commits that touched this file,
	// oldest-first. Never more than MaxCommits entries.
	RecentCommits []GitCommitInfo

	// RecentDiff is the unified diff showing changes to this file
	// across RecentCommits. Truncated to DiffBudget bytes.
	RecentDiff string
}

// GitCommitInfo is metadata about one commit.
type GitCommitInfo struct {
	Hash    string // 7-char abbreviated sha
	Author  string
	Date    string // ISO-8601 relative-fine (e.g. "2 hours ago")
	Subject string // first line of commit message
}

// AssembleFileHistory reads git log + diff for filePath and returns
// a structured summary. Reads max N commits (default 5, cap 20) and
// caps diff content at maxDiffBytes (default 4000, cap 16000).
//
// Returns nil + nil when:
//   - repoRoot is not a git repository
//   - filePath has no history (new untracked file)
//   - git binary not on PATH
//
// Errors only on protocol-level git failures (exit code != 0 with
// unexpected stderr).
func AssembleFileHistory(repoRoot, filePath string, maxCommits, maxDiffBytes int) (*GitFileHistory, error) {
	if repoRoot == "" || filePath == "" {
		return nil, nil
	}

	// Clamp bounds.
	if maxCommits <= 0 {
		maxCommits = defaultMaxCommits
	}
	if maxCommits > capMaxCommits {
		maxCommits = capMaxCommits
	}
	if maxDiffBytes <= 0 {
		maxDiffBytes = defaultMaxDiffByte
	}
	if maxDiffBytes > capMaxDiffByte {
		maxDiffBytes = capMaxDiffByte
	}

	// Silent no-op when git is missing.
	if _, err := exec.LookPath("git"); err != nil {
		return nil, nil
	}

	// Silent no-op when we're not inside a working tree.
	if !isInsideGitRepo(repoRoot) {
		return nil, nil
	}

	// Relativize filePath against repoRoot when absolute.
	rel := filePath
	if filepath.IsAbs(filePath) {
		if r, err := filepath.Rel(repoRoot, filePath); err == nil {
			rel = r
		}
	}
	rel = filepath.ToSlash(rel)

	// --- git log for metadata ---
	logOut, err := runGit(repoRoot, "log",
		fmt.Sprintf("--format=%%h|%%an|%%ar|%%s"),
		"-n", fmt.Sprintf("%d", maxCommits),
		"--", rel,
	)
	if err != nil {
		return nil, err
	}
	commits := parseCommitLog(strings.TrimSpace(logOut))
	if len(commits) == 0 {
		// No history — untracked or unknown file.
		return nil, nil
	}

	// git log returns newest-first; flip to oldest-first per spec.
	reverseCommits(commits)

	// --- git log -p for diff ---
	diffOut, err := runGit(repoRoot, "log", "-p", "--unified=3",
		"-n", fmt.Sprintf("%d", maxCommits),
		"--", rel,
	)
	if err != nil {
		return nil, err
	}
	diff := truncateBytes(diffOut, maxDiffBytes)

	return &GitFileHistory{
		Path:          rel,
		RecentCommits: commits,
		RecentDiff:    diff,
	}, nil
}

// AssembleRepairContext is the higher-level entry for the SOW
// repair flow. Takes a list of files a repair worker will touch
// and returns a single prompt-ready string combining history for
// each file, clipped to totalBudget bytes overall.
//
// Returns "" when none of the files have history. Format:
//
//	"RECENT CHANGES TO FILES YOU'RE ABOUT TO REPAIR (do NOT re-introduce bugs these commits fixed):
//
//	--- apps/web/app/page.tsx ---
//	commits:
//	  a1b2c3d eric: fix null check in usePatient (2h ago)
//	  9f8e7d6 eric: add loading state (5h ago)
//	diff (last 2 commits):
//	+ if (!patient) return <Skeleton />
//	- return <div>{patient.name}</div>
//	...
//
//	--- packages/types/src/index.ts ---
//	..."
//
// Callers pass this to buildSOWNativePromptsWithOpts via a new
// promptOpts.GitContext field.
func AssembleRepairContext(repoRoot string, files []string, totalBudget int) string {
	if repoRoot == "" || len(files) == 0 {
		return ""
	}
	if totalBudget <= 0 {
		totalBudget = defaultMaxDiffByte
	}

	// Dedup files while preserving order.
	seen := make(map[string]bool, len(files))
	ordered := make([]string, 0, len(files))
	for _, f := range files {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		ordered = append(ordered, f)
	}

	// Give each file a roughly equal slice of the diff budget. Leave
	// headroom for the header and per-file framing.
	const headerBytes = 256
	remaining := totalBudget - headerBytes
	if remaining < 256 {
		remaining = 256
	}
	perFileDiff := remaining / max(len(ordered), 1)
	if perFileDiff < 300 {
		perFileDiff = 300
	}

	var b strings.Builder
	var wroteAny bool

	for _, f := range ordered {
		hist, err := AssembleFileHistory(repoRoot, f, defaultMaxCommits, perFileDiff)
		if err != nil || hist == nil || len(hist.RecentCommits) == 0 {
			continue
		}

		if !wroteAny {
			b.WriteString("RECENT CHANGES TO FILES YOU'RE ABOUT TO REPAIR (do NOT re-introduce bugs these commits fixed):\n\n")
			wroteAny = true
		}

		fmt.Fprintf(&b, "--- %s ---\n", hist.Path)
		b.WriteString("commits:\n")
		for _, c := range hist.RecentCommits {
			fmt.Fprintf(&b, "  %s %s: %s (%s)\n", c.Hash, c.Author, c.Subject, c.Date)
		}
		fmt.Fprintf(&b, "diff (last %d commits):\n", len(hist.RecentCommits))
		b.WriteString(hist.RecentDiff)
		if !strings.HasSuffix(hist.RecentDiff, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")

		// Hard cap overall budget. Truncate visibly if we overflow.
		if b.Len() >= totalBudget {
			break
		}
	}

	if !wroteAny {
		return ""
	}

	out := b.String()
	if len(out) > totalBudget {
		out = out[:totalBudget] + "\n... [truncated]\n"
	}
	return out
}

// --- helpers ---

// runGit executes `git <args...>` in repoRoot with a 5s timeout and
// returns stdout. Exit code != 0 produces an error unless the stderr
// is one of the well-known "no history" signals, in which case stdout
// is returned as-is (typically empty).
func runGit(repoRoot string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitInvocationTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// git returns non-zero for some benign cases (empty log,
		// unknown path, etc.). Surface these as empty output.
		msg := stderr.String()
		if strings.Contains(msg, "unknown revision") ||
			strings.Contains(msg, "does not have any commits yet") ||
			strings.Contains(msg, "ambiguous argument") {
			return "", nil
		}
		if ctx.Err() == context.DeadlineExceeded {
			// Timeout is a silent no-op, not an error.
			return "", nil
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(msg))
	}
	return string(out), nil
}

// isInsideGitRepo returns true when repoRoot is inside a git working
// tree. Any failure (not a repo, git missing, timeout) is treated as
// "no", which lets upstream callers fail closed.
func isInsideGitRepo(repoRoot string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), gitInvocationTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// parseCommitLog parses `git log --format=%h|%an|%ar|%s` output into
// GitCommitInfo records. Malformed lines are skipped.
func parseCommitLog(raw string) []GitCommitInfo {
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	commits := make([]GitCommitInfo, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		commits = append(commits, GitCommitInfo{
			Hash:    parts[0],
			Author:  parts[1],
			Date:    parts[2],
			Subject: parts[3],
		})
	}
	return commits
}

// reverseCommits flips the slice in place.
func reverseCommits(cs []GitCommitInfo) {
	for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
		cs[i], cs[j] = cs[j], cs[i]
	}
}

// truncateBytes trims s to at most n bytes, appending a visible
// "[truncated]" marker when truncation happened.
func truncateBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "\n... [truncated]\n"
}

// max is a tiny local helper so we don't depend on Go 1.21's builtin
// being the same shape everywhere the repo builds.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
