package main

import (
	"bytes"
	"encoding/json"
	"os"
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

// initRepo creates a tmp git repo with the supplied subjects/bodies as a
// chain of empty commits. Returns the repo dir. Git mechanics delegate to the
// shared testhelpers helpers (fixed identity, explicit cmd.Dir).
func initRepo(t *testing.T, changes []struct{ Subject, Body string }) string {
	t.Helper()
	dir := t.TempDir()
	gitInitAt(t, dir)
	runGitIn(t, dir, "commit", "--allow-empty", "-q", "-m", "initial")
	for _, c := range changes {
		msg := c.Subject
		if c.Body != "" {
			msg += "\n\n" + c.Body
		}
		runGitIn(t, dir, "commit", "--allow-empty", "-q", "-m", msg)
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

// writeHookFixture lays out a minimal repo containing a plan file and
// optionally an assistant-output input file. Returns (repo, planPath,
// inputPath). planPath is relative to repo (matches how operators pass
// --plan). inputPath is absolute (matches how --input is passed).
func writeHookFixture(t *testing.T, planBody, inputText string) (repo, planRel, inputAbs string) {
	t.Helper()
	repo = t.TempDir()
	plansDir := filepath.Join(repo, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	planRel = "plans/build-plan.md"
	if err := os.WriteFile(filepath.Join(repo, planRel), []byte(planBody), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if inputText != "" {
		inputAbs = filepath.Join(repo, "input.txt")
		if err := os.WriteFile(inputAbs, []byte(inputText), 0o644); err != nil {
			t.Fatalf("write input: %v", err)
		}
	}
	return repo, planRel, inputAbs
}

// parseHookEnvelope decodes one JSON line from stdout and asserts the
// verb is the canonical "antitrunc.verify" string. Mirrors the
// envelope shape declared in cmd/r1/antitrunc_cmd.go::hookEnvelope.
func parseHookEnvelope(t *testing.T, stdoutBytes []byte) map[string]any {
	t.Helper()
	trimmed := bytes.TrimRight(stdoutBytes, "\n")
	if bytes.Count(trimmed, []byte("\n")) != 0 {
		t.Fatalf("expected single JSON line, got multiple lines:\n%s", stdoutBytes)
	}
	var env map[string]any
	if err := json.Unmarshal(trimmed, &env); err != nil {
		t.Fatalf("json.Unmarshal: %v\nbody=%s", err, stdoutBytes)
	}
	if env["verb"] != "antitrunc.verify" {
		t.Errorf("verb = %v, want antitrunc.verify", env["verb"])
	}
	return env
}

func TestAntitruncVerify_HookMode_CleanInputExits0(t *testing.T) {
	// All plan items checked + no truncation phrase in input → ok.
	repo, planRel, inputAbs := writeHookFixture(t,
		"<!-- STATUS: done -->\n- [x] one\n- [x] two\n",
		"normal helpful assistant text with no flagged phrases at all.\n",
	)
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncVerify(
		[]string{"-repo", repo, "-hook-mode", "-plan", planRel, "-input", inputAbs},
		&stdout, &stderr,
	)
	if rc != 0 {
		t.Fatalf("rc = %d, want 0; stderr=%s\nstdout=%s", rc, stderr.String(), stdout.String())
	}
	env := parseHookEnvelope(t, stdout.Bytes())
	if env["status"] != "ok" {
		t.Errorf("status = %v, want ok", env["status"])
	}
	data := env["data"].(map[string]any)
	if data["findings_count"].(float64) != 0 {
		t.Errorf("findings_count = %v, want 0", data["findings_count"])
	}
	if data["plan_items_done"].(float64) != 2 || data["plan_items_total"].(float64) != 2 {
		t.Errorf("plan counts wrong: done=%v total=%v", data["plan_items_done"], data["plan_items_total"])
	}
}

func TestAntitruncVerify_HookMode_PhraseFindingExits2(t *testing.T) {
	// Truncation phrase present in input → expect status:findings
	// with at least one source:"phrase" entry. The phrase
	// "I'll defer" hits the canonical premature_stop_let_me regex
	// in internal/antitrunc/phrases.go.
	const fixtureInput = "Looking at the remaining work, I'll defer the rest to a later turn.\n"
	repo, planRel, inputAbs := writeHookFixture(t,
		"<!-- STATUS: done -->\n- [x] one\n",
		fixtureInput,
	)
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncVerify(
		[]string{"-repo", repo, "-hook-mode", "-plan", planRel, "-input", inputAbs},
		&stdout, &stderr,
	)
	if rc != 2 {
		t.Fatalf("rc = %d, want 2; stderr=%s\nstdout=%s", rc, stderr.String(), stdout.String())
	}
	env := parseHookEnvelope(t, stdout.Bytes())
	if env["status"] != "findings" {
		t.Errorf("status = %v, want findings", env["status"])
	}
	data := env["data"].(map[string]any)
	if data["findings_count"].(float64) < 1 {
		t.Errorf("findings_count = %v, want >=1", data["findings_count"])
	}
	findings := data["findings"].([]any)
	sawPhrase := false
	for _, f := range findings {
		fm := f.(map[string]any)
		if fm["source"] == "phrase" {
			sawPhrase = true
			if fm["phrase_id"] == "" {
				t.Errorf("phrase finding missing phrase_id: %v", fm)
			}
		}
	}
	if !sawPhrase {
		t.Errorf("no phrase-source finding found: %v", findings)
	}
}

func TestAntitruncVerify_HookMode_PlanItemUncheckedExits2(t *testing.T) {
	// Clean input but plan has an unchecked item → status:findings
	// with a source:"scope" entry.
	repo, planRel, inputAbs := writeHookFixture(t,
		"<!-- STATUS: in-progress -->\n- [x] done item\n- [ ] unchecked\n",
		"normal helpful assistant text with nothing flagged.\n",
	)
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncVerify(
		[]string{"-repo", repo, "-hook-mode", "-plan", planRel, "-input", inputAbs},
		&stdout, &stderr,
	)
	if rc != 2 {
		t.Fatalf("rc = %d, want 2; stderr=%s\nstdout=%s", rc, stderr.String(), stdout.String())
	}
	env := parseHookEnvelope(t, stdout.Bytes())
	if env["status"] != "findings" {
		t.Errorf("status = %v, want findings", env["status"])
	}
	data := env["data"].(map[string]any)
	findings := data["findings"].([]any)
	sawScope := false
	for _, f := range findings {
		fm := f.(map[string]any)
		if fm["source"] == "scope" {
			sawScope = true
		}
	}
	if !sawScope {
		t.Errorf("no scope-source finding found: %v", findings)
	}
	if data["plan_items_done"].(float64) != 1 || data["plan_items_total"].(float64) != 2 {
		t.Errorf("plan counts: done=%v total=%v, want 1/2", data["plan_items_done"], data["plan_items_total"])
	}
}

func TestAntitruncVerify_HookMode_EmitsExactlyOneJSONLine(t *testing.T) {
	// Stdout must have exactly one newline (the one fmt.Fprintln
	// appends) AND parse as exactly one JSON object. No banner, no
	// debug, no extras.
	repo, planRel, _ := writeHookFixture(t,
		"<!-- STATUS: done -->\n- [x] only\n", "",
	)
	var stdout, stderr bytes.Buffer
	rc := runAntiTruncVerify(
		[]string{"-repo", repo, "-hook-mode", "-plan", planRel},
		&stdout, &stderr,
	)
	if rc != 0 {
		t.Fatalf("rc = %d, want 0; stderr=%s\nstdout=%s", rc, stderr.String(), stdout.String())
	}
	out := stdout.Bytes()
	if n := bytes.Count(out, []byte("\n")); n != 1 {
		t.Errorf("expected exactly 1 newline in stdout, got %d:\n%q", n, out)
	}
	// Stripping the trailing newline must yield exactly one JSON object.
	body := bytes.TrimRight(out, "\n")
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("not a single JSON object: %v\nbody=%q", err, body)
	}
	// Verify no second object follows.
	dec := json.NewDecoder(bytes.NewReader(body))
	if _, err := dec.Token(); err != nil {
		t.Fatalf("decoder error: %v", err)
	}
}

func TestAntitruncVerify_HookMode_PlanPathFlagHonored(t *testing.T) {
	// Place the plan at a non-default path and assert the envelope's
	// plan_path matches the resolved absolute location AND the
	// findings reflect that plan's checklist state.
	repo := t.TempDir()
	customDir := filepath.Join(repo, "custom-plans")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	customPlan := filepath.Join(customDir, "alt-plan.md")
	if err := os.WriteFile(customPlan,
		[]byte("<!-- STATUS: in-progress -->\n- [ ] a\n- [ ] b\n- [x] c\n"),
		0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	var stdout, stderr bytes.Buffer
	rc := runAntiTruncVerify(
		[]string{"-repo", repo, "-hook-mode", "-plan", customPlan},
		&stdout, &stderr,
	)
	if rc != 2 {
		t.Fatalf("rc = %d, want 2 (2 unchecked items); stderr=%s\nstdout=%s",
			rc, stderr.String(), stdout.String())
	}
	env := parseHookEnvelope(t, stdout.Bytes())
	data := env["data"].(map[string]any)
	if data["plan_path"] != customPlan {
		t.Errorf("plan_path = %v, want %v", data["plan_path"], customPlan)
	}
	if data["plan_items_done"].(float64) != 1 || data["plan_items_total"].(float64) != 3 {
		t.Errorf("plan counts: done=%v total=%v, want 1/3",
			data["plan_items_done"], data["plan_items_total"])
	}
	// Finding for scope must mention the custom path.
	findings := data["findings"].([]any)
	if len(findings) == 0 {
		t.Fatalf("expected at least one finding")
	}
	sawScopeWithPath := false
	for _, f := range findings {
		fm := f.(map[string]any)
		if fm["source"] == "scope" && strings.Contains(fm["detail"].(string), customPlan) {
			sawScopeWithPath = true
		}
	}
	if !sawScopeWithPath {
		t.Errorf("expected scope finding referencing custom plan path; got: %v", findings)
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
