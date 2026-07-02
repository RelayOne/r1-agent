package worktree

// merge_conflict_test.go — conflict-path coverage for Manager.Merge,
// ConflictScanner/detectConflicts, and the auto-resolution helpers
// (allAutoResolved, applyConflictResolutions), which previously had
// 0.0% coverage (audit A013).
//
// Note on scope: at HEAD, Manager.Merge parses `git merge-tree
// --write-tree` output for conflict markers, which that command never
// emits, so the auto-resolution branch inside Merge is unreachable and
// every conflicting merge hard-fails. These tests pin (a) the hard-fail
// path leaving main untouched with no merge state, and (b) the
// auto-resolution machinery driven directly against a REAL conflicted
// git working tree — the contract the future Merge re-wiring must
// preserve. An end-to-end "auto-resolvable conflict merges via
// Manager.Merge" test can only land together with that production
// repair.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/conflictres"
)

// initMergeTestRepo creates a temp git repo on branch main with a
// committed identity config (so Manager-driven git commit works
// without env injection) and returns the repo dir plus a runner that
// executes git in an arbitrary directory.
func initMergeTestRepo(t *testing.T) (string, func(dir string, args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append([]string{"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com"}, os.Environ()...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(repo, "init", "-b", "main")
	// Repo-local identity: Manager runs its own git commands without
	// the env overrides above, so commit/merge inside Manager needs
	// the config to be present in the repo itself.
	run(repo, "config", "user.name", "test")
	run(repo, "config", "user.email", "test@example.com")
	run(repo, "config", "commit.gpgsign", "false")
	return repo, run
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestMergeUnresolvableConflictFailsAndLeavesMainClean forces a real
// two-branch semantic conflict through Manager.Merge and asserts the
// failure contract: an error is returned, no merge state (MERGE_HEAD)
// is left behind, the tracked tree is clean, and main's HEAD and file
// content are untouched.
func TestMergeUnresolvableConflictFailsAndLeavesMainClean(t *testing.T) {
	ctx := context.Background()
	repo, run := initMergeTestRepo(t)

	writeRepoFile(t, repo, "conflict.txt", "alpha\nshared line\nomega\n")
	run(repo, "add", "conflict.txt")
	run(repo, "commit", "-m", "base")

	m := NewManager(repo)
	h, err := m.Prepare(ctx, "conflict-hard")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = m.Cleanup(ctx, h) }()

	// Worktree side: rewrite the shared line one way.
	writeRepoFile(t, h.Path, "conflict.txt", "alpha\nworktree rewrote this line entirely\nomega\n")
	run(h.Path, "add", "conflict.txt")
	run(h.Path, "commit", "-m", "worktree change")

	// Main side: rewrite the same line a semantically different way.
	mainContent := "alpha\nmain rewrote it with different logic\nomega\n"
	writeRepoFile(t, repo, "conflict.txt", mainContent)
	run(repo, "add", "conflict.txt")
	run(repo, "commit", "-m", "main change")

	headBefore := run(repo, "rev-parse", "HEAD")

	if err := m.Merge(ctx, h, "merge conflict-hard"); err == nil {
		t.Fatal("Merge succeeded on a semantic conflict; want error")
	}

	// No merge-in-progress state may survive a failed merge.
	if _, statErr := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); statErr == nil {
		t.Error("MERGE_HEAD exists after failed Merge — merge state not aborted")
	}
	// Tracked working tree must be clean (untracked .r1/worktrees is fine).
	if dirty := run(repo, "status", "--porcelain", "--untracked-files=no"); dirty != "" {
		t.Errorf("tracked tree dirty after failed Merge:\n%s", dirty)
	}
	// Main must be untouched: same HEAD, same content.
	if headAfter := run(repo, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Errorf("main HEAD moved across failed Merge: before=%s after=%s", headBefore, headAfter)
	}
	got, err := os.ReadFile(filepath.Join(repo, "conflict.txt"))
	if err != nil {
		t.Fatalf("read conflict.txt: %v", err)
	}
	if string(got) != mainContent {
		t.Errorf("main file content changed by failed Merge:\ngot:  %q\nwant: %q", got, mainContent)
	}
}

// NOTE — positive ConflictScanner path (overlapping worktrees → 1
// pair) is intentionally ABSENT: it cannot pass at HEAD.
// KNOWN DEFECT found while writing these tests (production fix is out
// of scope for this tests-only change): detectConflicts
// (conflicts.go) invokes `git merge-tree --write-tree <base> <headA>
// <headB>` — git rejects the 3-argument --write-tree form with a
// usage error (exit 129, verified on git 2.48), so Scan returns zero
// pairs even for a guaranteed same-line conflict. The valid forms are
// `merge-tree --write-tree <headA> <headB>` or
// `--merge-base=<base>`. Once conflicts.go is repaired, add the
// positive-path test here (two worktrees editing the same line →
// exactly one ConflictPair naming the file).

// TestConflictScannerNoOverlap is the negative control: worktrees
// touching disjoint files must produce zero conflict pairs. (At HEAD
// this passes through the defective merge-tree invocation described
// above; the assertion also pins the post-repair contract.)
func TestConflictScannerNoOverlap(t *testing.T) {
	ctx := context.Background()
	repo, run := initMergeTestRepo(t)

	writeRepoFile(t, repo, "base.txt", "base\n")
	run(repo, "add", "base.txt")
	run(repo, "commit", "-m", "base")

	m := NewManager(repo)
	a, err := m.Prepare(ctx, "wt-c")
	if err != nil {
		t.Fatalf("Prepare a: %v", err)
	}
	defer func() { _ = m.Cleanup(ctx, a) }()
	b, err := m.Prepare(ctx, "wt-d")
	if err != nil {
		t.Fatalf("Prepare b: %v", err)
	}
	defer func() { _ = m.Cleanup(ctx, b) }()

	writeRepoFile(t, a.Path, "only-a.txt", "a\n")
	run(a.Path, "add", "only-a.txt")
	run(a.Path, "commit", "-m", "a file")

	writeRepoFile(t, b.Path, "only-b.txt", "b\n")
	run(b.Path, "add", "only-b.txt")
	run(b.Path, "commit", "-m", "b file")

	cs := NewConflictScanner(m)
	if pairs := cs.Scan(ctx, []Handle{a, b}); len(pairs) != 0 {
		t.Errorf("Scan: got %d conflict pairs for disjoint edits, want 0 (%+v)", len(pairs), pairs)
	}
	if cs.HasConflicts() {
		t.Error("HasConflicts() = true for disjoint edits")
	}
}

// TestDetectConflictsMissingBase pins detectConflicts' guard: handles
// without captured base commits must error, not silently scan nothing.
func TestDetectConflictsMissingBase(t *testing.T) {
	repo, _ := initMergeTestRepo(t)
	m := NewManager(repo)
	_, err := detectConflicts(context.Background(), m, Handle{Name: "a"}, Handle{Name: "b"})
	if err == nil {
		t.Fatal("detectConflicts with empty BaseCommit: want error, got nil")
	}
	if !strings.Contains(err.Error(), "missing base commits") {
		t.Errorf("error = %q, want mention of missing base commits", err)
	}
}

// (parseConflictFiles itself is covered by conflicts_test.go.)

// TestValidateMergeCleanAndConflict covers both branches of
// ValidateMerge: a cleanly mergeable branch and a conflicting one.
func TestValidateMergeCleanAndConflict(t *testing.T) {
	ctx := context.Background()
	repo, run := initMergeTestRepo(t)

	writeRepoFile(t, repo, "v.txt", "one\ntwo\nthree\n")
	run(repo, "add", "v.txt")
	run(repo, "commit", "-m", "base")

	// Clean branch: adds a new file only.
	run(repo, "checkout", "-b", "clean-branch")
	writeRepoFile(t, repo, "new.txt", "new\n")
	run(repo, "add", "new.txt")
	run(repo, "commit", "-m", "clean addition")
	run(repo, "checkout", "main")

	// Conflicting branch: same line changed on both sides.
	run(repo, "checkout", "-b", "conflict-branch")
	writeRepoFile(t, repo, "v.txt", "one\nbranch side\nthree\n")
	run(repo, "add", "v.txt")
	run(repo, "commit", "-m", "branch change")
	run(repo, "checkout", "main")
	writeRepoFile(t, repo, "v.txt", "one\nmain side\nthree\n")
	run(repo, "add", "v.txt")
	run(repo, "commit", "-m", "main change")

	clean := Handle{GitBinary: "git", RepoRoot: repo, Branch: "clean-branch"}
	if err := ValidateMerge(ctx, clean); err != nil {
		t.Errorf("ValidateMerge(clean-branch): %v, want nil", err)
	}
	conflicting := Handle{GitBinary: "git", RepoRoot: repo, Branch: "conflict-branch"}
	err := ValidateMerge(ctx, conflicting)
	if err == nil {
		t.Fatal("ValidateMerge(conflict-branch): want error, got nil")
	}
	if !strings.Contains(err.Error(), "merge conflict") {
		t.Errorf("error = %q, want mention of merge conflict", err)
	}
}

// TestResetMainTo covers the rollback helper: it must hard-reset a
// clean tree to the given SHA, and must REFUSE to reset when the
// working tree is dirty (data-loss guard).
func TestResetMainTo(t *testing.T) {
	ctx := context.Background()
	repo, run := initMergeTestRepo(t)

	writeRepoFile(t, repo, "r.txt", "v1\n")
	run(repo, "add", "r.txt")
	run(repo, "commit", "-m", "first")
	first := run(repo, "rev-parse", "HEAD")

	writeRepoFile(t, repo, "r.txt", "v2\n")
	run(repo, "add", "r.txt")
	run(repo, "commit", "-m", "second")

	// Clean tree: reset succeeds.
	ResetMainTo(ctx, repo, first)
	if head := run(repo, "rev-parse", "HEAD"); head != first {
		t.Errorf("ResetMainTo on clean tree: HEAD = %s, want %s", head, first)
	}
	got, err := os.ReadFile(filepath.Join(repo, "r.txt"))
	if err != nil || string(got) != "v1\n" {
		t.Errorf("after reset, r.txt = %q err=%v, want v1", got, err)
	}

	// Dirty tree: reset must refuse and preserve uncommitted content.
	writeRepoFile(t, repo, "r.txt", "v2\n")
	run(repo, "add", "r.txt")
	run(repo, "commit", "-m", "second again")
	second := run(repo, "rev-parse", "HEAD")
	writeRepoFile(t, repo, "r.txt", "uncommitted work\n")
	ResetMainTo(ctx, repo, first)
	if head := run(repo, "rev-parse", "HEAD"); head != second {
		t.Errorf("ResetMainTo on dirty tree moved HEAD to %s, want unchanged %s", head, second)
	}
	got, err = os.ReadFile(filepath.Join(repo, "r.txt"))
	if err != nil || string(got) != "uncommitted work\n" {
		t.Errorf("dirty-tree guard failed: r.txt = %q err=%v, want uncommitted work preserved", got, err)
	}
}

// TestAllAutoResolved pins the helper's contract: empty input is NOT
// "all resolved" (the caller must not take the auto-resolution branch
// with zero conflicts), and one unresolved conflict poisons the set.
func TestAllAutoResolved(t *testing.T) {
	if allAutoResolved(nil) {
		t.Error("allAutoResolved(nil) = true, want false")
	}
	if allAutoResolved([]conflictres.Conflict{{AutoResolved: true}, {AutoResolved: false}}) {
		t.Error("allAutoResolved(mixed) = true, want false")
	}
	if !allAutoResolved([]conflictres.Conflict{{AutoResolved: true}, {AutoResolved: true}}) {
		t.Error("allAutoResolved(all resolved) = false, want true")
	}
}

// TestApplyConflictResolutionsCompletesMerge builds a REAL conflicted
// merge (both branches add a different import line at the same spot),
// parses the working-tree conflict markers, auto-resolves them
// (import-union), and drives applyConflictResolutions end-to-end:
// resolved content written, staged, and the merge commit completed.
func TestApplyConflictResolutionsCompletesMerge(t *testing.T) {
	ctx := context.Background()
	repo, run := initMergeTestRepo(t)

	base := "import (\n\t\"fmt\"\n)\n"
	writeRepoFile(t, repo, "imports.txt", base)
	writeRepoFile(t, repo, "clean.txt", "untouched\n")
	run(repo, "add", "imports.txt", "clean.txt")
	run(repo, "commit", "-m", "base")

	// Feature branch adds "os"; main adds "strings" at the same line.
	run(repo, "checkout", "-b", "feature")
	writeRepoFile(t, repo, "imports.txt", "import (\n\t\"fmt\"\n\t\"os\"\n)\n")
	run(repo, "add", "imports.txt")
	run(repo, "commit", "-m", "feature adds os")
	run(repo, "checkout", "main")
	writeRepoFile(t, repo, "imports.txt", "import (\n\t\"fmt\"\n\t\"strings\"\n)\n")
	run(repo, "add", "imports.txt")
	run(repo, "commit", "-m", "main adds strings")

	// Force the real conflicted-merge state applyConflictResolutions
	// operates on (git merge fails, leaves markers + MERGE_HEAD).
	mergeCmd := exec.Command("git", "merge", "feature", "-m", "merge feature")
	mergeCmd.Dir = repo
	if out, err := mergeCmd.CombinedOutput(); err == nil {
		t.Fatalf("git merge unexpectedly succeeded — fixture broken:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("fixture broken: no MERGE_HEAD after conflicting merge: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(repo, "imports.txt"))
	if err != nil {
		t.Fatalf("read conflicted file: %v", err)
	}
	if !strings.Contains(string(raw), "<<<<<<<") {
		t.Fatalf("fixture broken: no conflict markers in imports.txt:\n%s", raw)
	}

	conflicts := conflictres.Parse(string(raw), "imports.txt")
	if len(conflicts) != 1 {
		t.Fatalf("Parse: got %d conflicts, want 1 (%+v)", len(conflicts), conflicts)
	}
	if conflicts[0].Kind != conflictres.KindImport {
		t.Fatalf("conflict kind = %q, want %q", conflicts[0].Kind, conflictres.KindImport)
	}
	conflictres.AutoResolve(conflicts)
	if !allAutoResolved(conflicts) {
		t.Fatalf("import conflict not auto-resolved: %+v", conflicts)
	}

	// Include a second entry for a file WITHOUT markers to cover the
	// stage-as-is guard branch (merge-tree/git-merge strategy mismatch).
	conflicts = append(conflicts, conflictres.Conflict{
		File:         "clean.txt",
		Kind:         conflictres.KindWhitespace,
		Ours:         "untouched",
		Theirs:       "untouched",
		Resolved:     "untouched",
		AutoResolved: true,
	})

	m := NewManager(repo)
	if err := m.applyConflictResolutions(ctx, Handle{Name: "apply-test"}, conflicts); err != nil {
		t.Fatalf("applyConflictResolutions: %v", err)
	}

	// Merge must be completed: MERGE_HEAD gone, HEAD is a 2-parent commit.
	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); err == nil {
		t.Error("MERGE_HEAD still present — merge commit not completed")
	}
	parents := strings.Fields(run(repo, "rev-list", "--parents", "-n1", "HEAD"))
	if len(parents) != 3 { // self + 2 parents
		t.Errorf("HEAD has %d parents, want 2 (rev-list fields: %v)", len(parents)-1, parents)
	}

	// Resolved content: union of both import lines, sorted, no markers.
	resolved, err := os.ReadFile(filepath.Join(repo, "imports.txt"))
	if err != nil {
		t.Fatalf("read resolved file: %v", err)
	}
	want := "import (\n\t\"fmt\"\n\t\"os\"\n\t\"strings\"\n)\n"
	if string(resolved) != want {
		t.Errorf("resolved imports.txt:\ngot:  %q\nwant: %q", resolved, want)
	}
	cleanGot, err := os.ReadFile(filepath.Join(repo, "clean.txt"))
	if err != nil {
		t.Fatalf("read clean.txt: %v", err)
	}
	if string(cleanGot) != "untouched\n" {
		t.Errorf("clean.txt modified by stage-as-is branch: %q", cleanGot)
	}
}

// TestApplyConflictResolutionsMissingFile pins the error path: a
// conflict entry pointing at a nonexistent file must fail loudly, not
// silently commit a partial merge.
func TestApplyConflictResolutionsMissingFile(t *testing.T) {
	ctx := context.Background()
	repo, run := initMergeTestRepo(t)

	writeRepoFile(t, repo, "a.txt", "a\n")
	run(repo, "add", "a.txt")
	run(repo, "commit", "-m", "base")

	m := NewManager(repo)
	err := m.applyConflictResolutions(ctx, Handle{Name: "missing"}, []conflictres.Conflict{
		{File: "does-not-exist.txt", Resolved: "x", AutoResolved: true},
	})
	if err == nil {
		t.Fatal("applyConflictResolutions succeeded with a nonexistent conflicted file; want error")
	}
	if !strings.Contains(err.Error(), "does-not-exist.txt") {
		t.Errorf("error %q does not name the missing file", err)
	}
}
