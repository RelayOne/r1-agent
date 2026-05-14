// shared_test.go — coverage for the four helpers in shared.go.
//
// Spec: specs/truthful-completion-benchmark.md §T4.2 checklist
// item 18 (five tests). The CountChecklist round-trip pins the
// markdown shape WritePlan emits against the same parser the
// antitrunc Gate uses in production, so a future drift in either
// surface fails this test before it ships.
package agents

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/antitrunc"
	"github.com/RelayOne/r1/internal/bench"
)

// TestWritePlan_RoundTrip writes a 3-item plan, re-reads the file,
// and parses it via antitrunc.CountChecklist. We assert the parser
// agrees on the count — this is the contract between WritePlan and
// the Gate.
func TestWritePlan_RoundTrip(t *testing.T) {
	workDir := t.TempDir()
	plan := []bench.PlanItem{
		{ID: "P1", Description: "build the thing"},
		{ID: "P2", Description: "write the test"},
		{ID: "P3", Description: "ship it"},
	}
	if err := WritePlan(workDir, plan); err != nil {
		t.Fatalf("WritePlan: %v", err)
	}
	path := filepath.Join(workDir, "plans", "build-plan.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	done, total := antitrunc.CountChecklist(string(data))
	if done != 0 || total != 3 {
		t.Fatalf("CountChecklist = (done=%d, total=%d), want (0, 3); file:\n%s",
			done, total, string(data))
	}
}

// TestGitDiff_NonGitWorkspaceReturnsEmpty confirms the
// non-git-tolerant path: a bare tempdir with no .git should return
// "", nil rather than the noisy git error message.
func TestGitDiff_NonGitWorkspaceReturnsEmpty(t *testing.T) {
	workDir := t.TempDir()
	out, err := GitDiff(workDir)
	if err != nil {
		t.Fatalf("GitDiff returned error for non-git workspace: %v", err)
	}
	if out != "" {
		t.Fatalf("GitDiff for non-git workspace: got %q, want empty string", out)
	}
}

// TestGitDiff_CapturesUnstagedChanges initializes a real git repo,
// commits a file, modifies it, and asserts GitDiff returns a
// non-empty unified diff that mentions the modified line.
//
// Skips on machines without git installed (CI containers should
// always have it, dev laptops usually do).
func TestGitDiff_CapturesUnstagedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	workDir := t.TempDir()

	// init repo + identity (git refuses to commit without one).
	runGit(t, workDir, "init", "-q")
	runGit(t, workDir, "config", "user.email", "bench@example.com")
	runGit(t, workDir, "config", "user.name", "Bench Bot")

	// commit baseline file.
	target := filepath.Join(workDir, "hello.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	runGit(t, workDir, "add", "hello.txt")
	runGit(t, workDir, "commit", "-q", "-m", "init")

	// modify so there's something for `git diff` to show.
	if err := os.WriteFile(target, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	out, err := GitDiff(workDir)
	if err != nil {
		t.Fatalf("GitDiff: %v", err)
	}
	if out == "" {
		t.Fatalf("GitDiff returned empty string for modified workspace")
	}
	if !strings.Contains(out, "+world") {
		t.Fatalf("GitDiff missing expected `+world` line; got:\n%s", out)
	}
}

// TestBoundedLog_TruncatesAt64KiB feeds 100 KiB of bytes and asserts
// the result is bounded by 64 KiB plus the truncation-marker
// envelope (worst case is around 30 chars: "...<truncated 36864
// bytes>").
func TestBoundedLog_TruncatesAt64KiB(t *testing.T) {
	buf := bytes.Repeat([]byte("a"), 100*1024)
	got := BoundedLog(buf, 0) // default = 64 KiB.
	const maxLen = 64*1024 + 50
	if len(got) > maxLen {
		t.Fatalf("BoundedLog length = %d, want <= %d", len(got), maxLen)
	}
	if !strings.Contains(got, "...<truncated") {
		t.Fatalf("BoundedLog missing truncation marker; tail = %q",
			got[len(got)-min(80, len(got)):])
	}
}

// TestExtractLastAssistantTurn_SimpleMarker pins the contract used
// by the Aider and Codex dispatchers: take everything after the
// LAST sentinel, trim whitespace, return.
func TestExtractLastAssistantTurn_SimpleMarker(t *testing.T) {
	const input = "foo\n--- last ---\nbar baz\n"
	got := ExtractLastAssistantTurn(input, "--- last ---")
	if got != "bar baz" {
		t.Fatalf("ExtractLastAssistantTurn = %q, want %q", got, "bar baz")
	}
}

// runGit is a test helper that runs a git subcommand in dir and
// fatals the test on non-zero exit. Centralized so the
// repo-init test can stay readable.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

// min is the local fallback for Go versions that don't expose the
// builtin. Removable once the module is on go >= 1.21 everywhere.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
