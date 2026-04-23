package chat

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestFilterSourceFiles_SkipsDocs(t *testing.T) {
	got := FilterSourceFiles([]string{"src/foo.go", "README.md", "docs/x.md"})
	want := []string{"src/foo.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFilterSourceFiles_IncludesConfig(t *testing.T) {
	got := FilterSourceFiles([]string{"package.json"})
	want := []string{"package.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFilterSourceFiles_SkipsInternal(t *testing.T) {
	got := FilterSourceFiles([]string{".claude/foo.sh", ".stoke/cache"})
	if len(got) != 0 {
		t.Fatalf("expected empty slice for internal-state paths, got %v", got)
	}
}

func TestFilterSourceFiles_SkipsSpecsAndPlans(t *testing.T) {
	got := FilterSourceFiles([]string{"specs/foo.md", "plans/build.yaml", "LICENSE", ".gitignore"})
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestFilterSourceFiles_MixedSourceAndDocs(t *testing.T) {
	got := FilterSourceFiles([]string{
		"main.go",
		"docs/intro.md",
		"src/components/App.tsx",
		"README.md",
		"go.mod",
	})
	sort.Strings(got)
	want := []string{"go.mod", "main.go", "src/components/App.tsx"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildACs_Go(t *testing.T) {
	dir := t.TempDir()
	// Create a minimal Go file so detectLanguage sees a .go change.
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	acs := BuildACsForTouched(dir, []string{"foo.go"})
	if len(acs) != 3 {
		t.Fatalf("expected 3 Go ACs, got %d: %+v", len(acs), acs)
	}
	found := map[string]string{}
	for _, ac := range acs {
		found[ac.ID] = ac.Command
	}
	if cmd := found["chat.build"]; cmd != "go build ./..." {
		t.Errorf("chat.build cmd = %q", cmd)
	}
	if cmd := found["chat.vet"]; cmd != "go vet ./..." {
		t.Errorf("chat.vet cmd = %q", cmd)
	}
	if cmd := found["chat.test"]; cmd == "" || cmd[:10] != "go test ./" {
		t.Errorf("chat.test cmd = %q (expected go test ./...)", cmd)
	}
}

func TestBuildACs_DocsOnly(t *testing.T) {
	dir := t.TempDir()
	acs := BuildACsForTouched(dir, []string{"README.md", "docs/intro.md"})
	if len(acs) != 0 {
		t.Fatalf("docs-only change must produce zero ACs, got %+v", acs)
	}
}

func TestBuildACs_Rust(t *testing.T) {
	dir := t.TempDir()
	acs := BuildACsForTouched(dir, []string{"src/main.rs"})
	if len(acs) != 2 {
		t.Fatalf("expected 2 Rust ACs, got %d: %+v", len(acs), acs)
	}
	if acs[0].Command != "cargo check" {
		t.Errorf("first Rust AC should be cargo check, got %q", acs[0].Command)
	}
	if acs[1].Command != "cargo test" {
		t.Errorf("second Rust AC should be cargo test, got %q", acs[1].Command)
	}
}

func TestBuildACs_PythonWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	acs := BuildACsForTouched(dir, []string{"app.py"})
	if len(acs) != 0 {
		t.Fatalf("python without pytest config must produce zero ACs, got %+v", acs)
	}
}

func TestBuildACs_PythonWithPytestIni(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pytest.ini"), []byte("[pytest]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	acs := BuildACsForTouched(dir, []string{"app.py"})
	if len(acs) != 1 {
		t.Fatalf("expected 1 python AC with pytest.ini, got %+v", acs)
	}
	if acs[0].ID != "chat.test" || acs[0].Command != "pytest -q" {
		t.Errorf("unexpected python AC: %+v", acs[0])
	}
}

func TestBuildACs_ConfigManifestOnly(t *testing.T) {
	dir := t.TempDir()
	acs := BuildACsForTouched(dir, []string{"package.json"})
	if len(acs) != 1 {
		t.Fatalf("expected 1 install AC for manifest-only change, got %+v", acs)
	}
	if acs[0].ID != "chat.install" {
		t.Errorf("manifest-only AC id = %q, want chat.install", acs[0].ID)
	}
}

func TestBuildACs_UnknownLanguage(t *testing.T) {
	dir := t.TempDir()
	// FilterSourceFiles would strip this already, but detectLanguage
	// should also return "" on a path with no recognized extension.
	acs := BuildACsForTouched(dir, []string{"weird.xyz"})
	if len(acs) != 0 {
		t.Fatalf("unknown language must produce zero ACs, got %+v", acs)
	}
}

func TestShouldFire_NoStartCommit_Off(t *testing.T) {
	g := &DescentGate{StartCommit: ""}
	fire, changed, err := g.ShouldFire(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fire {
		t.Error("fire should be false when StartCommit is empty")
	}
	if changed != nil {
		t.Errorf("changed should be nil, got %v", changed)
	}
}

func TestShouldFire_NilReceiver_Off(t *testing.T) {
	var g *DescentGate
	fire, changed, err := g.ShouldFire(context.Background())
	if err != nil || fire || changed != nil {
		t.Fatalf("nil receiver must be a clean no-op, got fire=%v changed=%v err=%v", fire, changed, err)
	}
}

// TestShouldFire_DirtyGoFile builds a real git repo in a tempdir,
// adds a .go file after the initial commit, and asserts ShouldFire
// returns true. Hard-requires git on PATH — Stoke is a git-centric
// tool and its test environment must have git installed.
func TestShouldFire_DirtyGoFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git must be on PATH for Stoke tests: %v", err)
	}
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q")
	gitCmd(t, dir, "config", "user.email", "test@example.com")
	gitCmd(t, dir, "config", "user.name", "Test")
	// Seed an initial commit so HEAD exists.
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "seed.txt")
	gitCmd(t, dir, "commit", "-q", "-m", "seed")

	head := CaptureStartCommit(context.Background(), dir)
	// assert.NonEmpty: HEAD capture must succeed post-seed commit.
	if head == "" {
		t.Fatal("expected non-empty HEAD SHA after seed commit")
	}

	// Dirty a .go file (untracked — porcelain picks this up).
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &DescentGate{Repo: dir, StartCommit: head}
	fire, changed, err := g.ShouldFire(context.Background())
	if err != nil {
		t.Fatalf("ShouldFire err: %v", err)
	}
	if !fire {
		t.Errorf("expected fire=true for dirtied foo.go")
	}
	if len(changed) != 1 || changed[0] != "foo.go" {
		t.Errorf("changed = %v, want [foo.go]", changed)
	}
}

// TestShouldFire_OnlyDocsDirty asserts that a markdown-only edit does
// not trip the gate even though git status reports it. Hard-requires
// git on PATH.
func TestShouldFire_OnlyDocsDirty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git must be on PATH for Stoke tests: %v", err)
	}
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q")
	gitCmd(t, dir, "config", "user.email", "test@example.com")
	gitCmd(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "seed.txt")
	gitCmd(t, dir, "commit", "-q", "-m", "seed")
	head := CaptureStartCommit(context.Background(), dir)
	// assert.HeadSeeded: we need the SHA to build a DescentGate below.

	if err := os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte("# hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &DescentGate{Repo: dir, StartCommit: head}
	fire, changed, err := g.ShouldFire(context.Background())
	if err != nil {
		t.Fatalf("ShouldFire err: %v", err)
	}
	if fire {
		t.Errorf("expected fire=false for docs-only change, got changed=%v", changed)
	}
}

// TestCaptureStartCommit_NonGit verifies the no-git fallback: the
// function must return "" (not panic, not error out loudly) when the
// directory is not a git repo.
func TestCaptureStartCommit_NonGit(t *testing.T) {
	dir := t.TempDir()
	got := CaptureStartCommit(context.Background(), dir)
	// assert.Empty: non-git directory must yield the empty SHA sentinel.
	if got != "" {
		t.Errorf("non-git dir should return empty SHA, got %q", got)
	}
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
