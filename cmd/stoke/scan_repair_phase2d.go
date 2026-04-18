// scan_repair_phase2d.go — H-17 Phase 2d codex deep review shell-out.
//
// If codex binary + .claude/scripts/codex-review.sh are both available,
// invoke the script with --all and parse the JSON output into
// audit/perspectives/codex-review.qa.md. Missing binary OR missing
// script OR malformed JSON → log warning and skip cleanly (never
// fail the overall scan-repair run on Phase 2d alone).

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// codexFinding is the shape we expect from codex-review.sh --all.
// Extra fields are ignored; missing fields render as "(unknown)" in
// the output.
type codexFinding struct {
	File     string `json:"file"`
	Line     any    `json:"line"`      // int or string "12-15"
	Severity string `json:"severity"`
	Category string `json:"category"`
	Message  string `json:"message"`
	Fix      string `json:"fix"`
}

// codexReviewJSON is the top-level JSON shape returned by the script.
// The spec says the script emits an array; we also accept the nested
// "findings" shape that .claude/scripts/codex-review.sh actually uses
// today. Either form works.
type codexReviewJSON struct {
	Verdict  string         `json:"verdict"`
	Findings []codexFinding `json:"findings"`
}

// runPhase2dCodexReview is the Phase 2d entrypoint. Returns a
// non-nil error only for truly unrecoverable situations (the caller
// logs and continues); all the expected-missing paths return nil.
func runPhase2dCodexReview(ctx context.Context, cfg *scanRepairConfig) error {
	scriptPath := filepath.Join(cfg.Repo, ".claude", "scripts", "codex-review.sh")
	outDir := filepath.Join(cfg.Repo, "audit", "perspectives")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("mkdir perspectives: %w", err)
	}

	// Test hook: if cfg.codexCaller is set, bypass the path/script
	// checks and feed the test's JSON directly to the parser.
	if cfg.codexCaller != nil {
		raw, err := cfg.codexCaller(ctx, cfg.Repo)
		if err != nil {
			fmt.Printf("🤖 Phase 2d codex: error — %v\n", err)
			return nil
		}
		return writeCodexReviewFromJSON(outDir, raw)
	}

	// Pre-flight checks — missing codex OR script is an expected-skip.
	if _, err := exec.LookPath(cfg.CodexBin); err != nil {
		fmt.Printf("🤖 Phase 2d codex: skipped (codex binary not on PATH)\n")
		return nil
	}
	if _, err := os.Stat(scriptPath); err != nil {
		fmt.Printf("🤖 Phase 2d codex: skipped (codex-review.sh missing at %s)\n", scriptPath)
		return nil
	}

	// Run the script with a 5-minute ceiling. stdout → JSON buffer,
	// stderr discarded (the script is noisy with progress logs).
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", scriptPath, "--all")
	cmd.Dir = cfg.Repo
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("🤖 Phase 2d codex: script failed: %v (stderr: %s)\n", err, strings.TrimSpace(stderr.String()))
		return nil
	}

	// Persist the raw JSON so operators can inspect.
	rawPath := filepath.Join(cfg.Repo, "audit", "codex-review.json")
	if err := os.WriteFile(rawPath, stdout.Bytes(), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "  [Phase 2d] write %s: %v\n", rawPath, err)
	}

	return writeCodexReviewFromJSON(outDir, stdout.Bytes())
}

// writeCodexReviewFromJSON parses codex output and writes the
// findings-shaped markdown to audit/perspectives/codex-review.qa.md.
// Malformed JSON → log warning, still write an explanatory .qa.md so
// Phase 3a has something to aggregate.
func writeCodexReviewFromJSON(outDir string, raw []byte) error {
	outPath := filepath.Join(outDir, "codex-review.qa.md")
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		fmt.Printf("🤖 Phase 2d codex: empty output — skipping\n")
		return nil
	}

	// Try both shapes: top-level array (spec) OR top-level object with
	// "findings" array (current script).
	var findings []codexFinding
	var verdict string
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &findings); err != nil {
			fmt.Printf("🤖 Phase 2d codex: malformed JSON (array): %v — writing note file\n", err)
			_ = os.WriteFile(outPath, []byte("# Codex Review\n\n(codex returned malformed JSON)\n"), 0644)
			return nil
		}
	} else {
		var obj codexReviewJSON
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			fmt.Printf("🤖 Phase 2d codex: malformed JSON (obj): %v — writing note file\n", err)
			_ = os.WriteFile(outPath, []byte("# Codex Review\n\n(codex returned malformed JSON)\n"), 0644)
			return nil
		}
		findings = obj.Findings
		verdict = obj.Verdict
	}

	var buf bytes.Buffer
	buf.WriteString("# Codex Review\n\n")
	if verdict != "" {
		fmt.Fprintf(&buf, "**Verdict:** %s\n\n", verdict)
	}
	if len(findings) == 0 {
		buf.WriteString("None.\n")
	} else {
		for _, f := range findings {
			sev := strings.ToUpper(strings.TrimSpace(f.Severity))
			if sev == "" {
				sev = "MEDIUM"
			}
			file := strings.TrimSpace(f.File)
			if file == "" {
				file = "unknown"
			}
			line := formatCodexLine(f.Line)
			msg := strings.TrimSpace(f.Message)
			fix := strings.TrimSpace(f.Fix)
			if fix == "" {
				fix = "(no fix suggested)"
			}
			fmt.Fprintf(&buf, "- [%s] %s:%s — %s — fix: %s\n", sev, file, line, msg, fix)
		}
	}
	fmt.Printf("🤖 Phase 2d codex: %d findings\n", len(findings))
	return os.WriteFile(outPath, buf.Bytes(), 0644)
}

// formatCodexLine renders the codex "line" field which might be an
// int, a string like "12-15", or missing. Keeps the output line-format
// compatible with the persona finding parser.
func formatCodexLine(raw any) string {
	switch v := raw.(type) {
	case nil:
		return "?"
	case string:
		if v == "" {
			return "?"
		}
		return v
	case float64:
		return fmt.Sprintf("%d", int(v))
	case int:
		return fmt.Sprintf("%d", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}
