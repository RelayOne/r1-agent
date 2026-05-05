package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/antitrunc"
)

func TestRunAntiTruncCmd_NoVerb(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncCmd([]string{}, &stdout, &stderr)
	if rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("missing usage: %s", stderr.String())
	}
}

func TestRunAntiTruncCmd_UnknownVerb(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncCmd([]string{"banana"}, &stdout, &stderr)
	if rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "unknown verb") {
		t.Errorf("missing error msg: %s", stderr.String())
	}
}

func TestListPatterns_DumpsCatalog(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncListPatterns([]string{}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	out := stdout.String()
	if !strings.Contains(out, "premature_stop_let_me") {
		t.Errorf("missing premature_stop_let_me")
	}
	if !strings.Contains(out, "false_completion_spec_done") {
		t.Errorf("missing false_completion_spec_done")
	}
}

// initRepo creates a tmp git repo with the supplied subjects/bodies.
// Returns the repo dir.
func initRepo(t *testing.T, changes []struct{ Subject, Body string }) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "initial")
	for _, c := range changes {
		msg := c.Subject
		if c.Body != "" {
			msg += "\n\n" + c.Body
		}
		cmd := exec.Command("git", "commit", "--allow-empty", "-m", msg)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}
	return dir
}

func TestVerify_NoChanges_Clean(t *testing.T) {
	repo := initRepo(t, nil)
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncVerify([]string{"-repo", repo, "-n", "5"}, &stdout, &stderr)
	if rc != 0 {
		t.Errorf("rc = %d, want 0; stderr=%s", rc, stderr.String())
	}
	if !strings.Contains(stdout.String(), "inspected") {
		t.Errorf("missing inspected output: %s", stdout.String())
	}
}

func TestVerify_VerifiedSpecCompletion(t *testing.T) {
	repo := initRepo(t, []struct{ Subject, Body string }{
		{"feat(x): spec 1 done", ""},
	})

	specsDir := filepath.Join(repo, "specs")
	if err := os.MkdirAll(specsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specsDir, "spec-1.md"),
		[]byte("- [x] only\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	rc := runAntiTruncVerify([]string{"-repo", repo, "-n", "5", "-json"}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d (lying detected unexpectedly); stderr=%s\nstdout=%s", rc, stderr.String(), stdout.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	results, ok := payload["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("results missing: %v", payload)
	}
	if payload["lying_count"].(float64) != 0 {
		t.Errorf("expected lying_count=0, got %v", payload["lying_count"])
	}
}

func TestVerify_LyingFalseCompletion(t *testing.T) {
	repo := initRepo(t, []struct{ Subject, Body string }{
		{"feat(x): all tasks done", "spec 9 done — merging now"},
	})
	plansDir := filepath.Join(repo, "plans")
	os.MkdirAll(plansDir, 0o755)
	os.WriteFile(filepath.Join(plansDir, "build-plan.md"),
		[]byte("<!-- STATUS: in-progress -->\n- [ ] open\n"), 0o644)

	var stdout, stderr bytes.Buffer
	rc := runAntiTruncVerify([]string{"-repo", repo, "-n", "5"}, &stdout, &stderr)
	if rc == 0 {
		t.Fatalf("expected rc != 0 on lying claim; stdout=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "lying") && !strings.Contains(stdout.String(), "Lying") {
		t.Errorf("missing 'lying' verdict in output: %s", stdout.String())
	}
}

func TestClassifyChange_NoClaim(t *testing.T) {
	verdict := classifyChange(gitChange{SHA: "abc", Subject: "refactor: rename helper"}, antitrunc.ScopeReport{}, nil)
	if verdict.Verdict != "unverified" {
		t.Errorf("verdict = %q, want unverified", verdict.Verdict)
	}
	if verdict.Detail == "" {
		t.Error("expected non-empty detail")
	}
}

func TestClassifyChange_VerifiedSpec(t *testing.T) {
	specRep := antitrunc.ScopeReport{
		Path:  "/x/specs/spec-9.md",
		Total: 2,
		Done:  2,
	}
	verdict := classifyChange(
		gitChange{SHA: "abc", Subject: "feat: spec 9 complete"},
		antitrunc.ScopeReport{},
		[]antitrunc.ScopeReport{specRep},
	)
	if verdict.Verdict != "verified" {
		t.Errorf("verdict = %q, want verified; detail=%s", verdict.Verdict, verdict.Detail)
	}
}

func TestClassifyChange_LyingSpec(t *testing.T) {
	specRep := antitrunc.ScopeReport{
		Path:  "/x/specs/spec-9.md",
		Total: 5,
		Done:  2,
	}
	verdict := classifyChange(
		gitChange{SHA: "abc", Subject: "feat: spec 9 complete"},
		antitrunc.ScopeReport{},
		[]antitrunc.ScopeReport{specRep},
	)
	if verdict.Verdict != "lying" {
		t.Errorf("verdict = %q, want lying", verdict.Verdict)
	}
}

func TestFindSpecByIndex(t *testing.T) {
	specs := []antitrunc.ScopeReport{
		{Path: "/specs/spec-9.md"},
		{Path: "/specs/spec-12.md"},
		{Path: "/specs/anti-truncation.md"},
	}
	found, ok := findSpecByIndex(specs, "9")
	if !ok || !strings.HasSuffix(found.Path, "spec-9.md") {
		t.Errorf("findSpecByIndex(9) = %v ok=%v, want spec-9.md", found, ok)
	}
	found, ok = findSpecByIndex(specs, "12")
	if !ok || !strings.HasSuffix(found.Path, "spec-12.md") {
		t.Errorf("findSpecByIndex(12) = %v ok=%v", found, ok)
	}
	_, ok = findSpecByIndex(specs, "999")
	if ok {
		t.Error("findSpecByIndex(999) should return ok=false")
	}
}

func TestRunAntiTruncTail_NoDir(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncTail([]string{"-repo", dir}, &stdout, &stderr)
	if rc != 0 {
		t.Errorf("rc = %d, want 0", rc)
	}
	if !strings.Contains(stdout.String(), "no audit/antitrunc/") {
		t.Errorf("missing message: %s", stdout.String())
	}
}

func TestRunAntiTruncTail_StreamsExisting(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "audit", "antitrunc")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "post-commit-aaa.md"), []byte("# warn\nhi"), 0o644)
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncTail([]string{"-repo", dir}, &stdout, &stderr)
	if rc != 0 {
		t.Errorf("rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "post-commit-aaa.md") {
		t.Errorf("missing filename: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "# warn") {
		t.Errorf("missing body: %s", stdout.String())
	}
}

func TestRunAntiTruncTail_JSON(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "audit", "antitrunc")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "post-commit-bbb.md"), []byte("# json-mode\nhi"), 0o644)
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncTail([]string{"-repo", dir, "-json"}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &rec); err != nil {
		t.Fatalf("expected JSON line, got: %s\nerr=%v", stdout.String(), err)
	}
	if rec["name"] != "post-commit-bbb.md" {
		t.Errorf("name = %v", rec["name"])
	}
	if !strings.Contains(rec["body"].(string), "json-mode") {
		t.Errorf("body missing content: %v", rec["body"])
	}
}

func TestRunAntiTruncTail_SinceFilter(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "audit", "antitrunc")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "post-commit-aaa.md"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(out, "post-commit-zzz.md"), []byte("new"), 0o644)

	var stdout, stderr bytes.Buffer
	rc := runAntiTruncTail([]string{"-repo", dir, "-since", "post-commit-mmm.md"}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if strings.Contains(stdout.String(), "post-commit-aaa.md") {
		t.Errorf("aaa should be filtered out by --since: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "post-commit-zzz.md") {
		t.Errorf("zzz should be present: %s", stdout.String())
	}
}
