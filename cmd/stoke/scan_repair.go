package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"
)

// scan-repair: H-15
//
// Native-runner implementation of the 4-phase `scan-and-repair` workflow
// that was previously defined as an interactive Claude-Code slash
// command (.claude/commands/scan-and-repair.md). The slash-command
// version only runs inside Claude Code's subagent loop, which locks
// operators to a single model family and makes parallelism and
// budget-enforcement both ad-hoc. This subcommand bakes the workflow
// into the native runner so it works with ANY worker model via the
// existing ModelRole abstraction (see cmd/stoke/fallback.go).
//
// Four phases:
//
//  1. Deterministic scan (shell-only, no LLM): project-mapper +
//     security scripts + deterministic-scan.sh per section. Produces
//     audit/project-map.md, audit/sections/section-*.txt,
//     audit/security/*.csv, audit/scans/section-*-grep.md, and
//     audit/deterministic-report.md.
//
//  2. Semantic scan (LLM via --worker-model): for each (section,
//     pattern) pair up to --max-sections × --max-patterns, build a
//     single-pattern prompt and call the worker in parallel (default
//     4 concurrent). Findings collected to audit/scans/<section>/
//     <pattern>.md and aggregated into audit/semantic-report.md.
//
//  3. Fix-SOW generation (LLM via --reviewer): combined
//     deterministic + semantic reports are fed to the reviewer,
//     which produces a discrete-task repair SOW at <repo>/FIX_SOW.md.
//
//  4. Execute FIX_SOW.md via the existing runners (--mode sow invokes
//     `stoke sow`; --mode simple-loop invokes `stoke simple-loop`).
//     All existing hardening (H-4 pipe watchdog, H-6 regression guard,
//     H-7 codex backoff, H-10 CC rate-limit, H-11 fallback) applies
//     automatically because we reuse the production runners.

// scanRepairConfig collects every parsed flag for a single run.
// Kept as a struct so orchestration helpers (runPhase1/2/3/4) can be
// tested without re-parsing the command line.
type scanRepairConfig struct {
	Repo         string // absolute path to target repo
	WorkerModel  string // --worker-model for semantic scan calls
	Reviewer     string // --reviewer (codex|cc-opus|cc-sonnet|claude)
	Mode         string // sow | simple-loop
	MaxSections  int    // cap on sections scanned in Phase 2
	MaxPatterns  int    // cap on patterns per section in Phase 2
	Workers      int    // Phase 2 concurrency
	Fresh        bool   // clear prior audit/* artifacts before running
	ClaudeBin    string // claude CLI path (resolved by flag)
	CodexBin     string // codex CLI path (resolved by flag)
	StokeBin     string // absolute path to the stoke binary (auto-detected)
	SemanticFile string // path to semantic-patterns.md (resolved from .claude/scripts)

	// H-17/H-18 additions.
	Interactive         bool   // --interactive; auto-detected from isatty(stdin)
	InteractiveExplicit bool   // true when operator passed --interactive=... explicitly
	MaxSectionsExplicit bool   // true when operator passed --max-sections=... explicitly
	MaxPatternsExplicit bool   // true when operator passed --max-patterns=... explicitly
	SkipSecurityVectors bool   // --skip-security-vectors
	SkipPersonas        bool   // --skip-personas
	SkipCodexReview     bool   // --skip-codex-review
	PersonasSelection   string // --personas: "all" | "core" | comma list
	OpusBin             string // --opus-bin: optional alternate binary for opus-preferred calls

	// Hooks used in tests to short-circuit real subprocesses.
	// In production these are left nil and the real shellers are used.
	semanticCaller func(ctx context.Context, dir, prompt string) string                          // test override for the semantic-scan worker
	reviewerCaller func(ctx context.Context, dir, prompt string) string                          // test override for the reviewer
	phase1Runner   func(ctx context.Context, cfg *scanRepairConfig) (*phase1Result, error)       // test override for Phase 1 shell-out
	phase4Runner   func(ctx context.Context, cfg *scanRepairConfig, sowPath string) error        // test override for Phase 4 runner dispatch
	vectorCaller   func(ctx context.Context, dir, prompt string, preferOpus bool) string         // test override for Phase 2b vector worker
	personaCaller  func(ctx context.Context, dir, prompt string, preferOpus bool) string         // test override for Phase 2c persona worker
	codexCaller    func(ctx context.Context, dir string) ([]byte, error)                         // test override for Phase 2d codex shell-out
	fixTaskCaller  func(ctx context.Context, dir, prompt string) string                          // test override for Phase 3d fix-task generation

	// Stdin source for interactive prompts. Tests inject a strings.Reader;
	// production defers to os.Stdin. The field is typed as io.Reader via
	// any concrete reader so we can keep the struct dependency-light.
	interactiveIn interface {
		Read(p []byte) (int, error)
	}
}

// isHeadless returns true when the process appears to be running
// non-interactively. The check is:
//
//  1. If the operator explicitly passed --interactive=..., honor that
//     (cfg.Interactive reflects the explicit value).
//  2. Otherwise, detect TTY on stdin via golang.org/x/term.
//
// Headless mode triggers the "full depth, no caps" behavior from H-18.
func isHeadless(cfg *scanRepairConfig) bool {
	if cfg.InteractiveExplicit {
		return !cfg.Interactive
	}
	// Default auto-detect: no TTY on stdin → headless.
	return !term.IsTerminal(int(os.Stdin.Fd()))
}

// scanRepairCmd is the cmd/stoke/main.go dispatcher entry point. It
// parses flags, resolves the repo + binaries, and then runs phases
// 1→4 sequentially. Any fatal error prints a leading "scan-repair:"
// banner and exits 1. Phase 2 timeouts DON'T terminate the run —
// they produce a partial semantic report (see runPhase2).
func scanRepairCmd(args []string) {
	fs := flag.NewFlagSet("scan-repair", flag.ExitOnError)
	repo := fs.String("repo", "", "Target repository path (required)")
	workerModel := fs.String("worker-model", "claude-sonnet-4-6", "Model for Phase 2 semantic scan calls")
	reviewer := fs.String("reviewer", "codex", "Reviewer backend for Phase 3: codex | cc-opus | cc-sonnet | claude")
	mode := fs.String("mode", "sow", "Phase 4 execution mode: sow | simple-loop")
	maxSections := fs.Int("max-sections", 20, "Cap on sections scanned in Phase 2 (unlimited in headless mode unless set)")
	maxPatterns := fs.Int("max-patterns", 5, "Cap on patterns per section in Phase 2 (unlimited in headless mode unless set)")
	workers := fs.Int("workers", 4, "Phase 2 concurrency (default 4 concurrent semantic calls)")
	fresh := fs.Bool("fresh", false, "Clear any prior audit/ artifacts before running")
	claudeBin := fs.String("claude-bin", "claude", "Claude Code binary")
	codexBin := fs.String("codex-bin", "codex", "Codex binary")
	// H-17/H-18 flags.
	interactive := fs.Bool("interactive", false, "Interactive mode (prompts between phases). Default: auto-detect isatty(stdin).")
	skipSecVec := fs.Bool("skip-security-vectors", false, "Skip Phase 2b security-vector scan")
	skipPersonas := fs.Bool("skip-personas", false, "Skip Phase 2c 17-persona audit")
	skipCodex := fs.Bool("skip-codex-review", false, "Skip Phase 2d codex deep review")
	personas := fs.String("personas", "all", "Persona selection: all | core | comma,list,of,slugs")
	opusBin := fs.String("opus-bin", "", "Path to opus-capable binary for security-critical audits; empty = use worker-model")
	fs.Parse(args)

	// Detect whether the operator passed certain flags explicitly. We
	// can't use flag.Visit to mutate the *int afterward, so we scan
	// fs.Visit and flip the *Explicit booleans.
	interactiveSet := false
	maxSectionsSet := false
	maxPatternsSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "interactive":
			interactiveSet = true
		case "max-sections":
			maxSectionsSet = true
		case "max-patterns":
			maxPatternsSet = true
		}
	})

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "usage: stoke scan-repair --repo <path> [flags]")
		os.Exit(2)
	}
	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan-repair: resolve repo: %v\n", err)
		os.Exit(1)
	}
	if fi, err := os.Stat(absRepo); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "scan-repair: repo not found: %s\n", absRepo)
		os.Exit(1)
	}
	if *mode != "sow" && *mode != "simple-loop" {
		fmt.Fprintf(os.Stderr, "scan-repair: --mode must be 'sow' or 'simple-loop' (got %q)\n", *mode)
		os.Exit(2)
	}
	if *maxSections < 0 || *maxPatterns < 0 || *workers < 1 {
		fmt.Fprintln(os.Stderr, "scan-repair: --max-sections, --max-patterns must be >=0 and --workers >=1")
		os.Exit(2)
	}

	cfg := &scanRepairConfig{
		Repo:                absRepo,
		WorkerModel:         *workerModel,
		Reviewer:            *reviewer,
		Mode:                *mode,
		MaxSections:         *maxSections,
		MaxPatterns:         *maxPatterns,
		Workers:             *workers,
		Fresh:               *fresh,
		ClaudeBin:           *claudeBin,
		CodexBin:            *codexBin,
		Interactive:         *interactive,
		InteractiveExplicit: interactiveSet,
		MaxSectionsExplicit: maxSectionsSet,
		MaxPatternsExplicit: maxPatternsSet,
		SkipSecurityVectors: *skipSecVec,
		SkipPersonas:        *skipPersonas,
		SkipCodexReview:     *skipCodex,
		PersonasSelection:   *personas,
		OpusBin:             *opusBin,
	}
	// Locate the stoke binary so Phase 4 can re-invoke it without
	// relying on $PATH resolution. Errors on either os.Executable or
	// filepath.Abs are non-fatal here — runPhase4 re-checks
	// cfg.StokeBin=="" and returns a clear error if it remains unset.
	if exe, err := os.Executable(); err == nil {
		if abs, aerr := filepath.Abs(exe); aerr == nil {
			cfg.StokeBin = abs
		} else {
			fmt.Fprintf(os.Stderr, "scan-repair: resolve stoke binary abs path: %v\n", aerr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "scan-repair: locate stoke binary: %v\n", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runScanRepair(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "scan-repair: %v\n", err)
		os.Exit(1)
	}
}

// runScanRepair drives the four phases in order. Splitting this out
// of scanRepairCmd lets tests exercise the full pipeline with
// mocked phase runners (cfg.phase1Runner / cfg.semanticCaller / etc.)
// without touching flag parsing or os.Exit.
func runScanRepair(ctx context.Context, cfg *scanRepairConfig) error {
	// H-18: apply headless-mode cap overrides BEFORE printing so the
	// banner shows the effective values. Headless ∧ caps-at-default →
	// unlimited (0). If the operator explicitly set --max-sections or
	// --max-patterns, we respect their value even in headless mode.
	headless := isHeadless(cfg)
	if headless {
		if !cfg.MaxSectionsExplicit && cfg.MaxSections == 20 {
			cfg.MaxSections = 0
		}
		if !cfg.MaxPatternsExplicit && cfg.MaxPatterns == 5 {
			cfg.MaxPatterns = 0
		}
	}

	fmt.Printf("stoke scan-repair\n")
	fmt.Printf("  repo:         %s\n", cfg.Repo)
	fmt.Printf("  worker:       %s\n", cfg.WorkerModel)
	fmt.Printf("  reviewer:     %s\n", cfg.Reviewer)
	fmt.Printf("  mode:         %s\n", cfg.Mode)
	fmt.Printf("  max sections: %s\n", capDisplay(cfg.MaxSections))
	fmt.Printf("  max patterns: %s\n", capDisplay(cfg.MaxPatterns))
	fmt.Printf("  workers:      %d\n", cfg.Workers)
	fmt.Printf("  fresh:        %v\n", cfg.Fresh)
	fmt.Printf("  headless:     %v\n", headless)
	fmt.Printf("  personas:     %s\n", cfg.PersonasSelection)
	fmt.Printf("  skip vec/per/codex: %v/%v/%v\n\n", cfg.SkipSecurityVectors, cfg.SkipPersonas, cfg.SkipCodexReview)

	// --fresh: wipe prior audit/* + FIX_SOW.md before Phase 1 so we
	// aren't confused by stale findings from a previous run.
	if cfg.Fresh {
		if err := cleanAuditArtifacts(cfg.Repo); err != nil {
			return fmt.Errorf("--fresh cleanup: %w", err)
		}
	}

	// Ensure .claude/scripts/ infrastructure is present. Bootstrapped
	// from the local setup.sh when missing. Fail-closed: if both the
	// scripts AND setup.sh are missing (or setup fails) we abort.
	if err := ensureClaudeScripts(cfg.Repo); err != nil {
		return fmt.Errorf("claude scripts bootstrap: %w", err)
	}
	cfg.SemanticFile = filepath.Join(cfg.Repo, ".claude", "scripts", "semantic-patterns.md")

	// === Phase 1 ===
	fmt.Println("─── Phase 1: Deterministic scan ───")
	ph1Runner := cfg.phase1Runner
	if ph1Runner == nil {
		ph1Runner = runPhase1
	}
	p1, err := ph1Runner(ctx, cfg)
	if err != nil {
		return fmt.Errorf("phase 1: %w", err)
	}
	fmt.Printf("  Phase 1 complete: %d sections, %d deterministic findings, %d security findings\n\n",
		p1.NumSections, p1.DeterministicFindings, p1.SecurityFindings)

	// H-18 interactive prompt: after Phase 1, ask the operator what
	// scope they want. In headless mode this is a no-op that returns
	// "A" (full scope) silently.
	promptPhase1Scope(cfg, p1)

	// === Phase 2a: semantic scan ===
	fmt.Println("─── Phase 2: Semantic scan ───")
	p2, err := runPhase2(ctx, cfg, p1)
	if err != nil {
		// Phase 2 returns nil-error even on timeout — a real error
		// here means we couldn't produce even a partial report.
		return fmt.Errorf("phase 2: %w", err)
	}
	fmt.Printf("  Phase 2 complete: %d calls, %d findings, %d timeouts\n\n",
		p2.CallsMade, p2.FindingsCount, p2.Timeouts)

	// === Phase 2b: security vector scan ===
	if !cfg.SkipSecurityVectors && promptPhase2bScope(cfg) {
		fmt.Println("─── Phase 2b: Security vector scan ───")
		if err := runPhase2bSecurityVectors(ctx, cfg, p1); err != nil {
			fmt.Fprintf(os.Stderr, "  [Phase 2b] non-fatal: %v\n", err)
		}
	} else {
		fmt.Println("  Phase 2b skipped (--skip-security-vectors or operator choice)")
	}

	// === Phase 2c: 17-persona audit ===
	if !cfg.SkipPersonas && promptPhase2cScope(cfg) {
		fmt.Println("─── Phase 2c: 17-persona audit ───")
		if err := runPhase2cPersonas(ctx, cfg, p1); err != nil {
			fmt.Fprintf(os.Stderr, "  [Phase 2c] non-fatal: %v\n", err)
		}
	} else {
		fmt.Println("  Phase 2c skipped (--skip-personas or operator choice)")
	}

	// === Phase 2d: codex deep review ===
	if !cfg.SkipCodexReview {
		fmt.Println("─── Phase 2d: Codex deep review ───")
		if err := runPhase2dCodexReview(ctx, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "  [Phase 2d] non-fatal: %v\n", err)
		}
	} else {
		fmt.Println("  Phase 2d skipped (--skip-codex-review)")
	}

	// === Phase 3: aggregate → dedupe → TIER filter → fix-tasks ===
	fmt.Println("─── Phase 3: Aggregate, dedupe, tier-filter, fix-tasks ───")
	ph3, err := runPhase3Full(ctx, cfg, p1, p2)
	if err != nil {
		return fmt.Errorf("phase 3: %w", err)
	}

	// === Phase 3e: zero-findings clean exit ===
	if ph3.Approved == 0 {
		msg := fmt.Sprintf(
			"# Scan Complete\n\nNo high-impact issues found across %d sections.\n\n"+
				"[DROPPED] %d low-impact findings filtered to audit/findings-dropped.md.\n",
			p1.NumSections, ph3.Dropped)
		completePath := filepath.Join(cfg.Repo, "audit", "scan-complete.md")
		if err := os.WriteFile(completePath, []byte(msg), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "  [scan-repair] write %s: %v\n", completePath, err)
		}
		// Also write a trivial FIX_SOW.md so downstream tools that
		// blindly look for it don't blow up.
		_ = os.WriteFile(filepath.Join(cfg.Repo, "FIX_SOW.md"),
			[]byte("# Fix SOW\n\nAudit found no high-impact issues. No tasks generated.\n"), 0644)
		fmt.Printf("🎯 Phase 3c TIER: APPROVED=0 DEFERRED=%d DROPPED=%d — skipping Phase 4\n",
			ph3.Deferred, ph3.Dropped)
		return nil
	}

	// Interactive choice between Phases 3 and 4.
	if !promptPhase3cChoice(cfg, ph3) {
		fmt.Println("  operator chose to skip Phase 4.")
		return nil
	}

	// === Phase 4 ===
	fmt.Printf("─── Phase 4: Execute FIX_SOW (mode=%s) ───\n", cfg.Mode)
	ph4Runner := cfg.phase4Runner
	if ph4Runner == nil {
		ph4Runner = runPhase4
	}
	if err := ph4Runner(ctx, cfg, ph3.SOWPath); err != nil {
		return fmt.Errorf("phase 4: %w", err)
	}
	fmt.Println("scan-repair: done.")
	return nil
}

// capDisplay renders a Phase 2 cap. 0 → "unlimited" for readability.
func capDisplay(n int) string {
	if n <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", n)
}

// ------------------------------------------------------------------
// Phase 1: deterministic scan. All shell-outs, no LLM.
// ------------------------------------------------------------------

// phase1Result aggregates the counters from the deterministic scan.
// The heavy output (section-*.txt, *-grep.md, *.csv) is written to
// disk; this struct holds the summary counts used by downstream
// phases and by the "audit found no issues" early-exit check.
type phase1Result struct {
	NumSections           int
	DeterministicFindings int // aggregate lines in all section-*-grep.md
	SecurityFindings      int // non-header rows across the three CSVs
	Sections              []string // absolute paths to audit/sections/section-*.txt
}

// runPhase1 runs the deterministic pipeline:
//  1. project-mapper.sh (produces audit/project-map.md + section files)
//  2. deterministic-scan.sh per section (produces audit/scans/*-grep.md)
//  3. scan_inputs.py / scan_dataflow.py / scan_config.py (produce CSVs)
//  4. Aggregate everything into audit/deterministic-report.md.
//
// Each shell-out has a 5-minute timeout (the project-mapper can be
// slow on huge repos). A single script failing is non-fatal for the
// aggregate report — we log + continue so a broken Python dependency
// doesn't kill the entire audit.
func runPhase1(ctx context.Context, cfg *scanRepairConfig) (*phase1Result, error) {
	res := &phase1Result{}
	auditDir := filepath.Join(cfg.Repo, "audit")
	scansDir := filepath.Join(auditDir, "scans")
	securityDir := filepath.Join(auditDir, "security")
	if err := os.MkdirAll(scansDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir scans: %w", err)
	}
	if err := os.MkdirAll(securityDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir security: %w", err)
	}

	scriptsDir := filepath.Join(cfg.Repo, ".claude", "scripts")

	// 1. project-mapper.sh — produces sections and project-map.md.
	mapScript := filepath.Join(scriptsDir, "project-mapper.sh")
	if out, err := runShell(ctx, cfg.Repo, "bash "+shellQuote(mapScript), 5*time.Minute); err != nil {
		// Without sections, Phase 2 has nothing to do; this IS fatal.
		return nil, fmt.Errorf("project-mapper.sh failed: %w (output: %s)", err, out)
	}

	// Enumerate sections produced by the mapper. filepath.Glob only
	// errors on malformed patterns; our pattern is a literal constant
	// so any error here is a programming bug — surface it loudly.
	sectionFiles, err := filepath.Glob(filepath.Join(auditDir, "sections", "section-*.txt"))
	if err != nil {
		return nil, fmt.Errorf("glob sections: %w", err)
	}
	sort.Strings(sectionFiles)
	res.Sections = sectionFiles
	res.NumSections = len(sectionFiles)

	// 2. deterministic-scan.sh per section. We run these sequentially
	// because each call is fast (grep-only) and doing them in
	// parallel would add complexity for no real speedup.
	detScript := filepath.Join(scriptsDir, "deterministic-scan.sh")
	for _, sf := range sectionFiles {
		base := strings.TrimSuffix(filepath.Base(sf), ".txt")
		outPath := filepath.Join(scansDir, base+"-grep.md")
		cmdStr := fmt.Sprintf("bash %s %s > %s", shellQuote(detScript), shellQuote(sf), shellQuote(outPath))
		if out, err := runShell(ctx, cfg.Repo, cmdStr, 5*time.Minute); err != nil {
			fmt.Fprintf(os.Stderr, "  [Phase 1] %s failed: %v (output: %s)\n", base, err, out)
		}
		res.DeterministicFindings += countFindings(outPath)
	}

	// 3. Security CSV scans.
	secScripts := []struct {
		Script string
		Out    string
	}{
		{"security/scan_inputs.py", "inputs-report.csv"},
		{"security/scan_dataflow.py", "dataflow-report.csv"},
		{"security/scan_config.py", "secrets-report.csv"},
	}
	for _, s := range secScripts {
		scr := filepath.Join(scriptsDir, s.Script)
		if _, err := os.Stat(scr); err != nil {
			// Python script missing — warn and move on. The CSVs will
			// simply be absent from the aggregate report.
			fmt.Fprintf(os.Stderr, "  [Phase 1] security script missing: %s\n", scr)
			continue
		}
		outPath := filepath.Join(securityDir, s.Out)
		cmdStr := fmt.Sprintf("python3 %s . --output %s", shellQuote(scr), shellQuote(outPath))
		if out, err := runShell(ctx, cfg.Repo, cmdStr, 5*time.Minute); err != nil {
			fmt.Fprintf(os.Stderr, "  [Phase 1] %s failed: %v (output: %s)\n", s.Script, err, out)
		}
		res.SecurityFindings += countCSVRows(outPath)
	}

	// 4. Aggregate into deterministic-report.md.
	if err := writeDeterministicReport(auditDir, res); err != nil {
		fmt.Fprintf(os.Stderr, "  [Phase 1] aggregate report failed: %v\n", err)
	}
	return res, nil
}

// countFindings returns the number of lines that look like finding
// entries in a grep-style report (lines starting with "- [" which
// is the fixed format emitted by deterministic-scan.sh).
func countFindings(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "- [") {
			n++
		}
	}
	return n
}

// countCSVRows returns (lines - 1) for a CSV file (assumed to have
// a header row). Empty / missing files → 0. Used as a fast proxy
// for "number of security findings" without loading the CSV into
// memory.
func countCSVRows(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) <= 1 {
		return 0
	}
	return len(lines) - 1
}

// writeDeterministicReport compiles the individual section grep
// reports + CSVs into a single markdown aggregate at
// audit/deterministic-report.md. Kept small and deterministic —
// downstream phases parse this file so format changes are loadbearing.
func writeDeterministicReport(auditDir string, res *phase1Result) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Deterministic Audit Report\n\n")
	fmt.Fprintf(&buf, "Generated: %s\n\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&buf, "## Summary\n\n")
	fmt.Fprintf(&buf, "- Sections: %d\n", res.NumSections)
	fmt.Fprintf(&buf, "- Deterministic findings: %d\n", res.DeterministicFindings)
	fmt.Fprintf(&buf, "- Security findings: %d\n\n", res.SecurityFindings)

	fmt.Fprintf(&buf, "## Deterministic Scans\n\n")
	scanFiles, globErr := filepath.Glob(filepath.Join(auditDir, "scans", "section-*-grep.md"))
	if globErr != nil {
		// Programming bug — literal pattern can't actually fail, but
		// surface loudly if it ever does instead of silently ignoring.
		fmt.Fprintf(&buf, "_glob error: %v_\n\n", globErr)
	}
	sort.Strings(scanFiles)
	for _, sf := range scanFiles {
		b, err := os.ReadFile(sf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [Phase 1] read %s: %v\n", sf, err)
			continue
		}
		fmt.Fprintf(&buf, "### %s\n\n", filepath.Base(sf))
		buf.Write(b)
		buf.WriteString("\n\n")
	}

	fmt.Fprintf(&buf, "## Security Findings\n\n")
	csvs := []string{"inputs-report.csv", "dataflow-report.csv", "secrets-report.csv"}
	for _, c := range csvs {
		p := filepath.Join(auditDir, "security", c)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		fmt.Fprintf(&buf, "### %s\n\n```csv\n", c)
		buf.Write(b)
		buf.WriteString("```\n\n")
	}

	return os.WriteFile(filepath.Join(auditDir, "deterministic-report.md"), buf.Bytes(), 0644)
}

// ------------------------------------------------------------------
// Interactive prompt helpers (H-18).
// ------------------------------------------------------------------

// readInteractiveChoice reads one line from cfg.interactiveIn (falling
// back to os.Stdin). Returns the first non-whitespace letter, uppercased.
// On EOF/error or blank line, returns the default letter.
func readInteractiveChoice(cfg *scanRepairConfig, def byte) byte {
	var r io.Reader = cfg.interactiveIn
	if r == nil {
		r = os.Stdin
	}
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return def
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			c = c - 'a' + 'A'
		}
		return c
	}
	return def
}

// promptPhase1Scope is invoked between Phase 1 and Phase 2a. In
// headless mode it is a no-op. In interactive mode it asks the
// operator which semantic-scan scope they want. Any choice other
// than B/C/D/E is treated as "A — proceed with full scope".
func promptPhase1Scope(cfg *scanRepairConfig, p1 *phase1Result) {
	if isHeadless(cfg) {
		return
	}
	fmt.Printf(interactivePhase1PromptTemplate, p1.DeterministicFindings, p1.SecurityFindings)
	c := readInteractiveChoice(cfg, 'A')
	switch c {
	case 'B':
		// Quality only: keep semantic, skip security + personas.
		cfg.SkipSecurityVectors = true
		cfg.SkipPersonas = true
	case 'C':
		// Security only: skip semantic → cap to zero, skip personas.
		cfg.MaxSections = 0
		cfg.MaxPatterns = 0
		cfg.MaxSectionsExplicit = true
		cfg.MaxPatternsExplicit = true
		cfg.SkipPersonas = true
	case 'D':
		// Flagged only: skip semantic, run security + personas.
		cfg.MaxSections = 0
		cfg.MaxPatterns = 0
		cfg.MaxSectionsExplicit = true
		cfg.MaxPatternsExplicit = true
	case 'E':
		// Skip semantic entirely → set caps to 0 so Phase 2a
		// dispatches nothing.
		cfg.MaxSections = 0
		cfg.MaxPatterns = 0
		cfg.MaxSectionsExplicit = true
		cfg.MaxPatternsExplicit = true
		cfg.SkipSecurityVectors = true
		cfg.SkipPersonas = true
	default:
		// A (default): run full scope.
	}
}

// promptPhase2bScope returns true when Phase 2b should run. In
// headless mode returns !SkipSecurityVectors. In interactive mode
// asks A (run all) or B (skip).
func promptPhase2bScope(cfg *scanRepairConfig) bool {
	if isHeadless(cfg) {
		return true
	}
	// Approximate vector count for the prompt; the real count comes
	// from vectors.md. 10 is the current fixture.
	fmt.Printf(interactivePhase2bPromptTemplate, 10)
	c := readInteractiveChoice(cfg, 'A')
	return c != 'B'
}

// promptPhase2cScope returns true when Phase 2c should run and
// (possibly) mutates cfg.PersonasSelection. Headless: pass-through.
// Interactive: A=all, B=core, C=pick (comma list on stdin), D=skip.
func promptPhase2cScope(cfg *scanRepairConfig) bool {
	if isHeadless(cfg) {
		return true
	}
	fmt.Print(interactivePhase2cPromptTemplate)
	c := readInteractiveChoice(cfg, 'A')
	switch c {
	case 'B':
		cfg.PersonasSelection = "core"
		return true
	case 'C':
		fmt.Print("  Comma-separated persona slugs: ")
		var r io.Reader = cfg.interactiveIn
		if r == nil {
			r = os.Stdin
		}
		br := bufio.NewReader(r)
		line, _ := br.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			cfg.PersonasSelection = "all"
		} else {
			cfg.PersonasSelection = line
		}
		return true
	case 'D':
		return false
	default:
		cfg.PersonasSelection = "all"
		return true
	}
}

// promptPhase3cChoice returns true when Phase 4 should proceed. In
// headless mode always returns true. In interactive mode asks A
// (build now), B (open FIX_SOW.md), C/D (skip build).
func promptPhase3cChoice(cfg *scanRepairConfig, ph3 *phase3Result) bool {
	// Always log the tier summary for operators.
	fmt.Printf("🎯 Phase 3c TIER: APPROVED=%d DEFERRED=%d DROPPED=%d\n",
		ph3.Approved, ph3.Deferred, ph3.Dropped)
	if isHeadless(cfg) {
		return true
	}
	fmt.Printf(interactivePhase3cPromptTemplate, ph3.Approved, ph3.Deferred, ph3.Dropped)
	c := readInteractiveChoice(cfg, 'A')
	switch c {
	case 'B', 'C', 'D':
		return false
	default:
		return true
	}
}
