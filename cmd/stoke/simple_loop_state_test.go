package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitRepoWithCommit creates a fresh git repo under dir with a
// single commit containing `initial content` at README.md. Returns
// the HEAD sha. Used by P2-5 resume-compat tests.
func initGitRepoWithCommit(t *testing.T, dir string, msg string) string {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", msg)
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	sha := string(out)
	if len(sha) >= 40 {
		sha = sha[:40]
	}
	return sha
}

func appendCommit(t *testing.T, dir, filename, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("y"), 0o644); err != nil {
		t.Fatalf("append file: %v", err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("add", filename)
	run("commit", "-q", "-m", msg)
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, _ := cmd.Output()
	sha := string(out)
	if len(sha) >= 40 {
		sha = sha[:40]
	}
	return sha
}

func TestSimpleLoopState_SaveLoadRoundTrip(t *testing.T) {
	repo := t.TempDir()
	state := &simpleLoopState{
		SOWHash:      hashProse("sow prose v1"),
		CurrentRound: 3,
		MaxRounds:    8,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentProse: "round-3 extracted gaps prose",
		Step8Cycles:  1,
		LastGaps:     []string{"missing endpoint", "stub exec"},
	}
	if err := SaveSimpleLoopState(repo, state); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadSimpleLoopState(repo)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil state after save/load")
	}
	if got.Version != simpleLoopStateVersion {
		t.Errorf("version = %d, want %d", got.Version, simpleLoopStateVersion)
	}
	if got.CurrentRound != 3 || got.MaxRounds != 8 {
		t.Errorf("round fields lost in round-trip: %+v", got)
	}
	if got.CurrentProse != "round-3 extracted gaps prose" {
		t.Errorf("prose lost: %q", got.CurrentProse)
	}
	if len(got.LastGaps) != 2 || got.LastGaps[0] != "missing endpoint" {
		t.Errorf("gaps lost: %+v", got.LastGaps)
	}
	if got.SavedAt.IsZero() {
		t.Error("SavedAt should be set by Save")
	}
}

func TestSimpleLoopState_LoadAbsent(t *testing.T) {
	repo := t.TempDir()
	got, err := LoadSimpleLoopState(repo)
	if err != nil {
		t.Fatalf("load on missing file should return (nil, nil), got err: %v", err)
	}
	if got != nil {
		t.Errorf("load on missing file should return nil, got %+v", got)
	}
}

func TestSimpleLoopState_LoadMalformed(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".stoke")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(simpleLoopStateFile(repo), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSimpleLoopState(repo)
	if err == nil {
		t.Error("malformed file should return error so caller can surface + fall through to fresh")
	}
}

func TestSimpleLoopState_LoadWrongVersion(t *testing.T) {
	// Hand-craft a v0 blob; loader must refuse instead of silently
	// handing back a zero-valued state that would resume at round 0.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".stoke"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"version":       99,
		"current_round": 2,
		"sow_hash":      "abc",
	})
	if err := os.WriteFile(simpleLoopStateFile(repo), body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSimpleLoopState(repo)
	if err == nil {
		t.Error("wrong-version file should refuse to load")
	}
}

func TestSimpleLoopState_Clear(t *testing.T) {
	repo := t.TempDir()
	state := &simpleLoopState{
		SOWHash:      "h",
		CurrentRound: 1,
		MaxRounds:    5,
		Reviewer:     "codex",
		FixMode:      "sequential",
	}
	if err := SaveSimpleLoopState(repo, state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(simpleLoopStateFile(repo)); err != nil {
		t.Fatalf("state should exist after save: %v", err)
	}
	if err := ClearSimpleLoopState(repo); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := os.Stat(simpleLoopStateFile(repo)); !os.IsNotExist(err) {
		t.Errorf("state file should be gone after clear, stat err: %v", err)
	}
	// Clearing an already-absent file is a no-op, not an error.
	if err := ClearSimpleLoopState(repo); err != nil {
		t.Errorf("clear on absent file should be silent, got: %v", err)
	}
}

func TestValidateResumeCompat_Happy(t *testing.T) {
	h := hashProse("spec A")
	state := &simpleLoopState{
		SOWHash:      h,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 2,
	}
	ok, reason := validateResumeCompat(state, "", h, "codex", "sequential")
	if !ok {
		t.Errorf("compat check should pass, rejected: %s", reason)
	}
}

func TestValidateResumeCompat_SOWChanged(t *testing.T) {
	state := &simpleLoopState{
		SOWHash:      hashProse("spec A"),
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 2,
	}
	ok, reason := validateResumeCompat(state, "", hashProse("spec B"), "codex", "sequential")
	if ok {
		t.Error("compat must refuse when SOW hash changes")
	}
	if reason == "" {
		t.Error("reason string should explain the SOW hash mismatch")
	}
}

func TestValidateResumeCompat_AbortedRefuses(t *testing.T) {
	h := hashProse("spec A")
	state := &simpleLoopState{
		SOWHash:      h,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 5,
		Aborted:      true,
	}
	ok, reason := validateResumeCompat(state, "", h, "codex", "sequential")
	if ok {
		t.Error("compat must refuse to resume a regression-cap aborted run")
	}
	if reason == "" {
		t.Error("reason should explain the aborted state")
	}
}

func TestValidateResumeCompat_ReviewerSwap(t *testing.T) {
	h := hashProse("spec A")
	state := &simpleLoopState{
		SOWHash:      h,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 2,
	}
	ok, _ := validateResumeCompat(state, "", h, "cc-opus", "sequential")
	if ok {
		t.Error("compat must refuse cross-reviewer resume")
	}
}

func TestValidateResumeCompat_FixModeSwap(t *testing.T) {
	h := hashProse("spec A")
	state := &simpleLoopState{
		SOWHash:      h,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 2,
	}
	ok, _ := validateResumeCompat(state, "", h, "codex", "parallel")
	if ok {
		t.Error("compat must refuse cross-fix-mode resume")
	}
}

func TestValidateResumeCompat_NilState(t *testing.T) {
	ok, reason := validateResumeCompat(nil, "", "h", "codex", "sequential")
	if ok {
		t.Error("nil state must not validate")
	}
	if reason == "" {
		t.Error("nil state reason should be populated")
	}
}

func TestSaveSimpleLoopState_AtomicRename(t *testing.T) {
	// A pre-existing malformed file must be fully replaced by Save,
	// not merged or left half-written. Round-trips the replacement
	// through Load to confirm.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".stoke"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(simpleLoopStateFile(repo), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &simpleLoopState{
		SOWHash:      "h",
		CurrentRound: 2,
		MaxRounds:    5,
		Reviewer:     "codex",
		FixMode:      "sequential",
	}
	if err := SaveSimpleLoopState(repo, state); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSimpleLoopState(repo)
	if err != nil {
		t.Fatalf("load after atomic save: %v", err)
	}
	if got.CurrentRound != 2 {
		t.Errorf("expected round 2 after atomic replace, got %d", got.CurrentRound)
	}
}

// Codex P2-5 regression: repo HEAD must be recorded + verified on
// resume so a cherry-pick, manual fix, branch switch, or history
// rewrite doesn't silently replay stale gaps against an unrelated tree.

func TestValidateResumeCompat_HeadFastForwardOK(t *testing.T) {
	// Seed a repo, capture HEAD, advance with a second commit.
	// Resume must ACCEPT the fast-forward: saved HEAD is ancestor of current.
	repo := t.TempDir()
	savedHead := initGitRepoWithCommit(t, repo, "c1")
	_ = appendCommit(t, repo, "other.ts", "c2")

	h := hashProse("spec A")
	state := &simpleLoopState{
		SOWHash:      h,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 2,
		RepoHead:     savedHead,
	}
	ok, reason := validateResumeCompat(state, repo, h, "codex", "sequential")
	if !ok {
		t.Errorf("fast-forward must be allowed, rejected: %s", reason)
	}
}

func TestValidateResumeCompat_HeadEqualOK(t *testing.T) {
	// No commits since save — HEAD equal to saved.
	repo := t.TempDir()
	head := initGitRepoWithCommit(t, repo, "only")
	h := hashProse("spec A")
	state := &simpleLoopState{
		SOWHash:      h,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 2,
		RepoHead:     head,
	}
	ok, reason := validateResumeCompat(state, repo, h, "codex", "sequential")
	if !ok {
		t.Errorf("equal HEAD must be allowed, rejected: %s", reason)
	}
}

func TestValidateResumeCompat_HeadDivergedRefuses(t *testing.T) {
	// Seed repo, save state; then hard-reset away from saved HEAD so
	// savedHead is no longer an ancestor. Resume must refuse.
	repo := t.TempDir()
	savedHead := initGitRepoWithCommit(t, repo, "save-point")
	// Create a divergent branch by committing, then resetting back
	// and committing again on a different tree.
	_ = appendCommit(t, repo, "b.ts", "discard")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// Rewrite history: amend the first commit so savedHead is orphaned.
	run("checkout", "-q", "--orphan", "new-branch")
	run("rm", "-rf", "--cached", ".")
	if err := os.WriteFile(filepath.Join(repo, "C.md"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "C.md")
	run("commit", "-q", "-m", "divergent")
	// Now savedHead is NOT an ancestor of HEAD.
	h := hashProse("spec A")
	state := &simpleLoopState{
		SOWHash:      h,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 2,
		RepoHead:     savedHead,
	}
	ok, reason := validateResumeCompat(state, repo, h, "codex", "sequential")
	if ok {
		t.Error("diverged HEAD must refuse resume")
	}
	if reason == "" {
		t.Error("reason should explain the divergence")
	}
}

func TestValidateResumeCompat_EmptyRepoHeadBackCompat(t *testing.T) {
	// Pre-P2-5 state files have RepoHead="" — the loader should accept
	// them without running the ancestor check (backwards compat),
	// rather than failing out. Resume simply can't verify HEAD against
	// a state that never recorded it.
	repo := t.TempDir()
	_ = initGitRepoWithCommit(t, repo, "c1")
	h := hashProse("spec A")
	state := &simpleLoopState{
		SOWHash:      h,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 2,
		RepoHead:     "", // legacy state pre-P2-5
	}
	ok, reason := validateResumeCompat(state, repo, h, "codex", "sequential")
	if !ok {
		t.Errorf("empty saved RepoHead should preserve legacy compat, rejected: %s", reason)
	}
}

// Codex P1-1 regression: resume must refuse when the working tree has
// uncommitted changes (staged, unstaged, or untracked). A crash mid-
// builder leaves HEAD unchanged + partial writes in the tree; resuming
// would apply saved prose on top of garbage.

func TestValidateResumeCompat_DirtyTreeRefuses(t *testing.T) {
	repo := t.TempDir()
	head := initGitRepoWithCommit(t, repo, "c1")
	// Introduce dirt — an untracked file is enough.
	if err := os.WriteFile(filepath.Join(repo, "half-written.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := hashProse("spec A")
	state := &simpleLoopState{
		SOWHash:      h,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 2,
		RepoHead:     head,
	}
	ok, reason := validateResumeCompat(state, repo, h, "codex", "sequential")
	if ok {
		t.Error("dirty tree must refuse resume (codex P1-1)")
	}
	if reason == "" {
		t.Error("refusal reason must be populated so the operator knows to stash/commit")
	}
}

func TestValidateResumeCompat_CleanTreeAccepts(t *testing.T) {
	repo := t.TempDir()
	head := initGitRepoWithCommit(t, repo, "c1")
	h := hashProse("spec A")
	state := &simpleLoopState{
		SOWHash:      h,
		Reviewer:     "codex",
		FixMode:      "sequential",
		CurrentRound: 2,
		RepoHead:     head,
	}
	ok, reason := validateResumeCompat(state, repo, h, "codex", "sequential")
	if !ok {
		t.Errorf("clean tree should accept resume, rejected: %s", reason)
	}
}

func TestRepoTreeIsDirty_CleanRepoFalse(t *testing.T) {
	repo := t.TempDir()
	_ = initGitRepoWithCommit(t, repo, "c1")
	dirty, err := repoTreeIsDirty(repo)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if dirty {
		t.Error("fresh-commit repo should be clean")
	}
}

func TestRepoTreeIsDirty_StagedChangeTrue(t *testing.T) {
	repo := t.TempDir()
	_ = initGitRepoWithCommit(t, repo, "c1")
	_ = appendCommit(t, repo, "a.ts", "c2") // establish baseline
	// Stage a modification without committing.
	if err := os.WriteFile(filepath.Join(repo, "a.ts"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "add", "a.ts")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stage: %v: %s", err, out)
	}
	dirty, err := repoTreeIsDirty(repo)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !dirty {
		t.Error("staged change must be reported as dirty")
	}
}

func TestRepoTreeIsDirty_UntrackedFileTrue(t *testing.T) {
	repo := t.TempDir()
	_ = initGitRepoWithCommit(t, repo, "c1")
	if err := os.WriteFile(filepath.Join(repo, "new.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := repoTreeIsDirty(repo)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !dirty {
		t.Error("untracked file must be reported as dirty")
	}
}

func TestCurrentRepoHead_NonRepoReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := currentRepoHead(dir); got != "" {
		t.Errorf("non-git dir should return empty, got %q", got)
	}
}

func TestCurrentRepoHead_ReturnsSha(t *testing.T) {
	repo := t.TempDir()
	head := initGitRepoWithCommit(t, repo, "c1")
	got := currentRepoHead(repo)
	if got != head {
		t.Errorf("currentRepoHead = %q, want %q", got, head)
	}
}

func TestHashProse_Deterministic(t *testing.T) {
	// Two separate calls with the same input — binding to distinct
	// variables both documents the intent and sidesteps SA4000's
	// false-positive on X != X.
	a := hashProse("abc")
	b := hashProse("abc")
	if a != b {
		t.Error("hashProse must be deterministic")
	}
	if hashProse("abc") == hashProse("abd") {
		t.Error("hashProse must distinguish different inputs")
	}
	if len(hashProse("x")) != 16 {
		t.Errorf("hashProse should be 16 hex chars, got len %d", len(hashProse("x")))
	}
}
