// scan_repair_h17_test.go — H-17/H-18 test coverage for the extended
// scan-repair pipeline. Kept in a separate file from the original
// scan_repair_test.go so the test-cases for the new functionality are
// easy to audit as a single block.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// ------------------------------------------------------------------
// Phase 2b — security vectors
// ------------------------------------------------------------------

// TestPhase2b_FallbackVectorsWhenFileMissing: when vectors.md is
// absent we fall back to fallbackSecurityVectors so the phase still
// dispatches work.
func TestPhase2b_FallbackVectorsWhenFileMissing(t *testing.T) {
	repo := t.TempDir()
	// vectors.md path deliberately unwritten.
	got := parseSecurityVectors(filepath.Join(repo, ".claude", "scripts", "security", "vectors.md"))
	if len(got) != len(fallbackSecurityVectors) {
		t.Fatalf("expected %d fallback vectors, got %d", len(fallbackSecurityVectors), len(got))
	}
	if got[0].Name == "" || got[0].Body == "" {
		t.Errorf("fallback vector missing Name/Body: %+v", got[0])
	}
}

// TestPhase2b_ParsesRealVectorsMD: when vectors.md is present, the
// parser extracts every "## N. NAME" heading with its body.
func TestPhase2b_ParsesRealVectorsMD(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".claude", "scripts", "security")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := `# Security Review Vectors

## 1. Access Control
Role checks, tenant isolation, SSO.

## 2. Injection
SQL, XSS, command, path traversal.

## 3. Cryptography
JWT algorithm, key derivation.
`
	if err := os.WriteFile(filepath.Join(dir, "vectors.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	vectors := parseSecurityVectors(filepath.Join(dir, "vectors.md"))
	if len(vectors) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vectors))
	}
	if vectors[0].Num != 1 || vectors[0].Name != "Access Control" {
		t.Errorf("vec[0] = %+v, want Num=1 Name='Access Control'", vectors[0])
	}
	if !strings.Contains(vectors[1].Body, "SQL") {
		t.Errorf("vec[1] body missing 'SQL': %q", vectors[1].Body)
	}
}

// ------------------------------------------------------------------
// Phase 2c — persona selection + opus preference
// ------------------------------------------------------------------

// TestPhase2c_PersonaDispatchAllWithOpusFlag: --personas=all dispatches
// every parsed persona; the 4 opus-preferred slugs route through opus
// when cfg.OpusBin is set.
func TestPhase2c_PersonaDispatchAllWithOpusFlag(t *testing.T) {
	repo := t.TempDir()
	all := fallbackPersonas() // 17 personas
	// Sanity check: the fallback list has 17 entries.
	if len(all) != 17 {
		t.Fatalf("expected 17 fallback personas, got %d", len(all))
	}

	var totalCalls, opusCalls int64
	seen := map[string]bool{}
	var mu sync.Mutex
	cfg := &scanRepairConfig{
		Repo:              repo,
		PersonasSelection: "all",
		OpusBin:           "/fake/opus",
		Workers:           4,
		personaCaller: func(ctx context.Context, dir, prompt string, preferOpus bool) string {
			atomic.AddInt64(&totalCalls, 1)
			if preferOpus {
				atomic.AddInt64(&opusCalls, 1)
			}
			mu.Lock()
			// Key by any persona slug substring in the prompt so we
			// can assert uniqueness without reverse-engineering the
			// dispatch ordering.
			for _, p := range all {
				if strings.Contains(prompt, p.Name) {
					seen[p.Slug] = true
					break
				}
			}
			mu.Unlock()
			return "None."
		},
	}
	// Inject an in-memory personas file so parseAuditPersonas picks
	// up the full 17-persona set via the production parser path.
	writePersonasFixture(t, repo)

	p1 := &phase1Result{Sections: nil}
	if err := runPhase2cPersonas(context.Background(), cfg, p1); err != nil {
		t.Fatalf("runPhase2cPersonas: %v", err)
	}
	if atomic.LoadInt64(&totalCalls) != 17 {
		t.Errorf("expected 17 persona calls, got %d", totalCalls)
	}
	if atomic.LoadInt64(&opusCalls) != int64(len(opusPreferredSlugs)) {
		t.Errorf("expected %d opus calls, got %d", len(opusPreferredSlugs), opusCalls)
	}
}

// TestPhase2c_CoreSelects8: --personas=core dispatches exactly the 8
// canonical core slugs from corePersonaSlugs.
func TestPhase2c_CoreSelects8(t *testing.T) {
	repo := t.TempDir()
	writePersonasFixture(t, repo)

	var calls int64
	seen := map[string]bool{}
	var mu sync.Mutex
	cfg := &scanRepairConfig{
		Repo:              repo,
		PersonasSelection: "core",
		Workers:           4,
		personaCaller: func(ctx context.Context, dir, prompt string, preferOpus bool) string {
			atomic.AddInt64(&calls, 1)
			mu.Lock()
			for _, slug := range corePersonaSlugs {
				// Match on a disambiguating substring derived from the
				// slug (e.g. "picky-reviewer" persona body contains
				// "Picky Reviewer" — we scan for slug verbatim since
				// our fixture emits the slug in the body).
				if strings.Contains(prompt, slug) {
					seen[slug] = true
					break
				}
			}
			mu.Unlock()
			return "None."
		},
	}
	if err := runPhase2cPersonas(context.Background(), cfg, &phase1Result{}); err != nil {
		t.Fatalf("runPhase2cPersonas: %v", err)
	}
	if atomic.LoadInt64(&calls) != 8 {
		t.Errorf("expected 8 persona calls for core, got %d", calls)
	}
	for _, slug := range corePersonaSlugs {
		if !seen[slug] {
			t.Errorf("core persona %q not dispatched", slug)
		}
	}
}

// ------------------------------------------------------------------
// Phase 2d — codex review
// ------------------------------------------------------------------

// TestPhase2d_MalformedJSONGracefulDegrade: malformed JSON does not
// fail the phase; it writes a note file and returns nil.
func TestPhase2d_MalformedJSONGracefulDegrade(t *testing.T) {
	repo := t.TempDir()
	cfg := &scanRepairConfig{
		Repo: repo,
		codexCaller: func(ctx context.Context, dir string) ([]byte, error) {
			return []byte("{not valid json"), nil
		},
	}
	if err := runPhase2dCodexReview(context.Background(), cfg); err != nil {
		t.Fatalf("runPhase2dCodexReview: %v", err)
	}
	// The note file under audit/perspectives/codex-review.qa.md must
	// exist even on malformed input.
	out := filepath.Join(repo, "audit", "perspectives", "codex-review.qa.md")
	if _, err := os.Stat(out); err != nil {
		t.Errorf("codex-review.qa.md not written: %v", err)
	}
}

// TestPhase2d_SkipWhenNoHook: when codex isn't on PATH AND the script
// isn't present, the phase logs a warning and returns nil (no file
// written, but no error either).
func TestPhase2d_SkipWhenNoHook(t *testing.T) {
	repo := t.TempDir()
	cfg := &scanRepairConfig{
		Repo:     repo,
		CodexBin: "nonexistent-binary-" + t.Name(),
		// No codexCaller override — exercise the real PATH-check path.
	}
	if err := runPhase2dCodexReview(context.Background(), cfg); err != nil {
		t.Fatalf("runPhase2dCodexReview: %v", err)
	}
	// No codex-review.qa.md should be written since codex is missing.
	out := filepath.Join(repo, "audit", "perspectives", "codex-review.qa.md")
	if _, err := os.Stat(out); err == nil {
		t.Errorf("codex-review.qa.md was written even though codex is missing")
	}
}

// TestPhase2d_ValidJSONWritesFindings: valid JSON produces a correctly
// formatted markdown file with per-finding lines.
func TestPhase2d_ValidJSONWritesFindings(t *testing.T) {
	repo := t.TempDir()
	cfg := &scanRepairConfig{
		Repo: repo,
		codexCaller: func(ctx context.Context, dir string) ([]byte, error) {
			return []byte(`{"verdict":"approved","findings":[
				{"file":"src/a.ts","line":12,"severity":"critical","category":"sec","message":"xss","fix":"escape"},
				{"file":"src/b.ts","line":"3-5","severity":"high","category":"perf","message":"n+1","fix":"join"}
			]}`), nil
		},
	}
	if err := runPhase2dCodexReview(context.Background(), cfg); err != nil {
		t.Fatalf("runPhase2dCodexReview: %v", err)
	}
	out := filepath.Join(repo, "audit", "perspectives", "codex-review.qa.md")
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read %s: %v", out, err)
	}
	s := string(data)
	if !strings.Contains(s, "src/a.ts:12") {
		t.Errorf("missing src/a.ts:12: %s", s)
	}
	if !strings.Contains(s, "src/b.ts:3-5") {
		t.Errorf("missing src/b.ts:3-5 (line range): %s", s)
	}
	if !strings.Contains(s, "[CRITICAL]") || !strings.Contains(s, "[HIGH]") {
		t.Errorf("missing severity tags: %s", s)
	}
}

// ------------------------------------------------------------------
// Phase 3 — aggregate / dedupe / tier-filter / fix-tasks / zero
// ------------------------------------------------------------------

// TestPhase3a_AggregateWalksAllSources: the aggregator concatenates
// findings from every source directory (scans, security, perspectives).
func TestPhase3a_AggregateWalksAllSources(t *testing.T) {
	repo := t.TempDir()
	// Deterministic scans.
	ensureDir(t, filepath.Join(repo, "audit", "scans"))
	_ = os.WriteFile(filepath.Join(repo, "audit", "scans", "section-0000-grep.md"),
		[]byte("- [CRITICAL] src/a.ts:1 — det finding — fix: f\n"), 0o600)
	// Semantic scans.
	ensureDir(t, filepath.Join(repo, "audit", "scans", "section-0000"))
	_ = os.WriteFile(filepath.Join(repo, "audit", "scans", "section-0000", "pat.md"),
		[]byte("- [HIGH] src/a.ts:5 — sem finding — fix: g\n"), 0o600)
	// Security vectors.
	ensureDir(t, filepath.Join(repo, "audit", "security"))
	_ = os.WriteFile(filepath.Join(repo, "audit", "security", "vector-1-auth.md"),
		[]byte("- [CRITICAL] src/b.ts:3 — sec finding — fix: h\n"), 0o600)
	// Personas.
	ensureDir(t, filepath.Join(repo, "audit", "perspectives"))
	_ = os.WriteFile(filepath.Join(repo, "audit", "perspectives", "lead-eng.qa.md"),
		[]byte("- [MEDIUM] src/c.ts:7 — per finding — fix: i\n"), 0o600)

	agg := aggregatePhase3Findings(repo)
	for _, want := range []string{"det finding", "sem finding", "sec finding", "per finding"} {
		if !strings.Contains(agg, want) {
			t.Errorf("aggregate missing %q\n--- buf ---\n%s", want, agg)
		}
	}
	if got := countAggregateLines(agg); got != 4 {
		t.Errorf("aggregate line count = %d, want 4", got)
	}
}

// TestPhase3b_DedupePromptIncludesFindings: the dedupe prompt contains
// the literal aggregated findings block.
func TestPhase3b_DedupePromptIncludesFindings(t *testing.T) {
	agg := "- [CRITICAL] src/a.ts:1 — issue X — fix: do Y\n- [HIGH] src/a.ts:1 — issue X — fix: do Y\n"
	prompt := fmt.Sprintf(dedupPromptTemplate, agg)
	if !strings.Contains(prompt, "issue X") {
		t.Errorf("dedupe prompt missing findings body: %s", prompt)
	}
	if !strings.Contains(prompt, "De-duplicate") {
		t.Errorf("dedupe prompt missing instruction anchor: %s", prompt)
	}
}

// TestPhase3c_TierAllowlistPromotion: a finding mentioning a never-
// TIER-3 category gets promoted to TIER 1 even if the reviewer
// classified it as TIER 3.
func TestPhase3c_TierAllowlistPromotion(t *testing.T) {
	// Reviewer reply that WRONGLY puts a SQL-injection finding in
	// TIER 3 — post-processing must rescue it into TIER 1.
	reviewerReply := `## TIER 1

- [CRITICAL] src/ok.ts:1 — race condition — fix: mutex — effort: small

## TIER 2 (small/medium effort)

- [HIGH] src/perf.ts:5 — 600ms latency — fix: index — effort: medium

## TIER 3 (dropped)

- [MEDIUM] src/db.ts:12 — SQL injection on login query — fix: parameterize — effort: trivial
- [LOW] src/ugly.ts:3 — naming preference — fix: rename — effort: trivial
`
	approved, deferred, dropped := partitionTiers(reviewerReply, "")
	approved, deferred, dropped = promoteAllowlistFindings(approved, deferred, dropped)

	// The SQL-injection finding must be in approved, NOT dropped.
	var sawSQL bool
	for _, f := range approved {
		if strings.Contains(f, "SQL injection") {
			sawSQL = true
			break
		}
	}
	if !sawSQL {
		t.Errorf("SQL-injection finding was not promoted to approved; approved=%v dropped=%v", approved, dropped)
	}
	for _, f := range dropped {
		if strings.Contains(f, "SQL injection") {
			t.Errorf("SQL-injection finding still in dropped bucket: %q", f)
		}
	}
	// Naming preference stays in dropped.
	var sawNaming bool
	for _, f := range dropped {
		if strings.Contains(f, "naming preference") {
			sawNaming = true
		}
	}
	if !sawNaming {
		t.Errorf("naming preference should remain in dropped")
	}
	_ = deferred
}

// TestPhase3c_WritesThreeFiles: runPhase3Full writes approved/
// deferred/dropped files to audit/.
func TestPhase3c_WritesThreeFiles(t *testing.T) {
	repo := t.TempDir()
	// Seed aggregate finding sources so Phase 3a picks up content.
	ensureDir(t, filepath.Join(repo, "audit", "scans"))
	_ = os.WriteFile(filepath.Join(repo, "audit", "scans", "section-0000-grep.md"),
		[]byte("- [CRITICAL] src/a.ts:1 — race condition — fix: lock\n"), 0o600)

	cfg := &scanRepairConfig{
		Repo: repo,
		reviewerCaller: func(ctx context.Context, dir, prompt string) string {
			switch {
			case strings.Contains(prompt, "De-duplicate"):
				return "- [CRITICAL] src/a.ts:1 — race condition — fix: lock — effort: small\n"
			case strings.Contains(prompt, "## TIER 1"):
				return "## TIER 1\n\n- [CRITICAL] src/a.ts:1 — race condition — fix: lock — effort: small\n\n## TIER 2 (small/medium effort)\n\n## TIER 3 (dropped)\n"
			default:
				return "- [ ] FIX-src-1: add lock\n"
			}
		},
	}
	p1 := &phase1Result{NumSections: 1}
	p2 := &phase2Result{}
	ph3, err := runPhase3Full(context.Background(), cfg, p1, p2)
	if err != nil {
		t.Fatalf("runPhase3Full: %v", err)
	}
	for _, name := range []string{"findings-approved.md", "findings-deferred.md", "findings-dropped.md"} {
		if _, err := os.Stat(filepath.Join(repo, "audit", name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	if ph3.Approved == 0 {
		t.Errorf("expected at least one approved finding, got %d", ph3.Approved)
	}
}

// TestPhase3d_PerSectionFixTasks: approved findings grouped by
// section path produce per-section fix-task files.
func TestPhase3d_PerSectionFixTasks(t *testing.T) {
	repo := t.TempDir()
	approved := []string{
		"- [CRITICAL] src/a.ts:1 — race — fix: lock",
		"- [HIGH] api/route.ts:3 — missing auth — fix: check",
	}
	var calls int64
	call := func(ctx context.Context, dir, prompt string) string {
		atomic.AddInt64(&calls, 1)
		return "- [ ] FIX-x-1: body task\n"
	}
	if err := runPhase3dFixTasks(context.Background(), &scanRepairConfig{Repo: repo}, approved, call); err != nil {
		t.Fatalf("runPhase3dFixTasks: %v", err)
	}
	// Two distinct top-level sections (src, api) → 2 fix-tasks files
	// AND 2 reviewer calls.
	if atomic.LoadInt64(&calls) != 2 {
		t.Errorf("expected 2 reviewer calls, got %d", calls)
	}
	matches, _ := filepath.Glob(filepath.Join(repo, "audit", "fix-tasks", "*.md"))
	if len(matches) != 2 {
		t.Errorf("expected 2 fix-tasks files, got %d: %v", len(matches), matches)
	}
}

// TestPhase3e_ZeroFindingsWritesCompleteMarker: when aggregate has
// zero finding lines, the orchestrator writes audit/scan-complete.md
// and skips Phase 4.
func TestPhase3e_ZeroFindingsWritesCompleteMarker(t *testing.T) {
	repo := t.TempDir()
	writeFixtureSemanticPatterns(t, repo)
	sectionFile := writeFixtureSection(t, repo, []string{"src/ok.ts"})

	var phase4Calls int64
	cfg := &scanRepairConfig{
		Repo:                repo,
		Mode:                "sow",
		Workers:             2,
		MaxSections:         2,
		MaxPatterns:         2,
		SemanticFile:        filepath.Join(repo, ".claude", "scripts", "semantic-patterns.md"),
		SkipSecurityVectors: true,
		SkipPersonas:        true,
		SkipCodexReview:     true,
		phase1Runner: func(ctx context.Context, cfg *scanRepairConfig) (*phase1Result, error) {
			_ = os.MkdirAll(filepath.Join(cfg.Repo, "audit"), 0755)
			return &phase1Result{NumSections: 1, Sections: []string{sectionFile}}, nil
		},
		semanticCaller: func(ctx context.Context, dir, prompt string) string { return "None." },
		reviewerCaller: func(ctx context.Context, dir, prompt string) string { return "None." },
		phase4Runner: func(ctx context.Context, cfg *scanRepairConfig, sowPath string) error {
			atomic.AddInt64(&phase4Calls, 1)
			return nil
		},
	}
	if err := runScanRepair(context.Background(), cfg); err != nil {
		t.Fatalf("runScanRepair: %v", err)
	}
	if phase4Calls != 0 {
		t.Errorf("phase4 must not fire on zero approved findings; got %d", phase4Calls)
	}
	if _, err := os.Stat(filepath.Join(repo, "audit", "scan-complete.md")); err != nil {
		t.Errorf("audit/scan-complete.md missing: %v", err)
	}
}

// ------------------------------------------------------------------
// H-18 — headless / interactive modes
// ------------------------------------------------------------------

// TestH18_HeadlessDefaultsCapsToUnlimited: when stdin isn't a TTY
// (test harness default) AND caps are at defaults, runScanRepair
// sets MaxSections and MaxPatterns to 0 (unlimited).
func TestH18_HeadlessDefaultsCapsToUnlimited(t *testing.T) {
	repo := t.TempDir()
	writeFixtureSemanticPatterns(t, repo)
	sectionFile := writeFixtureSection(t, repo, []string{"src/x.ts"})

	cfg := &scanRepairConfig{
		Repo:                repo,
		Mode:                "sow",
		Workers:             1,
		MaxSections:         20, // default value
		MaxPatterns:         5,  // default value
		MaxSectionsExplicit: false,
		MaxPatternsExplicit: false,
		SemanticFile:        filepath.Join(repo, ".claude", "scripts", "semantic-patterns.md"),
		SkipSecurityVectors: true,
		SkipPersonas:        true,
		SkipCodexReview:     true,
		phase1Runner: func(ctx context.Context, cfg *scanRepairConfig) (*phase1Result, error) {
			return &phase1Result{NumSections: 1, Sections: []string{sectionFile}}, nil
		},
		semanticCaller: func(ctx context.Context, dir, prompt string) string { return "None." },
		reviewerCaller: func(ctx context.Context, dir, prompt string) string { return "None." },
		phase4Runner:   func(ctx context.Context, cfg *scanRepairConfig, sowPath string) error { return nil },
	}
	if err := runScanRepair(context.Background(), cfg); err != nil {
		t.Fatalf("runScanRepair: %v", err)
	}
	// Post-run: both caps should have been forced to 0.
	if cfg.MaxSections != 0 {
		t.Errorf("headless should force MaxSections=0; got %d", cfg.MaxSections)
	}
	if cfg.MaxPatterns != 0 {
		t.Errorf("headless should force MaxPatterns=0; got %d", cfg.MaxPatterns)
	}
}

// TestH18_HeadlessRespectsExplicitCaps: when operator sets
// --max-sections=N, headless mode does NOT override.
func TestH18_HeadlessRespectsExplicitCaps(t *testing.T) {
	repo := t.TempDir()
	writeFixtureSemanticPatterns(t, repo)
	sectionFile := writeFixtureSection(t, repo, []string{"src/x.ts"})

	cfg := &scanRepairConfig{
		Repo:                repo,
		Mode:                "sow",
		Workers:             1,
		MaxSections:         3,
		MaxPatterns:         2,
		MaxSectionsExplicit: true,
		MaxPatternsExplicit: true,
		SemanticFile:        filepath.Join(repo, ".claude", "scripts", "semantic-patterns.md"),
		SkipSecurityVectors: true,
		SkipPersonas:        true,
		SkipCodexReview:     true,
		phase1Runner: func(ctx context.Context, cfg *scanRepairConfig) (*phase1Result, error) {
			return &phase1Result{NumSections: 1, Sections: []string{sectionFile}}, nil
		},
		semanticCaller: func(ctx context.Context, dir, prompt string) string { return "None." },
		reviewerCaller: func(ctx context.Context, dir, prompt string) string { return "None." },
		phase4Runner:   func(ctx context.Context, cfg *scanRepairConfig, sowPath string) error { return nil },
	}
	if err := runScanRepair(context.Background(), cfg); err != nil {
		t.Fatalf("runScanRepair: %v", err)
	}
	if cfg.MaxSections != 3 {
		t.Errorf("explicit MaxSections=3 clobbered: got %d", cfg.MaxSections)
	}
	if cfg.MaxPatterns != 2 {
		t.Errorf("explicit MaxPatterns=2 clobbered: got %d", cfg.MaxPatterns)
	}
}

// TestH18_InteractiveReadsStdinChoice: in interactive mode, feeding
// "A\n" via cfg.interactiveIn accepts the default proceed path. A
// non-A response routes to the correct branch.
func TestH18_InteractiveReadsStdinChoice(t *testing.T) {
	cases := []struct {
		name  string
		input string
		def   byte
		want  byte
	}{
		{"answer A", "A\n", 'A', 'A'},
		{"answer B", "B\n", 'A', 'B'},
		{"lowercase c", "c\n", 'A', 'C'},
		{"whitespace then D", "   D\n", 'A', 'D'},
		{"blank line uses default", "\n", 'A', 'A'},
		{"junk uses first letter", "zzz\n", 'A', 'Z'},
	}
	for _, tc := range cases {
		cfg := &scanRepairConfig{interactiveIn: strings.NewReader(tc.input)}
		got := readInteractiveChoice(cfg, tc.def)
		if got != tc.want {
			t.Errorf("%s: got %c, want %c", tc.name, got, tc.want)
		}
	}
}

// TestH18_HeadlessSkipsInteractivePrompts: in headless mode, the
// phase-2b/phase-2c/phase-3c promptXxx helpers must NOT read stdin
// and must return the proceed default.
func TestH18_HeadlessSkipsInteractivePrompts(t *testing.T) {
	// Headless-forcing cfg: InteractiveExplicit=true + Interactive=false.
	cfg := &scanRepairConfig{
		InteractiveExplicit: true,
		Interactive:         false,
		// If the headless check falls through we'd try to read stdin.
		// Wire an empty reader to catch any accidental read.
		interactiveIn: strings.NewReader(""),
	}
	if !promptPhase2bScope(cfg) {
		t.Errorf("headless 2b should return true (proceed) without prompting")
	}
	if !promptPhase2cScope(cfg) {
		t.Errorf("headless 2c should return true (proceed) without prompting")
	}
	if !promptPhase3cChoice(cfg, &phase3Result{Approved: 1}) {
		t.Errorf("headless 3c should return true (build now) without prompting")
	}
}

// TestH18_EndToEndHeadlessFullPipeline: a headless full-depth run with
// mocked LLMs produces a FIX_SOW.md containing approved findings.
func TestH18_EndToEndHeadlessFullPipeline(t *testing.T) {
	repo := t.TempDir()
	writeFixtureSemanticPatterns(t, repo)
	writePersonasFixture(t, repo)
	sectionFile := writeFixtureSection(t, repo, []string{"src/app.ts"})

	var phase4Calls int64
	cfg := &scanRepairConfig{
		Repo:                repo,
		Mode:                "sow",
		Workers:             2,
		MaxSections:         20, // default → forced to 0 by headless
		MaxPatterns:         5,  // default → forced to 0 by headless
		InteractiveExplicit: true,
		Interactive:         false,
		SemanticFile:        filepath.Join(repo, ".claude", "scripts", "semantic-patterns.md"),
		PersonasSelection:   "core",
		SkipCodexReview:     true, // no external codex in test env
		phase1Runner: func(ctx context.Context, cfg *scanRepairConfig) (*phase1Result, error) {
			_ = os.MkdirAll(filepath.Join(cfg.Repo, "audit"), 0755)
			_ = os.WriteFile(filepath.Join(cfg.Repo, "audit", "deterministic-report.md"),
				[]byte("# Deterministic\n"), 0o600)
			return &phase1Result{NumSections: 1, DeterministicFindings: 1, Sections: []string{sectionFile}}, nil
		},
		semanticCaller: func(ctx context.Context, dir, prompt string) string {
			return "- [HIGH] src/app.ts:10 — missing auth check — fix: add guard"
		},
		vectorCaller: func(ctx context.Context, dir, prompt string, preferOpus bool) string {
			return "- [CRITICAL] src/app.ts:22 — auth bypass — fix: verify session"
		},
		personaCaller: func(ctx context.Context, dir, prompt string, preferOpus bool) string {
			return "- [ ] [HIGH] src/app.ts:55 — unhandled panic — fix: add recover — effort: small"
		},
		reviewerCaller: func(ctx context.Context, dir, prompt string) string {
			switch {
			case strings.Contains(prompt, "De-duplicate"):
				return "- [CRITICAL] src/app.ts:22 — auth bypass — fix: verify session\n- [HIGH] src/app.ts:10 — missing auth check — fix: add guard\n- [HIGH] src/app.ts:55 — unhandled panic — fix: add recover"
			case strings.Contains(prompt, "## TIER 1"):
				return "## TIER 1\n\n- [CRITICAL] src/app.ts:22 — auth bypass — fix: verify session\n\n## TIER 2 (small/medium effort)\n\n- [HIGH] src/app.ts:10 — missing auth check — fix: add guard — effort: small\n- [HIGH] src/app.ts:55 — unhandled panic — fix: add recover — effort: small\n\n## TIER 3 (dropped)\n"
			default:
				return "- [ ] FIX-src-1: wire auth guard + session verify"
			}
		},
		phase4Runner: func(ctx context.Context, cfg *scanRepairConfig, sowPath string) error {
			atomic.AddInt64(&phase4Calls, 1)
			// Assert the SOW content exists at this point.
			data, err := os.ReadFile(sowPath)
			if err != nil {
				return err
			}
			if !strings.Contains(string(data), "app.ts") && !strings.Contains(string(data), "FIX-src-1") {
				t.Errorf("SOW missing fix-task content at phase4-time: %s", data)
			}
			return nil
		},
	}
	if err := runScanRepair(context.Background(), cfg); err != nil {
		t.Fatalf("runScanRepair: %v", err)
	}
	if phase4Calls != 1 {
		t.Errorf("phase4 should be called exactly once, got %d", phase4Calls)
	}
	data, err := os.ReadFile(filepath.Join(repo, "FIX_SOW.md"))
	if err != nil {
		t.Fatalf("FIX_SOW.md missing: %v", err)
	}
	if !strings.Contains(string(data), "Implementation Checklist") {
		t.Errorf("FIX_SOW.md missing 'Implementation Checklist': %s", data)
	}
}

// ------------------------------------------------------------------
// Helpers
// ------------------------------------------------------------------

func ensureDir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0755); err != nil {
		t.Fatal(err)
	}
}

// writePersonasFixture drops a minimal audit-personas.md containing
// all 17 slugs so parseAuditPersonas returns the full list without
// relying on the test repo's actual .claude/scripts tree.
func writePersonasFixture(t *testing.T, repo string) {
	t.Helper()
	dir := filepath.Join(repo, ".claude", "scripts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	allSlugs := []string{
		"lead-eng", "lead-qa", "lead-security", "lead-ux", "lead-compliance",
		"product-owner", "vp-eng-completeness", "vp-eng-idempotency",
		"vp-eng-scaling", "vp-eng-types", "vp-eng-tests", "vp-eng-docs",
		"vp-eng-comments", "sneaky-finder", "scaling-consultant",
		"build-deploy", "picky-reviewer",
	}
	var buf strings.Builder
	buf.WriteString("# Audit Personas\n\n")
	for i, s := range allSlugs {
		fmt.Fprintf(&buf, "### %d. %s\nYou are the %s. Audit for your domain.\n\n", i+1, s, s)
	}
	if err := os.WriteFile(filepath.Join(dir, "audit-personas.md"), []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}
