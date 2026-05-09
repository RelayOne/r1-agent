package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeLintExec returns a closure suitable for assigning to
// verifyLintExec — captures stdout/stderr/exit and an error,
// optionally checking the argv it was invoked with.
func fakeLintExec(stdout, stderr string, exit int, err error, captureArgv *[]string) func(ctx context.Context, repoRoot string, argv []string) (string, string, int, error) {
	return func(ctx context.Context, repoRoot string, argv []string) (string, string, int, error) {
		if captureArgv != nil {
			*captureArgv = append([]string(nil), argv...)
		}
		return stdout, stderr, exit, err
	}
}

func unmarshalEnv(t *testing.T, raw string) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\nraw=%s", err, raw)
	}
	return env
}

// TestVerifyLint_NoFindingsOK verifies the happy path: lint exits 0,
// findings empty, envelope is OK, data has counts=0.
func TestVerifyLint_NoFindingsOK(t *testing.T) {
	prev := verifyLintExec
	defer func() { verifyLintExec = prev }()
	verifyLintExec = fakeLintExec("[]", "", 0, nil, nil)

	s := &StokeServer{}
	out, err := s.handleVerifyLint(map[string]interface{}{"repo_root": "."})
	if err != nil {
		t.Fatalf("handleVerifyLint: %v", err)
	}
	env := unmarshalEnv(t, out)
	if !env.OK {
		t.Errorf("env.OK=%v want true", env.OK)
	}
	var data VerifyLintResult
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if data.Failures != 0 || data.Warnings != 0 || data.Total != 0 {
		t.Errorf("counts=(%d,%d,%d) want (0,0,0)", data.Failures, data.Warnings, data.Total)
	}
}

// TestVerifyLint_FailsAreErrEnvelope verifies that ≥1 FAIL findings
// produce an ErrEnvelope (validation code) but Data still carries
// the full result so the caller can drill into specifics.
func TestVerifyLint_FailsAreErrEnvelope(t *testing.T) {
	prev := verifyLintExec
	defer func() { verifyLintExec = prev }()
	stdout := `[
		{"severity":"FAIL","surface":"react","path":"web/App.tsx","line":42,"message":"missing data-testid"},
		{"severity":"WARN","surface":"catalog","path":"","tool":"r1.experimental","message":"un-referenced"}
	]`
	verifyLintExec = fakeLintExec(stdout, "", 1, nil, nil)

	s := &StokeServer{}
	out, _ := s.handleVerifyLint(map[string]interface{}{})
	env := unmarshalEnv(t, out)
	if env.OK {
		t.Errorf("env.OK=%v want false (FAILs should yield ErrEnvelope)", env.OK)
	}
	if env.ErrorCode != "validation" {
		t.Errorf("ErrorCode=%q want validation", env.ErrorCode)
	}
	if !strings.Contains(env.ErrorMessage, "1 FAIL") {
		t.Errorf("ErrorMessage=%q should mention FAIL count", env.ErrorMessage)
	}
	var data VerifyLintResult
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if data.Failures != 1 || data.Warnings != 1 || data.Total != 2 {
		t.Errorf("counts=(%d,%d,%d) want (1,1,2)", data.Failures, data.Warnings, data.Total)
	}
}

// TestVerifyLint_BinaryFailureIsInternalError verifies that a
// non-1 exit (binary missing, parse error, etc) maps to an
// internal_error envelope rather than a validation envelope —
// distinguishing "lint says repo broken" from "lint itself
// broken".
func TestVerifyLint_BinaryFailureIsInternalError(t *testing.T) {
	prev := verifyLintExec
	defer func() { verifyLintExec = prev }()
	verifyLintExec = fakeLintExec("", "go: command not found", 127, errors.New("exec: not found"), nil)

	s := &StokeServer{}
	out, _ := s.handleVerifyLint(map[string]interface{}{})
	env := unmarshalEnv(t, out)
	if env.OK {
		t.Errorf("env.OK=%v want false", env.OK)
	}
	if env.ErrorCode != "internal_error" {
		t.Errorf("ErrorCode=%q want internal_error", env.ErrorCode)
	}
	if !strings.Contains(env.ErrorMessage, "exec: not found") || !strings.Contains(env.ErrorMessage, "go: command not found") {
		t.Errorf("ErrorMessage=%q should surface both err and stderr", env.ErrorMessage)
	}
}

// TestVerifyLint_RootArgPropagatesIntoArgv verifies that
// repo_root from MCP args overrides the canonical command's
// --root flag rather than falling through silently.
func TestVerifyLint_RootArgPropagatesIntoArgv(t *testing.T) {
	prev := verifyLintExec
	defer func() { verifyLintExec = prev }()
	var captured []string
	verifyLintExec = fakeLintExec("[]", "", 0, nil, &captured)

	s := &StokeServer{}
	if _, err := s.handleVerifyLint(map[string]interface{}{
		"repo_root": "/tmp/some-repo",
		"catalog":   "/tmp/cat.json",
		"allowlist": "/tmp/allow.yaml",
	}); err != nil {
		t.Fatalf("handleVerifyLint: %v", err)
	}
	rootIdx := -1
	for i, a := range captured {
		if a == "--root" {
			rootIdx = i
			break
		}
	}
	if rootIdx < 0 || rootIdx+1 >= len(captured) {
		t.Fatalf("--root flag missing in argv: %v", captured)
	}
	// Abs-resolution may turn "/tmp/some-repo" into "/private/tmp/..."
	// on macOS but always preserves the suffix.
	if !strings.HasSuffix(captured[rootIdx+1], "some-repo") {
		t.Errorf("--root value=%q want suffix some-repo", captured[rootIdx+1])
	}
	// Catalog + allowlist must be appended.
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "--catalog /tmp/cat.json") {
		t.Errorf("argv missing --catalog: %v", captured)
	}
	if !strings.Contains(joined, "--allowlist /tmp/allow.yaml") {
		t.Errorf("argv missing --allowlist: %v", captured)
	}
}

// TestVerifyLint_DispatchedViaHandleToolCall covers the routing
// from r1.verify.* prefix through HandleToolCall — the on-the-wire
// path agents use.
func TestVerifyLint_DispatchedViaHandleToolCall(t *testing.T) {
	prev := verifyLintExec
	defer func() { verifyLintExec = prev }()
	verifyLintExec = fakeLintExec("[]", "", 0, nil, nil)

	s := &StokeServer{}
	out, err := s.HandleToolCall("r1.verify.lint", map[string]interface{}{"repo_root": "."})
	if err != nil {
		t.Fatalf("HandleToolCall(r1.verify.lint): %v", err)
	}
	env := unmarshalEnv(t, out)
	if !env.OK {
		t.Errorf("dispatched envelope OK=%v want true", env.OK)
	}
	if env.Links == nil || env.Links.Self != "r1.verify.lint" {
		t.Errorf("envelope links self=%v want r1.verify.lint", env.Links)
	}
}

// TestVerifyLint_UnknownVerifyTool verifies the fallback for
// an unknown r1.verify.* tool — should return an explicit error
// (consistent with cortex/lanes prefixes).
func TestVerifyLint_UnknownVerifyTool(t *testing.T) {
	s := &StokeServer{}
	_, err := s.HandleToolCall("r1.verify.bogus", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for unknown verify tool")
	}
	if !strings.Contains(err.Error(), "unknown verify tool") {
		t.Errorf("err=%v want 'unknown verify tool'", err)
	}
}
