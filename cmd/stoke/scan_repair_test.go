package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// writeFixtureSemanticPatterns installs a 3-pattern semantic-patterns.md
// under the test repo so parseSemanticPatterns has something real to
// chew on without copying the full 20-pattern production file.
func writeFixtureSemanticPatterns(t *testing.T, repo string) {
	t.Helper()
	dir := filepath.Join(repo, ".claude", "scripts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := `# Semantic Scan Patterns
Header prose ignored.

## 1. INCOMPLETE_IMPL (critical)
Every function: does every code path return a meaningful value?

## 2. MISSING_AUTH (critical)
Every route: auth checked? Authorization (role/permission) checked?

## 3. DEAD_CODE (medium)
Exported functions never imported. Variables never read.
`
	if err := os.WriteFile(filepath.Join(dir, "semantic-patterns.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// writeFixtureSection fabricates one section-0000.txt with the given
// pretend-files + a matching set of .ts files so audit/sections/
// points at real readable paths.
func writeFixtureSection(t *testing.T, repo string, files []string) string {
	t.Helper()
	sectionsDir := filepath.Join(repo, "audit", "sections")
	if err := os.MkdirAll(sectionsDir, 0755); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	for _, f := range files {
		full := filepath.Join(repo, f)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("// placeholder body\nexport function foo() {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		buf.WriteString("./" + f + "\n")
	}
	p := filepath.Join(sectionsDir, "section-0000.txt")
	if err := os.WriteFile(p, []byte(buf.String()), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestParseSemanticPatterns_Fixture asserts the parser finds all three
// fixture patterns, slugs them correctly, and captures the body text.
func TestParseSemanticPatterns_Fixture(t *testing.T) {
	repo := t.TempDir()
	writeFixtureSemanticPatterns(t, repo)
	patterns, err := parseSemanticPatterns(filepath.Join(repo, ".claude", "scripts", "semantic-patterns.md"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(patterns) != 3 {
		t.Fatalf("expected 3 patterns, got %d", len(patterns))
	}
	want := []struct{ Name, Slug string }{
		{"INCOMPLETE_IMPL", "incomplete-impl"},
		{"MISSING_AUTH", "missing-auth"},
		{"DEAD_CODE", "dead-code"},
	}
	for i, w := range want {
		if patterns[i].Name != w.Name {
			t.Errorf("pattern[%d].Name = %q, want %q", i, patterns[i].Name, w.Name)
		}
		if patterns[i].Slug != w.Slug {
			t.Errorf("pattern[%d].Slug = %q, want %q", i, patterns[i].Slug, w.Slug)
		}
		if patterns[i].Body == "" {
			t.Errorf("pattern[%d].Body empty", i)
		}
	}
	// Spot-check the body capture — INCOMPLETE_IMPL should mention
	// "meaningful value", confirming we kept the paragraph text.
	if !strings.Contains(patterns[0].Body, "meaningful value") {
		t.Errorf("INCOMPLETE_IMPL body missing expected text: %q", patterns[0].Body)
	}
}

// TestBuildSemanticPrompt_ContainsPatternAndSection asserts the
// prompt builder includes the pattern body, the sentinel literal
// ("None."), and the section file content — all of which are load-
// bearing contract points the worker relies on.
func TestBuildSemanticPrompt_ContainsPatternAndSection(t *testing.T) {
	repo := t.TempDir()
	sectionFile := writeFixtureSection(t, repo, []string{"src/a.ts", "src/b.ts"})
	pattern := semanticPattern{
		Name: "INCOMPLETE_IMPL",
		Slug: "incomplete-impl",
		Body: "Every function should return a meaningful value.",
	}
	prompt := buildSemanticPrompt(sectionFile, pattern)
	if !strings.Contains(prompt, "Every function should return a meaningful value.") {
		t.Errorf("prompt missing pattern body: %q", prompt)
	}
	if !strings.Contains(prompt, "If zero findings, reply exactly: `None.`") {
		t.Errorf("prompt missing None. sentinel instruction")
	}
	if !strings.Contains(prompt, "./src/a.ts") || !strings.Contains(prompt, "./src/b.ts") {
		t.Errorf("prompt missing section file content: %q", prompt)
	}
	// Section basename must appear (so the worker can sanity-check
	// which section it's scanning).
	if !strings.Contains(prompt, "section-0000.txt") {
		t.Errorf("prompt missing section basename")
	}
}

// TestRunPhase2_ParallelDispatch asserts that the Phase 2 worker
// pool actually issues one call per (section, pattern) and that
// concurrency is honored. Uses a fast mocked semanticCaller so the
// test runs in milliseconds.
func TestRunPhase2_ParallelDispatch(t *testing.T) {
	repo := t.TempDir()
	writeFixtureSemanticPatterns(t, repo)

	// Create three distinct section files on disk.
	sectionsDir := filepath.Join(repo, "audit", "sections")
	_ = os.MkdirAll(sectionsDir, 0755)
	for i := 0; i < 3; i++ {
		p := filepath.Join(sectionsDir, "section-000"+string(rune('0'+i))+".txt")
		_ = os.WriteFile(p, []byte("./fake.ts\n"), 0644)
	}
	got, _ := filepath.Glob(filepath.Join(sectionsDir, "section-*.txt"))
	if len(got) != 3 {
		t.Fatalf("expected 3 sections on disk, got %d", len(got))
	}

	var callCount int64
	var maxConcurrent int64
	var currentConcurrent int64
	cfg := &scanRepairConfig{
		Repo:         repo,
		SemanticFile: filepath.Join(repo, ".claude", "scripts", "semantic-patterns.md"),
		MaxSections:  10,
		MaxPatterns:  10,
		Workers:      4,
		semanticCaller: func(ctx context.Context, dir, prompt string) string {
			atomic.AddInt64(&callCount, 1)
			cur := atomic.AddInt64(&currentConcurrent, 1)
			for {
				old := atomic.LoadInt64(&maxConcurrent)
				if cur <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, cur) {
					break
				}
			}
			// Short sleep so concurrent calls overlap and maxConcurrent
			// has a chance to record >1. Using a brief delay so slow
			// CI doesn't amplify this into a long test.
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt64(&currentConcurrent, -1)
			return "- finding 1\n- finding 2\n"
		},
	}
	p1 := &phase1Result{Sections: got, NumSections: len(got)}
	res, err := runPhase2(context.Background(), cfg, p1)
	if err != nil {
		t.Fatalf("runPhase2: %v", err)
	}
	expectedCalls := int64(len(got) * 3) // sections × 3 patterns
	if atomic.LoadInt64(&callCount) != expectedCalls {
		t.Errorf("callCount = %d, want %d", callCount, expectedCalls)
	}
	if res.CallsMade != int(expectedCalls) {
		t.Errorf("res.CallsMade = %d, want %d", res.CallsMade, expectedCalls)
	}
	if maxConcurrent < 2 {
		t.Errorf("expected >=2 concurrent calls, got %d", maxConcurrent)
	}
	// Report must exist.
	if _, err := os.Stat(filepath.Join(repo, "audit", "semantic-report.md")); err != nil {
		t.Errorf("semantic-report.md not created: %v", err)
	}
}

// TestRunPhase2_CapsEnforced asserts --max-sections and
// --max-patterns truly clamp the number of calls dispatched.
func TestRunPhase2_CapsEnforced(t *testing.T) {
	repo := t.TempDir()
	writeFixtureSemanticPatterns(t, repo)
	// 4 sections on disk but cap to 2.
	for i := 0; i < 4; i++ {
		p := filepath.Join(repo, "audit", "sections",
			"section-000"+string(rune('0'+i))+".txt")
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("./fake.ts\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := filepath.Glob(filepath.Join(repo, "audit", "sections", "section-*.txt"))

	var callCount int64
	cfg := &scanRepairConfig{
		Repo:         repo,
		SemanticFile: filepath.Join(repo, ".claude", "scripts", "semantic-patterns.md"),
		MaxSections:  2, // cap
		MaxPatterns:  2, // cap (3 patterns available)
		Workers:      2,
		semanticCaller: func(ctx context.Context, dir, prompt string) string {
			atomic.AddInt64(&callCount, 1)
			return "None."
		},
	}
	p1 := &phase1Result{Sections: got, NumSections: len(got)}
	if _, err := runPhase2(context.Background(), cfg, p1); err != nil {
		t.Fatal(err)
	}
	// 2 sections × 2 patterns = 4 calls.
	if atomic.LoadInt64(&callCount) != 4 {
		t.Errorf("expected 4 calls under caps, got %d", callCount)
	}
}

// TestCountSemanticFindings covers None., empty, list, and prose
// reply shapes.
func TestCountSemanticFindings(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"empty", "", 0},
		{"none sentinel", "None.", 0},
		{"none lowercase", "none.", 0},
		{"single list item", "- found at foo.ts:12", 1},
		{"three list items", "- a\n- b\n- c\n", 3},
		{"numeric list", "1. issue\n2. issue\n", 2},
		{"prose", "We found a bug in the main function.", 1},
	}
	for _, c := range cases {
		got := countSemanticFindings(c.body)
		if got != c.want {
			t.Errorf("%s: countSemanticFindings(%q) = %d, want %d", c.name, c.body, got, c.want)
		}
	}
}

// TestBuildReviewerPrompt_Structure asserts the Phase 3 reviewer
// prompt contains the anchor phrases our operator contract depends
// on. Breaking this prompt silently would mean the reviewer
// misinterprets the task.
func TestBuildReviewerPrompt_Structure(t *testing.T) {
	prompt := buildReviewerPrompt("DET_REPORT_BODY", "SEM_REPORT_BODY")
	for _, anchor := range []string{
		"tech-lead",
		"SOW",
		"have an ID (T1, T2, ...)",
		"severity (critical / high / medium / low)",
		"Do not invent findings",
		"DET_REPORT_BODY",
		"SEM_REPORT_BODY",
	} {
		if !strings.Contains(prompt, anchor) {
			t.Errorf("reviewer prompt missing anchor %q", anchor)
		}
	}
}

// TestRunPhase3_WritesFixSOW_FromReviewer asserts that a successful
// reviewer call writes the reply verbatim to FIX_SOW.md and returns
// the path.
func TestRunPhase3_WritesFixSOW_FromReviewer(t *testing.T) {
	repo := t.TempDir()
	// Produce the two source reports the phase reads.
	auditDir := filepath.Join(repo, "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(auditDir, "deterministic-report.md"), []byte("det"), 0644)
	_ = os.WriteFile(filepath.Join(auditDir, "semantic-report.md"), []byte("sem"), 0644)

	cfg := &scanRepairConfig{
		Repo: repo,
		reviewerCaller: func(ctx context.Context, dir, prompt string) string {
			return "# Fix SOW\n\n## critical\n\n- [ ] T1: fix widgets.ts — the auth check is inverted.\n"
		},
	}
	p1 := &phase1Result{DeterministicFindings: 1}
	p2 := &phase2Result{FindingsCount: 1}
	sowPath, err := runPhase3(context.Background(), cfg, p1, p2)
	if err != nil {
		t.Fatalf("runPhase3: %v", err)
	}
	if sowPath != filepath.Join(repo, "FIX_SOW.md") {
		t.Errorf("unexpected sowPath: %s", sowPath)
	}
	data, err := os.ReadFile(sowPath)
	if err != nil {
		t.Fatalf("read FIX_SOW.md: %v", err)
	}
	if !strings.Contains(string(data), "T1: fix widgets.ts") {
		t.Errorf("FIX_SOW.md missing task body: %s", data)
	}
}

// TestRunPhase3_StubOnEmptyReviewer asserts the fallback stub SOW
// fires when the reviewer returns empty output.
func TestRunPhase3_StubOnEmptyReviewer(t *testing.T) {
	repo := t.TempDir()
	auditDir := filepath.Join(repo, "audit")
	_ = os.MkdirAll(auditDir, 0755)
	_ = os.WriteFile(filepath.Join(auditDir, "deterministic-report.md"), []byte("det"), 0644)
	_ = os.WriteFile(filepath.Join(auditDir, "semantic-report.md"), []byte("sem"), 0644)

	cfg := &scanRepairConfig{
		Repo: repo,
		reviewerCaller: func(ctx context.Context, dir, prompt string) string {
			return ""
		},
	}
	p1 := &phase1Result{DeterministicFindings: 3, SecurityFindings: 1}
	p2 := &phase2Result{FindingsCount: 5}
	sowPath, err := runPhase3(context.Background(), cfg, p1, p2)
	if err != nil {
		t.Fatalf("runPhase3: %v", err)
	}
	data, _ := os.ReadFile(sowPath)
	if !strings.Contains(string(data), "reviewer call failed") {
		t.Errorf("expected stub SOW, got: %s", data)
	}
	// Counters should flow through the stub.
	for _, want := range []string{"Deterministic findings: 3", "Security findings: 1", "Semantic findings: 5"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("stub SOW missing %q", want)
		}
	}
}

// TestBuildPhase4Args_ModeSelection asserts the subcommand wiring —
// sow vs simple-loop — picks the right top-level args. Exercising
// the helper directly keeps us from needing to spawn a subprocess
// just to validate flag routing.
func TestBuildPhase4Args_ModeSelection(t *testing.T) {
	cases := []struct {
		mode         string
		wantContains []string
	}{
		{"sow", []string{"sow", "--file", "--per-task-worktree"}},
		{"simple-loop", []string{"simple-loop", "--file", "--reviewer"}},
	}
	for _, c := range cases {
		cfg := &scanRepairConfig{
			Repo:        "/tmp/fake",
			Mode:        c.mode,
			WorkerModel: "sonnet",
			Reviewer:    "codex",
			ClaudeBin:   "claude",
			CodexBin:    "codex",
		}
		got := buildPhase4Args(cfg, "/tmp/fake/FIX_SOW.md")
		for _, anchor := range c.wantContains {
			found := false
			for _, a := range got {
				if a == anchor {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("mode=%s args missing %q: %v", c.mode, anchor, got)
			}
		}
	}
}

// TestCleanAuditArtifacts removes the audit/ dir and FIX_SOW.md.
func TestCleanAuditArtifacts(t *testing.T) {
	repo := t.TempDir()
	auditDir := filepath.Join(repo, "audit", "scans")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(auditDir, "thing.md"), []byte("old"), 0644)
	_ = os.WriteFile(filepath.Join(repo, "FIX_SOW.md"), []byte("old"), 0644)
	if err := cleanAuditArtifacts(repo); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "audit")); !os.IsNotExist(err) {
		t.Errorf("audit dir survived clean")
	}
	if _, err := os.Stat(filepath.Join(repo, "FIX_SOW.md")); !os.IsNotExist(err) {
		t.Errorf("FIX_SOW.md survived clean")
	}
}

// TestPrioritizeAggregatedFindings_UnderCapIsNoOp asserts the
// prioritizer returns the input unchanged when already below the
// character cap. No dropping, no copying — cheap path on small runs.
func TestPrioritizeAggregatedFindings_UnderCapIsNoOp(t *testing.T) {
	agg := "## Section\n\n- [CRITICAL] a.go:1 — issue — fix: do x\n- [HIGH] b.go:2 — issue — fix: do y\n"
	out, dropped := prioritizeAggregatedFindings(agg, 10*1024)
	if out != agg {
		t.Fatalf("prioritizer mutated input under cap: got %q want %q", out, agg)
	}
	if len(dropped) != 0 {
		t.Fatalf("dropped=%v; want empty under cap", dropped)
	}
}

// TestPrioritizeAggregatedFindings_DropsLowFirst asserts that when
// truncation is needed, LOW severities go first, MEDIUM last, and
// CRITICAL/HIGH are preserved. Recovers the H-21 guarantee: the
// reviewer should never see LOW-severity noise at the cost of a
// CRITICAL finding.
func TestPrioritizeAggregatedFindings_DropsLowFirst(t *testing.T) {
	// Build a large buffer: 1 CRITICAL + 1 HIGH + many LOW + many MEDIUM
	// so we're forced to drop. Each LOW line is ~80 chars; we want the
	// total to exceed the cap.
	var buf strings.Builder
	buf.WriteString("## Deterministic\n\n")
	buf.WriteString("- [CRITICAL] core.go:1 — secret leak — fix: rotate the key now\n")
	buf.WriteString("- [HIGH] auth.go:2 — bypass — fix: enforce check\n")
	for i := 0; i < 200; i++ {
		buf.WriteString("- [LOW] trivial.go:42 — style nit — fix: rename local variable here\n")
	}
	for i := 0; i < 50; i++ {
		buf.WriteString("- [MEDIUM] mid.go:10 — error handling — fix: wrap errors with context\n")
	}
	// Cap at 4 KiB — forces substantial drops.
	out, dropped := prioritizeAggregatedFindings(buf.String(), 4*1024)
	if len(out) > 4*1024+200 { // leave small slack for the truncation banner
		t.Fatalf("out len=%d exceeds cap+slack", len(out))
	}
	// CRITICAL and HIGH must survive.
	if !strings.Contains(out, "core.go:1") {
		t.Fatalf("CRITICAL finding dropped; output: %s", out)
	}
	if !strings.Contains(out, "auth.go:2") {
		t.Fatalf("HIGH finding dropped; output: %s", out)
	}
	// LOW should be dropped before MEDIUM.
	if dropped["LOW"] == 0 {
		t.Fatalf("expected LOW to be dropped; dropped=%v", dropped)
	}
	// We should have dropped ALL low findings (200) before starting on
	// medium. Sanity check: low drop count >= medium drop count.
	if dropped["LOW"] < dropped["MEDIUM"] {
		t.Fatalf("LOW(%d) should drop before MEDIUM(%d); dropped=%v",
			dropped["LOW"], dropped["MEDIUM"], dropped)
	}
}

// TestPrioritizeAggregatedFindings_RDeepScale simulates the R-deep
// failure mode: 40K LOW + MEDIUM finding lines that would otherwise
// blow ARG_MAX (~128 KiB). With the H-21 cap the output MUST be
// under the cap AND MUST preserve any CRITICAL/HIGH that exist.
func TestPrioritizeAggregatedFindings_RDeepScale(t *testing.T) {
	var buf strings.Builder
	buf.WriteString("## Security vectors\n\n")
	buf.WriteString("- [CRITICAL] api.go:5 — SQL injection — fix: parameterize query\n")
	for i := 0; i < 40_000; i++ {
		buf.WriteString("- [LOW] x.go:1 — trivial — fix: cleanup\n")
	}
	cap := 100 * 1024
	out, _ := prioritizeAggregatedFindings(buf.String(), cap)
	if len(out) > cap+256 {
		t.Fatalf("out len=%d exceeds cap+256; H-21 cap not enforced", len(out))
	}
	if !strings.Contains(out, "SQL injection") {
		t.Fatalf("CRITICAL dropped on R-deep-scale input; output head: %s", safeHead(out, 500))
	}
}

// safeHead returns the first n chars of s for error messages.
func safeHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestBuildPhase4Args_ForwardsProviderFlags asserts the H-22
// passthroughs: when --native-base-url / --native-api-key / etc. are
// set on the scanRepairConfig they reach the sow sub-invocation.
func TestBuildPhase4Args_ForwardsProviderFlags(t *testing.T) {
	cfg := &scanRepairConfig{
		Repo:             "/tmp/fake",
		Mode:             "sow",
		WorkerModel:      "claude-sonnet-4-6",
		Runner:           "native",
		NativeBaseURL:    "http://localhost:8000",
		NativeAPIKey:     "sk-test",
		ReasoningBaseURL: "http://localhost:8001",
		ReasoningAPIKey:  "sk-reasoning",
		ReasoningModel:   "opus",
		Workers:          8,
	}
	args := buildPhase4Args(cfg, "/tmp/fake/FIX_SOW.md")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--runner native",
		"--native-base-url http://localhost:8000",
		"--native-api-key sk-test",
		"--native-model claude-sonnet-4-6",
		"--reasoning-base-url http://localhost:8001",
		"--reasoning-api-key sk-reasoning",
		"--reasoning-model opus",
		"--workers 8",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sow args missing %q; got: %s", want, joined)
		}
	}
}

// TestBuildPhase4Args_SimpleLoopForwardsFlags covers the
// simple-loop passthroughs (--claude-model, --fix-mode, --max-rounds,
// --tier-filter-*). Without these, H-22 reports that scan-repair
// Phase 4 runs with defaults that mismatch the parent invocation.
func TestBuildPhase4Args_SimpleLoopForwardsFlags(t *testing.T) {
	cfg := &scanRepairConfig{
		Repo:             "/tmp/fake",
		Mode:             "simple-loop",
		ClaudeBin:        "claude",
		CodexBin:         "codex",
		Reviewer:         "codex",
		ClaudeModel:      "opus",
		FixMode:          "parallel",
		MaxRounds:        7,
		TierFilterAfter:  3,
		TierFilterThresh: 0.8,
	}
	args := buildPhase4Args(cfg, "/tmp/fake/FIX_SOW.md")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--claude-model opus",
		"--fix-mode parallel",
		"--max-rounds 7",
		"--tier-filter-after 3",
		"--tier-filter-threshold 0.8",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("simple-loop args missing %q; got: %s", want, joined)
		}
	}
}

// TestBuildPhase4Args_OmitsEmptyFlags asserts we don't forward
// flags that weren't set on the parent. Passing bogus empty --runner
// / --native-api-key to the child would mask its own defaults.
func TestBuildPhase4Args_OmitsEmptyFlags(t *testing.T) {
	cfg := &scanRepairConfig{
		Repo:        "/tmp/fake",
		Mode:        "sow",
		WorkerModel: "sonnet",
		// No runner / native-* / reasoning-* / workers set.
		// Workers=0 should also be skipped.
	}
	args := buildPhase4Args(cfg, "/tmp/fake/FIX_SOW.md")
	joined := strings.Join(args, " ")
	for _, notWant := range []string{"--runner", "--native-api-key",
		"--native-base-url", "--reasoning-api-key", "--reasoning-base-url",
		"--reasoning-model", "--workers"} {
		if strings.Contains(joined, notWant) {
			t.Errorf("unset flag %q leaked into sow args: %s", notWant, joined)
		}
	}
	// --native-model should still appear (derived from WorkerModel).
	if !strings.Contains(joined, "--native-model sonnet") {
		t.Errorf("expected --native-model derived from WorkerModel; got: %s", joined)
	}
}

// TestSlugify covers the common shapes we get from pattern names.
func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"INCOMPLETE_IMPL", "incomplete-impl"},
		{"FAKE_TESTS", "fake-tests"},
		{"MOCK_IN_PRODUCTION", "mock-in-production"},
		{"___", "pattern"}, // all-dashes collapses to "pattern"
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEndToEnd_SmokeAllPhases threads a full scan-repair run through
// mocked Phase 1 + semantic + reviewer hooks and asserts the final
// FIX_SOW.md exists with the reviewer's text. This is the integration
// smoke test — it exercises the orchestration glue (early-exit
// check, phase sequencing, Phase 4 stub).
func TestEndToEnd_SmokeAllPhases(t *testing.T) {
	repo := t.TempDir()
	writeFixtureSemanticPatterns(t, repo)

	// Fake Phase 1 result: pretend the shell-outs produced one
	// section with one deterministic finding.
	sectionFile := writeFixtureSection(t, repo, []string{"src/main.ts"})
	fakePh1 := func(ctx context.Context, cfg *scanRepairConfig) (*phase1Result, error) {
		// Write minimal deterministic-report.md so Phase 3 can read it.
		_ = os.MkdirAll(filepath.Join(cfg.Repo, "audit"), 0755)
		_ = os.WriteFile(filepath.Join(cfg.Repo, "audit", "deterministic-report.md"),
			[]byte("# Deterministic\n- [critical] src/main.ts: placeholder comment\n"), 0644)
		return &phase1Result{
			NumSections:           1,
			DeterministicFindings: 1,
			SecurityFindings:      0,
			Sections:              []string{sectionFile},
		}, nil
	}

	var reviewerCalls int64
	var phase4Calls int64
	cfg := &scanRepairConfig{
		Repo:                repo,
		WorkerModel:         "sonnet",
		Reviewer:            "codex",
		Mode:                "sow",
		MaxSections:         5,
		MaxPatterns:         5,
		Workers:             2,
		ClaudeBin:           "claude",
		CodexBin:            "codex",
		SemanticFile:        filepath.Join(repo, ".claude", "scripts", "semantic-patterns.md"),
		// H-17: skip the new Phase 2b/2c/2d sub-phases in this smoke
		// test — they each have their own dedicated tests.
		SkipSecurityVectors: true,
		SkipPersonas:        true,
		SkipCodexReview:     true,
		phase1Runner:        fakePh1,
		semanticCaller: func(ctx context.Context, dir, prompt string) string {
			return "- [CRITICAL] src/main.ts:1 — body comment — fix: remove stray line\n"
		},
		reviewerCaller: func(ctx context.Context, dir, prompt string) string {
			atomic.AddInt64(&reviewerCalls, 1)
			// Reviewer fires for dedupe (3b), TIER filter (3c), AND per-
			// section fix-tasks (3d). Detect which is which by substring.
			switch {
			case strings.Contains(prompt, "## TIER 1"):
				return "## TIER 1\n\n- [CRITICAL] src/main.ts:1 — body comment — fix: remove stray line\n\n## TIER 2 (small/medium effort)\n\n## TIER 3 (dropped)\n"
			case strings.Contains(prompt, "De-duplicate"):
				return "- [CRITICAL] src/main.ts:1 — body comment — fix: remove stray line\n"
			default:
				return "- [ ] FIX-src-1: remove stray line in src/main.ts\n"
			}
		},
		phase4Runner: func(ctx context.Context, cfg *scanRepairConfig, sowPath string) error {
			atomic.AddInt64(&phase4Calls, 1)
			return nil
		},
	}
	if err := runScanRepair(context.Background(), cfg); err != nil {
		t.Fatalf("runScanRepair: %v", err)
	}
	// FIX_SOW.md should exist and contain the fix-task output from 3d.
	data, err := os.ReadFile(filepath.Join(repo, "FIX_SOW.md"))
	if err != nil {
		t.Fatalf("FIX_SOW.md not created: %v", err)
	}
	if !strings.Contains(string(data), "FIX-src-1: remove stray line in src/main.ts") {
		t.Errorf("FIX_SOW.md missing fix-task content: %s", data)
	}
	// Reviewer fires at least twice (dedupe + TIER) plus once per
	// section for 3d fix-tasks.
	if atomic.LoadInt64(&reviewerCalls) < 2 {
		t.Errorf("reviewer should be called >=2 times (dedupe+TIER), got %d", reviewerCalls)
	}
	if atomic.LoadInt64(&phase4Calls) != 1 {
		t.Errorf("phase4 should be called exactly once, got %d", phase4Calls)
	}
	if _, err := os.Stat(filepath.Join(repo, "audit", "semantic-report.md")); err != nil {
		t.Errorf("semantic-report.md not created: %v", err)
	}
}

// TestEndToEnd_EarlyExitOnZeroFindings asserts that when Phase 1
// and Phase 2 both return 0 findings, Phase 3/4 are skipped and we
// emit the "audit found no issues" FIX_SOW.md stub.
func TestEndToEnd_EarlyExitOnZeroFindings(t *testing.T) {
	repo := t.TempDir()
	writeFixtureSemanticPatterns(t, repo)
	sectionFile := writeFixtureSection(t, repo, []string{"src/clean.ts"})

	var reviewerCalls int64
	var phase4Calls int64
	cfg := &scanRepairConfig{
		Repo:                repo,
		Mode:                "sow",
		MaxSections:         5,
		MaxPatterns:         5,
		Workers:             2,
		SemanticFile:        filepath.Join(repo, ".claude", "scripts", "semantic-patterns.md"),
		// H-17: skip the new 2b/2c/2d sub-phases for this clean-audit
		// scenario — covered by dedicated tests below.
		SkipSecurityVectors: true,
		SkipPersonas:        true,
		SkipCodexReview:     true,
		phase1Runner: func(ctx context.Context, cfg *scanRepairConfig) (*phase1Result, error) {
			_ = os.MkdirAll(filepath.Join(cfg.Repo, "audit"), 0755)
			_ = os.WriteFile(filepath.Join(cfg.Repo, "audit", "deterministic-report.md"),
				[]byte("# Deterministic\n\nNo findings.\n"), 0644)
			return &phase1Result{
				NumSections: 1,
				Sections:    []string{sectionFile},
			}, nil
		},
		semanticCaller: func(ctx context.Context, dir, prompt string) string {
			return "None."
		},
		reviewerCaller: func(ctx context.Context, dir, prompt string) string {
			atomic.AddInt64(&reviewerCalls, 1)
			// If reviewer does get called, return something that
			// reduces to zero findings so 3e short-circuits.
			return "None."
		},
		phase4Runner: func(ctx context.Context, cfg *scanRepairConfig, sowPath string) error {
			atomic.AddInt64(&phase4Calls, 1)
			return nil
		},
	}
	if err := runScanRepair(context.Background(), cfg); err != nil {
		t.Fatalf("runScanRepair: %v", err)
	}
	// Phase 4 must not fire when approved == 0.
	if phase4Calls != 0 {
		t.Errorf("phase4 fired %d times on clean audit; want 0", phase4Calls)
	}
	// FIX_SOW.md should exist with the no-issues banner.
	data, _ := os.ReadFile(filepath.Join(repo, "FIX_SOW.md"))
	if !strings.Contains(string(data), "no high-impact issues") {
		t.Errorf("FIX_SOW.md should mention 'no high-impact issues', got: %s", data)
	}
	// Also audit/scan-complete.md should be written by Phase 3e.
	if _, err := os.Stat(filepath.Join(repo, "audit", "scan-complete.md")); err != nil {
		t.Errorf("audit/scan-complete.md not written: %v", err)
	}
}

// TestRunPhase1_FixtureShellPipeline exercises the Phase 1 runner
// end-to-end on a tiny fixture repo with a fabricated project-mapper
// + deterministic-scan + security scripts. No LLMs. This catches
// shell-quoting regressions and confirms the aggregate report is
// produced with the expected counts.
func TestRunPhase1_FixtureShellPipeline(t *testing.T) {
	repo := t.TempDir()
	scriptsDir := filepath.Join(repo, ".claude", "scripts", "security")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Fabricate a project-mapper.sh that writes ONE section with
	// two files listed.
	mapper := filepath.Join(repo, ".claude", "scripts", "project-mapper.sh")
	mapperBody := `#!/bin/bash
set -e
mkdir -p audit/sections
echo "./src/a.ts" > audit/sections/section-0000.txt
echo "./src/b.ts" >> audit/sections/section-0000.txt
echo "# Project Map" > audit/project-map.md
echo "2 files | 10 lines | 1 sections"
`
	if err := os.WriteFile(mapper, []byte(mapperBody), 0755); err != nil {
		t.Fatal(err)
	}
	// Fabricate a deterministic-scan.sh that emits 3 findings.
	detScan := filepath.Join(repo, ".claude", "scripts", "deterministic-scan.sh")
	detBody := `#!/bin/bash
echo "# Deterministic Scan"
echo "## Findings (critical:2 high:1 medium:0)"
echo "- [critical] src/a.ts:1 — placeholder"
echo "- [critical] src/b.ts:2 — placeholder"
echo "- [high] src/a.ts:3 — console debug"
`
	if err := os.WriteFile(detScan, []byte(detBody), 0755); err != nil {
		t.Fatal(err)
	}
	// Fabricate security scripts that each write a single-row CSV.
	for _, name := range []string{"scan_inputs.py", "scan_dataflow.py", "scan_config.py"} {
		p := filepath.Join(scriptsDir, name)
		body := `#!/usr/bin/env python3
import sys
out = None
for i, a in enumerate(sys.argv):
    if a == "--output":
        out = sys.argv[i+1]
with open(out, "w") as f:
    f.write("header1,header2\n")
    f.write("finding1,value\n")
`
		if err := os.WriteFile(p, []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Create the fake source files (project-mapper.sh writes paths
	// but deterministic-scan doesn't actually need them to exist).
	_ = os.MkdirAll(filepath.Join(repo, "src"), 0755)
	_ = os.WriteFile(filepath.Join(repo, "src", "a.ts"), []byte("// placeholder\n"), 0644)
	_ = os.WriteFile(filepath.Join(repo, "src", "b.ts"), []byte("// placeholder\n"), 0644)

	cfg := &scanRepairConfig{
		Repo: repo,
	}
	res, err := runPhase1(context.Background(), cfg)
	if err != nil {
		t.Fatalf("runPhase1: %v", err)
	}
	if res.NumSections != 1 {
		t.Errorf("NumSections = %d, want 1", res.NumSections)
	}
	if res.DeterministicFindings != 3 {
		t.Errorf("DeterministicFindings = %d, want 3", res.DeterministicFindings)
	}
	if res.SecurityFindings != 3 {
		t.Errorf("SecurityFindings = %d, want 3 (1 per script × 3 scripts)", res.SecurityFindings)
	}
	// Aggregate report must exist and mention all counts.
	rpt, err := os.ReadFile(filepath.Join(repo, "audit", "deterministic-report.md"))
	if err != nil {
		t.Fatalf("deterministic-report.md missing: %v", err)
	}
	if !strings.Contains(string(rpt), "Deterministic findings: 3") {
		t.Errorf("report missing det findings count: %s", rpt)
	}
	if !strings.Contains(string(rpt), "Security findings: 3") {
		t.Errorf("report missing security count: %s", rpt)
	}
}
