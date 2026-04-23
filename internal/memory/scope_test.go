package memory

import (
	"context"
	"strings"
	"testing"
)

func TestHierScope_Valid(t *testing.T) {
	for _, s := range []HierScope{HierGlobal, HierRepo, HierTask, HierAuto} {
		if !s.Valid() {
			t.Errorf("HierScope(%q).Valid() = false, want true", s)
		}
	}
	for _, bad := range []HierScope{"", "project", "GLOBAL", "session"} {
		if bad.Valid() {
			t.Errorf("HierScope(%q).Valid() = true, want false", bad)
		}
	}
}

func TestParseHierScope(t *testing.T) {
	cases := []struct {
		in   string
		want HierScope
		ok   bool
	}{
		{"global", HierGlobal, true},
		{"repo", HierRepo, true},
		{"task", HierTask, true},
		{"auto", HierAuto, true},
		{"  Task  ", HierTask, true},
		{"GLOBAL", HierGlobal, true},
		{"", HierScope(""), false},
		{"session", HierScope(""), false},
	}
	for _, tc := range cases {
		got, err := ParseHierScope(tc.in)
		if tc.ok && err != nil {
			t.Errorf("ParseHierScope(%q) err=%v, want nil", tc.in, err)
			continue
		}
		if !tc.ok && err == nil {
			t.Errorf("ParseHierScope(%q) err=nil, want error", tc.in)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseHierScope(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSpecificity_Ordering(t *testing.T) {
	if Specificity(HierTask) <= Specificity(HierRepo) {
		t.Error("task must outrank repo")
	}
	if Specificity(HierRepo) <= Specificity(HierGlobal) {
		t.Error("repo must outrank global")
	}
	if Specificity(HierGlobal) <= 0 {
		t.Error("global must outrank unset")
	}
	if Specificity(HierAuto) != 0 {
		t.Errorf("Auto must have specificity 0 (unresolved), got %d", Specificity(HierAuto))
	}
	if Specificity(HierScope("garbage")) != 0 {
		t.Error("unknown scope must have specificity 0")
	}
}

func TestSpecificityOf_ReadsItemField(t *testing.T) {
	it := Item{HierScope: HierRepo}
	if SpecificityOf(it) != 2 {
		t.Errorf("SpecificityOf(repo-item) = %d, want 2", SpecificityOf(it))
	}
	if SpecificityOf(Item{}) != 0 {
		t.Error("unscoped item must be specificity 0")
	}
}

func TestRepoHash_Stable(t *testing.T) {
	h1 := RepoHash()
	h2 := RepoHash()
	if h1 != h2 {
		t.Errorf("RepoHash not stable: %s vs %s", h1, h2)
	}
	if len(h1) != 16 {
		t.Errorf("RepoHash length = %d, want 16", len(h1))
	}
	for _, c := range h1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("RepoHash not hex: %q (char %c)", h1, c)
			break
		}
	}
}

func TestRepoHashAt_FallbackToCwd(t *testing.T) {
	// tmpdir that isn't a git repo — git rev-parse fails,
	// RepoHashAt falls back to os.Getwd() (the test
	// process's cwd, not `tmp`). Contract: we still get a
	// valid 16-char hex hash with no panic, and the
	// fallback is deterministic (two calls match).
	tmp := t.TempDir()
	h := RepoHashAt(context.Background(), tmp)
	if len(h) != 16 {
		t.Errorf("len(hash) = %d, want 16", len(h))
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("fallback hash not hex: %q", h)
			break
		}
	}
	// Determinism: a second call with a different tempdir
	// still lands on the same process-cwd fallback, so the
	// hash matches. This pins the documented behavior: the
	// `dir` arg only influences the git-success branch; the
	// fallback is cwd-anchored, not dir-anchored.
	other := t.TempDir()
	h2 := RepoHashAt(context.Background(), other)
	if h != h2 {
		t.Errorf("fallback not deterministic across calls: %s vs %s", h, h2)
	}
}

func TestPredicateFor_Global(t *testing.T) {
	frag, args, err := PredicateFor(HierGlobal, "", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if frag != "(scope = ?)" {
		t.Errorf("fragment = %q", frag)
	}
	if len(args) != 1 || args[0] != "global" {
		t.Errorf("args = %v, want [global]", args)
	}
}

func TestPredicateFor_Repo(t *testing.T) {
	frag, args, err := PredicateFor(HierRepo, "abc1234567890def", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(frag, "scope = ?") || !strings.Contains(frag, "scope_id = ?") {
		t.Errorf("fragment missing parts: %q", frag)
	}
	if len(args) != 2 || args[0] != "repo" || args[1] != "abc1234567890def" {
		t.Errorf("args = %v", args)
	}
}

func TestPredicateFor_Repo_EmptyHashErrors(t *testing.T) {
	_, _, err := PredicateFor(HierRepo, "", "")
	if err == nil {
		t.Error("HierRepo with empty hash must error")
	}
}

func TestPredicateFor_Task(t *testing.T) {
	frag, args, err := PredicateFor(HierTask, "", "T-42")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(frag, "scope = ?") {
		t.Errorf("fragment missing scope equality: %q", frag)
	}
	if len(args) != 2 || args[0] != "task" || args[1] != "T-42" {
		t.Errorf("args = %v", args)
	}
}

func TestPredicateFor_Task_EmptyIDErrors(t *testing.T) {
	_, _, err := PredicateFor(HierTask, "", "")
	if err == nil {
		t.Error("HierTask with empty taskID must error")
	}
}

func TestPredicateFor_Auto_AllBranches(t *testing.T) {
	frag, args, err := PredicateFor(HierAuto, "repoHH", "T-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{"'global'", "'repo'", "'task'"} {
		if !strings.Contains(frag, want) {
			t.Errorf("Auto fragment missing %s branch: %q", want, frag)
		}
	}
	if len(args) != 2 {
		t.Errorf("args len = %d, want 2 (repo + task binds)", len(args))
	}
}

func TestPredicateFor_Auto_RepoOnly(t *testing.T) {
	frag, args, err := PredicateFor(HierAuto, "repoHH", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(frag, "'global'") || !strings.Contains(frag, "'repo'") {
		t.Errorf("Auto(repo-only) missing global/repo: %q", frag)
	}
	if strings.Contains(frag, "'task'") {
		t.Errorf("Auto(repo-only) must not mention task branch: %q", frag)
	}
	if len(args) != 1 {
		t.Errorf("args len = %d, want 1", len(args))
	}
}

func TestPredicateFor_Auto_GlobalOnly(t *testing.T) {
	frag, args, err := PredicateFor(HierAuto, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(frag, "'repo'") || strings.Contains(frag, "'task'") {
		t.Errorf("Auto(empty) leaked repo/task branch: %q", frag)
	}
	if !strings.Contains(frag, "'global'") {
		t.Errorf("Auto(empty) dropped global: %q", frag)
	}
	if len(args) != 0 {
		t.Errorf("args len = %d, want 0", len(args))
	}
}

func TestPredicateFor_Unknown(t *testing.T) {
	_, _, err := PredicateFor(HierScope("session"), "", "")
	if err == nil {
		t.Error("unknown scope must return error")
	}
}

func TestResolveConflict_TaskBeatsRepo(t *testing.T) {
	a := Item{ID: "repo-fact", HierScope: HierRepo}
	b := Item{ID: "task-fact", HierScope: HierTask}
	win, broken := ResolveConflict(a, b)
	if !broken {
		t.Error("specificity tie-break should have resolved")
	}
	if win.ID != "task-fact" {
		t.Errorf("winner = %q, want task-fact", win.ID)
	}
}

func TestResolveConflict_RepoBeatsGlobal(t *testing.T) {
	a := Item{ID: "global-fact", HierScope: HierGlobal}
	b := Item{ID: "repo-fact", HierScope: HierRepo}
	win, broken := ResolveConflict(a, b)
	if !broken || win.ID != "repo-fact" {
		t.Errorf("win=%q broken=%v, want repo-fact/true", win.ID, broken)
	}
}

func TestResolveConflict_SameScope_UnbrokenFallback(t *testing.T) {
	a := Item{ID: "r1", HierScope: HierRepo}
	b := Item{ID: "r2", HierScope: HierRepo}
	win, broken := ResolveConflict(a, b)
	if broken {
		t.Error("same-scope tie must report broken=false")
	}
	if win.ID != "r1" {
		t.Errorf("fallback winner = %q, want first arg r1", win.ID)
	}
}

func TestSortBySpecificity_StableWithinBucket(t *testing.T) {
	items := []Item{
		{ID: "g1", HierScope: HierGlobal},
		{ID: "t1", HierScope: HierTask},
		{ID: "r1", HierScope: HierRepo},
		{ID: "t2", HierScope: HierTask},
		{ID: "r2", HierScope: HierRepo},
	}
	got := SortBySpecificity(items)
	if got[0].ID != "t1" || got[1].ID != "t2" {
		t.Errorf("task ordering lost: got %+v", got)
	}
	if got[2].ID != "r1" || got[3].ID != "r2" || got[4].ID != "g1" {
		t.Errorf("bucket ordering wrong: got %+v", got)
	}
	if items[0].ID != "g1" {
		t.Error("SortBySpecificity mutated input slice")
	}
}
