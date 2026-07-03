package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newShadowTestHandle builds a Handle over a fresh seeded repo with an
// isolated RuntimeDir so each test gets its own private shadow index
// (a shared index across repos would reference foreign objects).
func newShadowTestHandle(t *testing.T) Handle {
	t.Helper()
	dir := t.TempDir()
	base := initTestRepo(t, dir)
	return Handle{
		Name:       "shadow-" + filepath.Base(dir),
		Branch:     "main",
		Path:       dir,
		RuntimeDir: t.TempDir(),
		BaseCommit: base,
		RepoRoot:   dir,
		GitBinary:  "git",
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitFails(t *testing.T, dir string, args ...string) bool {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run() != nil
}

func writeFileT(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestCheckpointRefName(t *testing.T) {
	cases := []struct {
		name string
		seq  int
		want string
	}{
		{"my-task", 3, "refs/r1-checkpoints/my-task/0003"},
		{"Weird Name!", 12, "refs/r1-checkpoints/weird-name/0012"},
		{"", 1, "refs/r1-checkpoints/task/0001"},
		{"my-task", -1, "refs/r1-checkpoints/my-task/pre-restore"},
	}
	for _, c := range cases {
		if got := CheckpointRefName(c.name, c.seq); got != c.want {
			t.Errorf("CheckpointRefName(%q, %d) = %q, want %q", c.name, c.seq, got, c.want)
		}
	}
}

// TestShadowCheckpointCapturesAllAndPreservesIndex is the load-bearing
// contract: the checkpoint tree holds modified + staged + untracked
// files, while HEAD and the agent's REAL index are untouched (the
// GIT_INDEX_FILE guarantee SnapshotWorkingTree does not give).
func TestShadowCheckpointCapturesAllAndPreservesIndex(t *testing.T) {
	h := newShadowTestHandle(t)
	ctx := context.Background()

	writeFileT(t, h.Path, "seed.txt", "modified") // tracked, unstaged edit
	writeFileT(t, h.Path, "staged.txt", "staged")
	gitOut(t, h.Path, "add", "staged.txt") // agent-staged state
	writeFileT(t, h.Path, "untracked.txt", "untracked")

	headBefore := gitOut(t, h.Path, "rev-parse", "HEAD")
	statusBefore := gitOut(t, h.Path, "status", "--porcelain")

	sha, err := ShadowCheckpoint(ctx, h, 1)
	if err != nil {
		t.Fatalf("ShadowCheckpoint: %v", err)
	}
	if sha == "" {
		t.Fatal("empty checkpoint SHA")
	}

	if got := gitOut(t, h.Path, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD moved: %s -> %s", headBefore, got)
	}
	if got := gitOut(t, h.Path, "status", "--porcelain"); got != statusBefore {
		t.Errorf("real index disturbed:\nbefore:\n%s\nafter:\n%s", statusBefore, got)
	}

	for name, want := range map[string]string{
		"seed.txt":      "modified",
		"staged.txt":    "staged",
		"untracked.txt": "untracked",
	} {
		if got := gitOut(t, h.Path, "show", sha+":"+name); got != want {
			t.Errorf("checkpoint tree %s = %q, want %q", name, got, want)
		}
	}

	ref := CheckpointRefName(h.Name, 1)
	if got := gitOut(t, h.Path, "rev-parse", ref); got != sha {
		t.Errorf("ref %s = %s, want %s", ref, got, sha)
	}
}

func TestRestoreFiles(t *testing.T) {
	h := newShadowTestHandle(t)
	ctx := context.Background()

	writeFileT(t, h.Path, "keep.txt", "keep-v1")
	gitOut(t, h.Path, "add", "keep.txt")
	gitOut(t, h.Path, "commit", "-m", "keep")

	sha, err := ShadowCheckpoint(ctx, h, 1)
	if err != nil {
		t.Fatalf("ShadowCheckpoint: %v", err)
	}
	headBefore := gitOut(t, h.Path, "rev-parse", "HEAD")
	branchBefore := gitOut(t, h.Path, "rev-parse", "--abbrev-ref", "HEAD")

	// Post-checkpoint damage: modify, create, delete.
	writeFileT(t, h.Path, "keep.txt", "keep-v2")
	writeFileT(t, h.Path, "junk.txt", "junk")
	if err := os.Remove(filepath.Join(h.Path, "seed.txt")); err != nil {
		t.Fatalf("remove seed.txt: %v", err)
	}

	if err := RestoreFiles(ctx, h, sha); err != nil {
		t.Fatalf("RestoreFiles: %v", err)
	}

	if got, err := os.ReadFile(filepath.Join(h.Path, "keep.txt")); err != nil || string(got) != "keep-v1" {
		t.Errorf("keep.txt = %q (err %v), want keep-v1", got, err)
	}
	if _, err := os.Stat(filepath.Join(h.Path, "junk.txt")); !os.IsNotExist(err) {
		t.Errorf("junk.txt survived restore (err %v)", err)
	}
	if got, err := os.ReadFile(filepath.Join(h.Path, "seed.txt")); err != nil || string(got) != "seed" {
		t.Errorf("seed.txt = %q (err %v), want restored 'seed'", got, err)
	}
	if got := gitOut(t, h.Path, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD moved: %s -> %s", headBefore, got)
	}
	if got := gitOut(t, h.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != branchBefore {
		t.Errorf("branch moved: %s -> %s", branchBefore, got)
	}

	// The restore itself must be undoable: pre-restore safety
	// checkpoint captured the damaged state.
	preRef := CheckpointRefName(h.Name, -1)
	preSHA := gitOut(t, h.Path, "rev-parse", preRef)
	if got := gitOut(t, h.Path, "show", preSHA+":keep.txt"); got != "keep-v2" {
		t.Errorf("pre-restore snapshot keep.txt = %q, want keep-v2", got)
	}
}

func TestRestoreFilesPreservesIgnored(t *testing.T) {
	h := newShadowTestHandle(t)
	ctx := context.Background()

	writeFileT(t, h.Path, ".gitignore", "deps/\n")
	gitOut(t, h.Path, "add", ".gitignore")
	gitOut(t, h.Path, "commit", "-m", "ignore deps")

	sha, err := ShadowCheckpoint(ctx, h, 1)
	if err != nil {
		t.Fatalf("ShadowCheckpoint: %v", err)
	}

	// Ignored artifact (think node_modules) created after the
	// checkpoint must survive; a plain untracked file must not.
	writeFileT(t, h.Path, "deps/lib.txt", "installed")
	writeFileT(t, h.Path, "scratch.txt", "scratch")

	if err := RestoreFiles(ctx, h, sha); err != nil {
		t.Fatalf("RestoreFiles: %v", err)
	}

	if _, err := os.Stat(filepath.Join(h.Path, "deps", "lib.txt")); err != nil {
		t.Errorf("ignored deps/lib.txt should survive restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.Path, "scratch.txt")); !os.IsNotExist(err) {
		t.Errorf("scratch.txt should be cleaned by restore (err %v)", err)
	}
}

func TestRestoreFilesFailsClosedOnMissingCheckpoint(t *testing.T) {
	h := newShadowTestHandle(t)
	ctx := context.Background()

	writeFileT(t, h.Path, "seed.txt", "live-edit")
	err := RestoreFiles(ctx, h, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("RestoreFiles with bogus SHA should error")
	}
	// Nothing was touched.
	if got, rerr := os.ReadFile(filepath.Join(h.Path, "seed.txt")); rerr != nil || string(got) != "live-edit" {
		t.Errorf("working tree modified by failed restore: %q (err %v)", got, rerr)
	}
}

// TestShadowCheckpointSerialized mirrors the engine's usage: parallel
// tool goroutines checkpoint through a mutex. Every SHA must resolve
// and the real index must stay clean.
func TestShadowCheckpointSerialized(t *testing.T) {
	h := newShadowTestHandle(t)
	ctx := context.Background()
	statusBefore := gitOut(t, h.Path, "status", "--porcelain")

	var mu sync.Mutex
	var wg sync.WaitGroup
	shas := make([]string, 8)
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			writeFileT(t, h.Path, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("v%d", i))
			shas[i], errs[i] = ShadowCheckpoint(ctx, h, i+1)
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		if errs[i] != nil {
			t.Fatalf("checkpoint %d: %v", i+1, errs[i])
		}
		if gitFails(t, h.Path, "cat-file", "-e", shas[i]+"^{commit}") {
			t.Errorf("checkpoint %d SHA %s does not resolve", i+1, shas[i])
		}
		if seen[shas[i]] {
			t.Errorf("duplicate checkpoint SHA %s", shas[i])
		}
		seen[shas[i]] = true
	}
	// Only the new untracked files may show up; the pre-existing
	// entries must be unchanged (no stray staged state).
	statusAfter := gitOut(t, h.Path, "status", "--porcelain")
	if !strings.Contains(statusAfter, statusBefore) && statusBefore != "" {
		t.Errorf("pre-existing status lines disturbed:\nbefore:\n%s\nafter:\n%s", statusBefore, statusAfter)
	}
	for _, line := range strings.Split(statusAfter, "\n") {
		if strings.HasPrefix(line, "?? f") {
			continue
		}
		if strings.TrimSpace(line) != "" && !strings.Contains(statusBefore, line) {
			t.Errorf("unexpected status entry after checkpoints: %q", line)
		}
	}
}

func TestListCheckpoints(t *testing.T) {
	h := newShadowTestHandle(t)
	ctx := context.Background()

	writeFileT(t, h.Path, "a.txt", "a")
	if _, err := ShadowCheckpoint(ctx, h, 2); err != nil {
		t.Fatalf("checkpoint 2: %v", err)
	}
	writeFileT(t, h.Path, "b.txt", "b")
	if _, err := ShadowCheckpoint(ctx, h, 10); err != nil {
		t.Fatalf("checkpoint 10: %v", err)
	}

	refs, err := ListCheckpoints(ctx, h)
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want 2: %+v", len(refs), refs)
	}
	if refs[0].Seq != 2 || refs[1].Seq != 10 {
		t.Errorf("seq order = %d,%d, want 2,10", refs[0].Seq, refs[1].Seq)
	}
	for _, r := range refs {
		if gitFails(t, h.Path, "cat-file", "-e", r.SHA+"^{commit}") {
			t.Errorf("ref %s SHA %s does not resolve", r.Ref, r.SHA)
		}
	}
}

// TestManagerCleanupDeletesCheckpointRefs: linked worktrees share the
// parent repo's ref store, so Cleanup must sweep refs/r1-checkpoints/
// or every checkpointed run pins its dangling commits forever.
func TestManagerCleanupDeletesCheckpointRefs(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)
	m := NewManager(repo)
	ctx := context.Background()

	handle, err := m.Prepare(ctx, "ck-clean")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	writeFileT(t, handle.Path, "w.txt", "w")
	if _, err := ShadowCheckpoint(ctx, handle, 1); err != nil {
		t.Fatalf("checkpoint 1: %v", err)
	}
	if _, err := ShadowCheckpoint(ctx, handle, 2); err != nil {
		t.Fatalf("checkpoint 2: %v", err)
	}

	prefix := checkpointRefDir(handle.Name)
	if out := gitOut(t, repo, "for-each-ref", "--format=%(refname)", prefix); out == "" {
		t.Fatalf("expected checkpoint refs under %s before cleanup", prefix)
	}

	if err := m.Cleanup(ctx, handle); err != nil {
		t.Logf("cleanup returned: %v (refs must still be swept)", err)
	}

	if out := gitOut(t, repo, "for-each-ref", "--format=%(refname)", prefix); out != "" {
		t.Errorf("checkpoint refs survived cleanup:\n%s", out)
	}
}
