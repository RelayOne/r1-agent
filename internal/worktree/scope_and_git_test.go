package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// initTestRepo creates a new git repo at dir with a single commit that
// writes "seed" to "seed.txt". Returns the commit SHA.
func initTestRepo(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "test"},
		{"config", "user.email", "test@example.com"},
		{"commit", "--allow-empty", "-m", "root"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// Add seed.txt as base content so subsequent diffs have something to diff.
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	for _, args := range [][]string{
		{"add", "seed.txt"},
		{"commit", "-m", "seed"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// Capture HEAD
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// newTestHandle creates a fresh git repo and returns a Handle pointing
// at it with BaseCommit = HEAD.
func newTestHandle(t *testing.T) (Handle, string) {
	t.Helper()
	dir := t.TempDir()
	base := initTestRepo(t, dir)
	return Handle{
		Name:       "test-wt",
		Branch:     "main",
		Path:       dir,
		BaseCommit: base,
		RepoRoot:   dir,
		GitBinary:  "git",
	}, base
}

// TestScopeCheck_EmptyAllowedMeansNoViolations documents the "no scope
// set" behavior: when allowed is empty, every file passes (no violations).
func TestScopeCheck_EmptyAllowedMeansNoViolations(t *testing.T) {
	got := ScopeCheck([]string{"a.go", "b.go", "some/deep/path.go"}, nil)
	if got != nil {
		t.Errorf("ScopeCheck with empty allowed = %v, want nil (no restrictions)", got)
	}
}

// TestScopeCheck_ExactFilesOnly verifies exact-file matching: listed
// files pass, unlisted files show up as violations.
func TestScopeCheck_ExactFilesOnly(t *testing.T) {
	allowed := []string{"a.go", "b.go"}
	files := []string{"a.go", "c.go", "b.go", "d.go"}
	got := ScopeCheck(files, allowed)
	sort.Strings(got)
	want := []string{"c.go", "d.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("violations = %v, want %v", got, want)
	}
}

// TestScopeCheck_DirPrefix verifies that allowed entries ending in "/"
// match any file under that directory.
func TestScopeCheck_DirPrefix(t *testing.T) {
	allowed := []string{"internal/worktree/", "cmd/r1/main.go"}
	files := []string{
		"internal/worktree/helpers.go",  // OK (dir prefix)
		"internal/worktree/sub/deep.go", // OK (dir prefix)
		"cmd/r1/main.go",             // OK (exact)
		"cmd/r1/other.go",            // VIOLATION (not the exact match)
		"internal/other/pkg.go",         // VIOLATION (outside prefix)
	}
	got := ScopeCheck(files, allowed)
	sort.Strings(got)
	want := []string{"cmd/r1/other.go", "internal/other/pkg.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("violations = %v, want %v", got, want)
	}
}

// TestScopeCheck_PartialPrefixDoesNotMatch confirms that "internal/wt"
// does NOT match "internal/wt-other/x.go" when the allowed entry is
// the dir-style "internal/wt/" (trailing slash anchors it).
func TestScopeCheck_PartialPrefixDoesNotMatch(t *testing.T) {
	allowed := []string{"internal/wt/"}
	files := []string{"internal/wt-other/x.go"}
	got := ScopeCheck(files, allowed)
	if len(got) != 1 || got[0] != "internal/wt-other/x.go" {
		t.Errorf("violations = %v, want single 'internal/wt-other/x.go' (trailing slash must not partial-match)", got)
	}
}

// TestModifiedFiles_CapturesUntrackedAndUnstaged sets up a worktree
// with one untracked file, one staged modification, and one unstaged
// modification, and verifies ModifiedFiles reports all three.
func TestModifiedFiles_CapturesUntrackedAndUnstaged(t *testing.T) {
	h, _ := newTestHandle(t)
	ctx := context.Background()

	// Unstaged modification to tracked file
	if err := os.WriteFile(filepath.Join(h.Path, "seed.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatalf("modify seed: %v", err)
	}
	// Staged new file
	if err := os.WriteFile(filepath.Join(h.Path, "staged.txt"), []byte("new-staged"), 0o644); err != nil {
		t.Fatalf("write staged: %v", err)
	}
	cmd := exec.Command("git", "add", "staged.txt")
	cmd.Dir = h.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	// Untracked file (never git add'd)
	if err := os.WriteFile(filepath.Join(h.Path, "untracked.txt"), []byte("untracked"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	got, err := ModifiedFiles(ctx, h)
	if err != nil {
		t.Fatalf("ModifiedFiles: %v", err)
	}
	sort.Strings(got)
	want := []string{"seed.txt", "staged.txt", "untracked.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ModifiedFiles = %v, want %v", got, want)
	}
}

// TestModifiedFilesList_NoErrorReturnsEmptyWrap verifies the wrapper
// swallows errors and returns the file list (never an error).
func TestModifiedFilesList_NoErrorReturnsEmptyWrap(t *testing.T) {
	h, _ := newTestHandle(t)
	// Clean repo: no modifications
	got := ModifiedFilesList(context.Background(), h)
	if len(got) != 0 {
		t.Errorf("ModifiedFilesList on clean repo = %v, want empty", got)
	}

	// Non-git path: should return nil rather than panic
	bad := Handle{Path: t.TempDir(), GitBinary: "git", BaseCommit: "HEAD"}
	bogus := ModifiedFilesList(context.Background(), bad)
	if bogus != nil {
		t.Errorf("ModifiedFilesList on non-git path = %v, want nil", bogus)
	}
}

// TestOverlappingFiles_Intersection verifies OverlappingFiles returns
// only the files modified by BOTH handles.
func TestOverlappingFiles_Intersection(t *testing.T) {
	a, _ := newTestHandle(t)
	b, _ := newTestHandle(t)

	// Both repos modify seed.txt; only A modifies extra.txt
	if err := os.WriteFile(filepath.Join(a.Path, "seed.txt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Path, "a-only.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Path, "seed.txt"), []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Path, "b-only.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	overlap := OverlappingFiles(context.Background(), a, b)
	if len(overlap) != 1 || overlap[0] != "seed.txt" {
		t.Errorf("overlap = %v, want [seed.txt]", overlap)
	}
}

// TestDiffSummary_ReportsTrackedAndUntracked verifies DiffSummary
// combines tracked-change stats with an untracked-file list.
func TestDiffSummary_ReportsTrackedAndUntracked(t *testing.T) {
	h, _ := newTestHandle(t)
	// Unstaged modification to tracked file
	if err := os.WriteFile(filepath.Join(h.Path, "seed.txt"), []byte("updated seed content"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Untracked file
	if err := os.WriteFile(filepath.Join(h.Path, "newfile.md"), []byte("docs"), 0o644); err != nil {
		t.Fatal(err)
	}

	summary := DiffSummary(context.Background(), h)
	if !strings.Contains(summary, "1 new file(s)") {
		t.Errorf("DiffSummary = %q, want to mention '1 new file(s)'", summary)
	}
	if !strings.Contains(summary, "newfile.md") {
		t.Errorf("DiffSummary = %q, want to mention 'newfile.md'", summary)
	}
	// DiffSummary falls back to "(diff unavailable)" only when both
	// parts are empty; with an untracked file it must NOT return that.
	if strings.Contains(summary, "(diff unavailable)") {
		t.Errorf("DiffSummary = %q, should not be unavailable when changes exist", summary)
	}
}

// TestDiffSummary_UnavailableWhenClean verifies the fallback message
// when the repo has no changes vs base.
func TestDiffSummary_UnavailableWhenClean(t *testing.T) {
	h, _ := newTestHandle(t)
	summary := DiffSummary(context.Background(), h)
	if summary != "(diff unavailable)" {
		t.Errorf("DiffSummary(clean) = %q, want '(diff unavailable)'", summary)
	}
}

// TestMainHeadSHA_ReturnsCurrentHEAD verifies MainHeadSHA returns the
// actual HEAD SHA for an initialized repo.
func TestMainHeadSHA_ReturnsCurrentHEAD(t *testing.T) {
	dir := t.TempDir()
	expected := initTestRepo(t, dir)

	got := MainHeadSHA(context.Background(), dir)
	if got != expected {
		t.Errorf("MainHeadSHA = %q, want %q", got, expected)
	}
}

// TestMainHeadSHA_EmptyOnBadRepo verifies MainHeadSHA returns empty
// string for a non-git directory instead of panicking.
func TestMainHeadSHA_EmptyOnBadRepo(t *testing.T) {
	dir := t.TempDir()
	got := MainHeadSHA(context.Background(), dir)
	if got != "" {
		t.Errorf("MainHeadSHA on non-git dir = %q, want empty", got)
	}
}

// TestTreeSHA_StableAcrossIdenticalContent verifies TreeSHA returns a
// 40-char hash and is stable when content hasn't changed between calls.
func TestTreeSHA_StableAcrossIdenticalContent(t *testing.T) {
	h, _ := newTestHandle(t)
	ctx := context.Background()

	first, err := TreeSHA(ctx, h)
	if err != nil {
		t.Fatalf("first TreeSHA: %v", err)
	}
	if len(first) != 40 {
		t.Errorf("TreeSHA len = %d, want 40 hex chars: %q", len(first), first)
	}

	second, err := TreeSHA(ctx, h)
	if err != nil {
		t.Fatalf("second TreeSHA: %v", err)
	}
	if first != second {
		t.Errorf("TreeSHA changed between calls without edits: %q vs %q", first, second)
	}
}

// TestConflicts_SnapshotIsCopy verifies that Conflicts() returns a
// snapshot slice that is independent of the scanner's internal state
// (mutating the returned slice must not affect later calls).
func TestConflicts_SnapshotIsCopy(t *testing.T) {
	mgr := &Manager{RepoRoot: t.TempDir(), GitBinary: "git"}
	cs := NewConflictScanner(mgr)
	cs.mu.Lock()
	cs.conflicts = []ConflictPair{
		{WorktreeA: "wt-1", WorktreeB: "wt-2", Files: []string{"f.go"}},
		{WorktreeA: "wt-2", WorktreeB: "wt-3", Files: []string{"g.go"}},
	}
	cs.mu.Unlock()

	snap := cs.Conflicts()
	if len(snap) != 2 {
		t.Fatalf("snap len = %d, want 2", len(snap))
	}
	if snap[0].WorktreeA != "wt-1" {
		t.Errorf("snap[0].WorktreeA = %q, want wt-1", snap[0].WorktreeA)
	}

	// Mutate returned snapshot
	snap[0].WorktreeA = "MUTATED"

	// Re-read: scanner's internal state must be unchanged
	snap2 := cs.Conflicts()
	if snap2[0].WorktreeA != "wt-1" {
		t.Errorf("snap2[0].WorktreeA = %q, want wt-1 (scanner state was leaked)", snap2[0].WorktreeA)
	}
}
