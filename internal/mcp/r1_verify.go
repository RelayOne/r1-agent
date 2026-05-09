// r1_verify.go — handlers for the r1.verify.* MCP tools.
//
// r1.verify.lint runs tools/lint-view-without-api against the
// caller-supplied repo root and translates its Findings into the
// Slack-style envelope from envelope.go. Same exit semantics as
// the CI Makefile target so the agentic verify path and CI agree
// on what counts as a regression.
//
// Closes audit/scan-go-stubs.md item "internal/mcp/verify_lint_wiring.go
// PARTIAL". The shell-out approach is the documented pre-spec-5
// recipe; the in-process refactor lands once spec 5 ships.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// LintFinding mirrors the JSON shape emitted by
// `tools/lint-view-without-api --json`. Kept local so the daemon
// doesn't have to import the tool package (which is package main).
type LintFinding struct {
	Severity string `json:"severity"`
	Surface  string `json:"surface"`
	Path     string `json:"path"`
	Line     int    `json:"line,omitempty"`
	Tool     string `json:"tool,omitempty"`
	StableID string `json:"stable_id,omitempty"`
	Message  string `json:"message"`
}

// VerifyLintResult is the data payload of an OK r1.verify.lint
// envelope. Counts let agents reason about severity without
// re-counting the slice.
type VerifyLintResult struct {
	OK       bool          `json:"ok"`
	Failures int           `json:"failures"`
	Warnings int           `json:"warnings"`
	Total    int           `json:"total"`
	Findings []LintFinding `json:"findings"`
	Command  []string      `json:"command"`
}

// verifyLintExec is overridable in tests so we don't have to spawn
// the real lint binary in unit tests. Production callers leave it
// at its default (runLintViewWithoutAPI) which spawns + captures.
var verifyLintExec = runLintViewWithoutAPI

// defaultVerifyLintTimeout caps the lint subprocess so an
// unbounded scan can't pin the MCP handler. The CI invocation
// completes in <5s on a 50k-file tree; 60s is the safe ceiling.
const defaultVerifyLintTimeout = 60 * time.Second

// handleVerifyLint implements the r1.verify.lint MCP tool.
// Args:
//
//	repo_root (string, optional) — defaults to the server's CWD
//	catalog   (string, optional) — overrides --catalog flag
//	allowlist (string, optional) — overrides --allowlist flag
//
// Returns: OKEnvelope (data=VerifyLintResult) when 0 FAILs,
// ErrEnvelope("validation", "...") when ≥1 FAIL.
func (s *StokeServer) handleVerifyLint(args map[string]interface{}) (string, error) {
	repoRoot, _ := args["repo_root"].(string)
	if repoRoot == "" {
		repoRoot = "."
	}
	catalogPath, _ := args["catalog"].(string)
	allowPath, _ := args["allowlist"].(string)

	cmd := append([]string(nil), LintViewWithoutAPICommand()...)
	// Patch the --root arg if the caller supplied one — the canonical
	// command uses ".", agents pointing at a different repo root need
	// the override.
	if abs, err := filepath.Abs(repoRoot); err == nil {
		cmd = patchFlag(cmd, "--root", abs)
	}
	if catalogPath != "" {
		cmd = append(cmd, "--catalog", catalogPath)
	}
	if allowPath != "" {
		cmd = append(cmd, "--allowlist", allowPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultVerifyLintTimeout)
	defer cancel()
	stdout, stderr, exitCode, err := verifyLintExec(ctx, repoRoot, cmd)
	if err != nil && exitCode != 1 {
		// exit 1 = lint reported FAILs (expected); other exits are
		// genuine errors (binary not found, parse error, etc).
		env := ErrEnvelope("r1.verify.lint", "internal_error",
			fmt.Sprintf("lint-view-without-api invocation failed: %v: %s", err, strings.TrimSpace(stderr)),
			"r1.verify.build", "r1.verify.test")
		return marshalEnvelopeOrPanic(env), nil
	}

	findings, err := parseLintFindings(stdout)
	if err != nil {
		env := ErrEnvelope("r1.verify.lint", "internal_error",
			fmt.Sprintf("parsing lint findings failed: %v", err),
			"r1.verify.build", "r1.verify.test")
		return marshalEnvelopeOrPanic(env), nil
	}

	failures, warnings := countSeverities(findings)
	result := VerifyLintResult{
		OK:       failures == 0,
		Failures: failures,
		Warnings: warnings,
		Total:    len(findings),
		Findings: findings,
		Command:  cmd,
	}

	if failures > 0 {
		env := ErrEnvelope("r1.verify.lint", "validation",
			fmt.Sprintf("%d FAIL findings — see data.findings", failures),
			"r1.verify.build", "r1.verify.test")
		raw, _ := json.Marshal(result)
		env.Data = raw
		return marshalEnvelopeOrPanic(env), nil
	}
	env := OKEnvelope("r1.verify.lint", result, "r1.verify.build", "r1.verify.test")
	return marshalEnvelopeOrPanic(env), nil
}

// runLintViewWithoutAPI spawns the canonical lint command and
// returns (stdout, stderr, exitCode, error). The error is set
// only on system failures (binary missing, ctx canceled); a
// nonzero exitCode with empty error is the lint reporting FAILs.
func runLintViewWithoutAPI(ctx context.Context, repoRoot string, argv []string) (string, string, int, error) {
	if len(argv) == 0 {
		return "", "", -1, errors.New("empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = repoRoot
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
			// exit 1 = FAILs reported; not a system error.
			if exitCode == 1 {
				err = nil
			}
		} else {
			exitCode = -1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode, err
}

// parseLintFindings decodes the JSON-mode output of the lint
// tool. Empty stdout ([] or "") is fine and yields zero findings.
func parseLintFindings(stdout string) ([]LintFinding, error) {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" || stdout == "null" {
		return nil, nil
	}
	var findings []LintFinding
	if err := json.Unmarshal([]byte(stdout), &findings); err != nil {
		return nil, err
	}
	return findings, nil
}

// countSeverities sums FAIL + WARN findings (INFO is neither —
// callers can read it from the slice if they care).
func countSeverities(findings []LintFinding) (failures, warnings int) {
	for _, f := range findings {
		switch strings.ToUpper(f.Severity) {
		case "FAIL":
			failures++
		case "WARN":
			warnings++
		}
	}
	return failures, warnings
}

// patchFlag finds an existing --flag and replaces its value, or
// appends both flag+value if not present. Used so the canonical
// command from LintViewWithoutAPICommand() can be safely modified
// per-call without callers having to know its exact shape.
func patchFlag(argv []string, flag, value string) []string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			argv[i+1] = value
			return argv
		}
	}
	return append(argv, flag, value)
}

// marshalEnvelopeOrPanic returns the JSON encoding of env or a
// sentinel error envelope if marshaling fails (which would
// indicate a bug in our envelope schema, not user data).
func marshalEnvelopeOrPanic(env Envelope) string {
	raw, err := MarshalEnvelope(env)
	if err != nil {
		// Encoding the canonical envelope shape should never fail;
		// if it does, return a static error string the test bench
		// can grep for.
		return `{"ok":false,"error_code":"internal_error","error_message":"envelope marshal failed"}`
	}
	return string(raw)
}
