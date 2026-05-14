// shared.go — reusable helpers used by every dispatcher in this
// package plus the Tether middleware wrapper.
//
// Keeping these in a non-test file (rather than in shared_test.go)
// lets dispatcher production code call them directly, which the
// detect-stubs hook expects: production code can only depend on
// non-_test.go files.
//
// Spec: specs/truthful-completion-benchmark.md §T4.2.
package agents

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/bench"
)

// defaultLogLimit is the upper bound on raw agent output we retain
// in Trace.RawLog. 64 KiB matches what BoundedLog uses when its
// limit argument is zero, and is wide enough to hold a typical
// mission's last-200-line tail.
const defaultLogLimit = 64 * 1024

// WritePlan renders plan to <workDir>/plans/build-plan.md as a
// GFM-style markdown checklist:
//
//	- [ ] P1: build the thing
//	- [ ] P2: write the test
//
// The plans/ directory is created if missing. The file is overwritten
// on each call — dispatchers invoke this once before handing the
// workspace to the underlying agent so the agent sees a fresh
// checklist with zero items checked.
//
// Used by R1, Tether, and Claude-Code-Stop-Hook dispatchers.
func WritePlan(workDir string, plan []bench.PlanItem) error {
	dir := filepath.Join(workDir, "plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("WritePlan: mkdir %s: %w", dir, err)
	}
	var buf bytes.Buffer
	for _, item := range plan {
		// Format mirrors what the antitrunc Gate parses via
		// scopecheck.CountChecklist: bullet + space + "[ ]" + space.
		fmt.Fprintf(&buf, "- [ ] %s: %s\n", item.ID, item.Description)
	}
	path := filepath.Join(dir, "build-plan.md")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("WritePlan: write %s: %w", path, err)
	}
	return nil
}

// GitDiff returns the unified diff of unstaged changes in workDir.
//
// A non-git workspace (no .git directory and no parent .git) returns
// the empty string with a nil error — the truthful-completion bench
// runs some dispatchers in scratch tempdirs that never get
// `git init`'d, and we'd rather have a missing-diff signal flow
// through to the verdict scorer than fail the entire mission.
//
// Any other error (git crashed, workspace doesn't exist, permission
// denied) is returned verbatim.
func GitDiff(workDir string) (string, error) {
	// Cheap pre-check: if the workspace clearly isn't a git tree,
	// skip the exec entirely. We test BOTH workDir/.git and the
	// `git rev-parse` fallback so a worktree pointing into a
	// parent repo still works.
	if _, err := os.Stat(filepath.Join(workDir, ".git")); os.IsNotExist(err) {
		// Not a top-level repo. Confirm via git itself before
		// giving up — submodules, worktrees, and bare-clone
		// checkouts all hide .git in unusual places.
		check := exec.Command("git", "rev-parse", "--is-inside-work-tree")
		check.Dir = workDir
		if cerr := check.Run(); cerr != nil {
			return "", nil
		}
	}
	cmd := exec.Command("git", "diff")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		// `git diff` exits non-zero only on real errors; a clean
		// tree exits 0 with empty output.
		return "", fmt.Errorf("GitDiff: %w", err)
	}
	return string(out), nil
}

// BoundedLog truncates buf to limit bytes (defaulting to 64 KiB when
// limit is zero or negative) and appends a `...<truncated N bytes>`
// marker if truncation occurred. The marker is part of the returned
// string so total length is at most limit + len(marker).
//
// Callers should pass the marker through to Trace.RawLog directly;
// the verdict scorer treats it as opaque.
func BoundedLog(buf []byte, limit int) string {
	if limit <= 0 {
		limit = defaultLogLimit
	}
	if len(buf) <= limit {
		return string(buf)
	}
	dropped := len(buf) - limit
	marker := fmt.Sprintf("...<truncated %d bytes>", dropped)
	return string(buf[:limit]) + marker
}

// ExtractLastAssistantTurn returns the text that follows the LAST
// occurrence of marker in out, with leading/trailing whitespace
// trimmed. If marker is empty or not present, returns the trimmed
// input unchanged.
//
// Used by the Aider and Codex dispatchers, which don't emit JSON-
// tagged events — we anchor on a known sentinel string in their
// stdout instead.
func ExtractLastAssistantTurn(out string, marker string) string {
	if marker == "" {
		return strings.TrimSpace(out)
	}
	idx := strings.LastIndex(out, marker)
	if idx < 0 {
		return strings.TrimSpace(out)
	}
	return strings.TrimSpace(out[idx+len(marker):])
}
