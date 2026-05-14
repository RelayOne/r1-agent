package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/oneshot"
)

// Unit tests for runOneShotCmd — the CLI adapter around
// internal/oneshot. Covers flag parsing, verb dispatch, the new
// A3 hardening flags (--max-mem, --timeout, --audit-endpoint),
// and the signal-aware ctx wiring.

func TestRunOneShotCmd_MissingVerbExits2(harness *testing.T) {
	stubMemLimitForCLITests(harness)
	var stdout, stderr bytes.Buffer
	code := runOneShotCmd(nil, &stdout, &stderr)
	if code != oneshot.ExitUsage {
		harness.Errorf("exit=%d want %d", code, oneshot.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		harness.Errorf("stderr should contain usage hint, got: %s", stderr.String())
	}
}

func TestRunOneShotCmd_FlagBeforeVerbExits2(harness *testing.T) {
	stubMemLimitForCLITests(harness)
	var stdout, stderr bytes.Buffer
	code := runOneShotCmd([]string{"--input", "foo.json"}, &stdout, &stderr)
	if code != oneshot.ExitUsage {
		harness.Errorf("exit=%d want %d", code, oneshot.ExitUsage)
	}
}

func TestRunOneShotCmd_UnknownVerbExits2(harness *testing.T) {
	stubMemLimitForCLITests(harness)
	var stdout, stderr bytes.Buffer
	code := runOneShotCmd([]string{"made-up-verb"}, &stdout, &stderr)
	if code != oneshot.ExitUsage {
		harness.Errorf("exit=%d want %d", code, oneshot.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "unknown verb") {
		harness.Errorf("stderr should mention unknown verb, got: %s", stderr.String())
	}
}

func TestRunOneShotCmd_AcceptsJSONFlag(harness *testing.T) {
	stubMemLimitForCLITests(harness)
	dir := harness.TempDir()
	inPath := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inPath, []byte(`{"task":"design a landing page"}`), 0o600); err != nil {
		harness.Fatalf("write input: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := runOneShotCmd([]string{"decompose", "--json", "--input", inPath}, &stdout, &stderr)
	if code != oneshot.ExitOK {
		harness.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var resp struct {
		Verb string `json:"verb"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		harness.Fatalf("parse: %v (%s)", err, stdout.String())
	}
	if resp.Verb != "decompose" {
		harness.Errorf("verb=%q want decompose", resp.Verb)
	}
}

func TestRunOneShotCmd_DecomposeWritesScaffoldJSON(harness *testing.T) {
	stubMemLimitForCLITests(harness)
	harness.Run("real task returns ok with plan", func(sub *testing.T) {
		dir := sub.TempDir()
		inPath := filepath.Join(dir, "in.json")
		if err := os.WriteFile(inPath, []byte(`{"task":"design a landing page"}`), 0o600); err != nil {
			sub.Fatalf("write input: %v", err)
		}
		var stdout, stderr bytes.Buffer
		code := runOneShotCmd([]string{"decompose", "--input", inPath}, &stdout, &stderr)
		if code != oneshot.ExitOK {
			sub.Fatalf("exit=%d stderr=%s", code, stderr.String())
		}
		var resp struct {
			Verb            string  `json:"verb"`
			Status          string  `json:"status"`
			ProviderUsed    string  `json:"provider_used"`
			CostEstimateUSD float64 `json:"cost_estimate_usd"`
			Data            struct {
				Plan         json.RawMessage `json:"plan"`
				StrategyUsed string          `json:"strategy_used"`
			} `json:"data"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
			sub.Fatalf("parse: %v (%s)", err, stdout.String())
		}
		if resp.Verb != "decompose" || resp.Status != "ok" {
			sub.Errorf("got verb=%q status=%q want decompose/ok", resp.Verb, resp.Status)
		}
		if resp.ProviderUsed != "r1_core" {
			sub.Errorf("provider_used=%q want r1_core", resp.ProviderUsed)
		}
		if resp.CostEstimateUSD != 0 {
			sub.Errorf("cost_estimate_usd=%v want 0", resp.CostEstimateUSD)
		}
		if len(resp.Data.Plan) == 0 {
			sub.Error("data.plan should be non-empty for a real task")
		}
		if resp.Data.StrategyUsed == "" {
			sub.Error("data.strategy_used should be populated")
		}
	})

	harness.Run("empty task falls through to scaffold", func(sub *testing.T) {
		dir := sub.TempDir()
		inPath := filepath.Join(dir, "in.json")
		if err := os.WriteFile(inPath, []byte(`{}`), 0o600); err != nil {
			sub.Fatalf("write input: %v", err)
		}
		var stdout, stderr bytes.Buffer
		code := runOneShotCmd([]string{"decompose", "--input", inPath}, &stdout, &stderr)
		if code != oneshot.ExitOK {
			sub.Fatalf("exit=%d stderr=%s", code, stderr.String())
		}
		var resp struct {
			Verb   string `json:"verb"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
			sub.Fatalf("parse: %v (%s)", err, stdout.String())
		}
		if resp.Verb != "decompose" || resp.Status != "scaffold" {
			sub.Errorf("got verb=%q status=%q want decompose/scaffold", resp.Verb, resp.Status)
		}
	})
}

func TestRunOneShotCmd_VerifyAndCritiqueAlsoScaffold(harness *testing.T) {
	stubMemLimitForCLITests(harness)
	for _, verb := range []string{"verify", "critique"} {
		verb := verb
		harness.Run(verb, func(sub *testing.T) {
			dir := sub.TempDir()
			inPath := filepath.Join(dir, "in.json")
			if err := os.WriteFile(inPath, []byte(`{}`), 0o600); err != nil {
				sub.Fatalf("write: %v", err)
			}
			var stdout, stderr bytes.Buffer
			code := runOneShotCmd([]string{verb, "--input", inPath}, &stdout, &stderr)
			if code != oneshot.ExitOK {
				sub.Fatalf("exit=%d stderr=%s", code, stderr.String())
			}
			var resp struct {
				Verb   string `json:"verb"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
				sub.Fatalf("parse: %v", err)
			}
			if resp.Verb != verb {
				sub.Errorf("verb=%q want %q", resp.Verb, verb)
			}
			if resp.Status != "scaffold" {
				sub.Errorf("status=%q want scaffold", resp.Status)
			}
		})
	}
}

func TestRunOneShotCmd_NonexistentInputFileExits1(harness *testing.T) {
	stubMemLimitForCLITests(harness)
	var stdout, stderr bytes.Buffer
	code := runOneShotCmd([]string{"decompose", "--input", "/does/not/exist.json"}, &stdout, &stderr)
	if code != oneshot.ExitRuntime {
		harness.Errorf("exit=%d want %d", code, oneshot.ExitRuntime)
	}
	var envelope map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &envelope); err != nil {
		harness.Errorf("stderr should be JSON error envelope, got: %s (err=%v)", stderr.String(), err)
	} else {
		if envelope["status"] != "error" {
			harness.Errorf("envelope.status=%q want error", envelope["status"])
		}
		if envelope["verb"] != "decompose" {
			harness.Errorf("envelope.verb=%q want decompose", envelope["verb"])
		}
		if envelope["correlation_id"] == "" {
			harness.Errorf("envelope.correlation_id should be populated")
		}
	}
}

// --- A3 hardening flag validation -----------------------------------

func TestRunOneShotCmd_MaxMemRangeValidation(harness *testing.T) {
	stubMemLimitForCLITests(harness)
	cases := []struct {
		name string
		val  string
	}{
		{"zero", "0"},
		{"too small", "16"},
		{"too large", "32768"},
		{"negative", "-1"},
	}
	for _, c := range cases {
		c := c
		harness.Run(c.name, func(sub *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runOneShotCmd([]string{"decompose", "--max-mem", c.val}, &stdout, &stderr)
			if code != oneshot.ExitUsage {
				sub.Errorf("exit=%d want %d (stderr=%s)", code, oneshot.ExitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "--max-mem must be in") {
				sub.Errorf("stderr should contain range message, got: %s", stderr.String())
			}
		})
	}
}

func TestRunOneShotCmd_TimeoutRangeValidation(harness *testing.T) {
	stubMemLimitForCLITests(harness)
	cases := []struct {
		name string
		val  string
	}{
		{"too small", "50ms"},
		{"too large", "1h"},
	}
	for _, c := range cases {
		c := c
		harness.Run(c.name, func(sub *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runOneShotCmd([]string{"decompose", "--timeout", c.val}, &stdout, &stderr)
			if code != oneshot.ExitUsage {
				sub.Errorf("exit=%d want %d (stderr=%s)", code, oneshot.ExitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "--timeout must be in") {
				sub.Errorf("stderr should contain range message, got: %s", stderr.String())
			}
		})
	}
}

func TestRunOneShotCmd_AuditFlagValidation(harness *testing.T) {
	stubMemLimitForCLITests(harness)

	harness.Run("non-loopback http rejected", func(sub *testing.T) {
		sub.Setenv("R1_AUDIT_TOKEN", "")
		var stdout, stderr bytes.Buffer
		code := runOneShotCmd([]string{
			"decompose",
			"--audit-endpoint", "http://example.com/audit",
			"--audit-token", "secret",
		}, &stdout, &stderr)
		if code != oneshot.ExitUsage {
			sub.Errorf("exit=%d want %d (stderr=%s)", code, oneshot.ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "https or http loopback") {
			sub.Errorf("stderr should mention scheme, got: %s", stderr.String())
		}
	})

	harness.Run("missing token rejected", func(sub *testing.T) {
		sub.Setenv("R1_AUDIT_TOKEN", "")
		var stdout, stderr bytes.Buffer
		code := runOneShotCmd([]string{
			"decompose",
			"--audit-endpoint", "https://relaygate.example.com/audit",
		}, &stdout, &stderr)
		if code != oneshot.ExitUsage {
			sub.Errorf("exit=%d want %d (stderr=%s)", code, oneshot.ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "--audit-token") {
			sub.Errorf("stderr should mention --audit-token, got: %s", stderr.String())
		}
	})

	harness.Run("env token accepted", func(sub *testing.T) {
		sub.Setenv("R1_AUDIT_TOKEN", "env-secret")
		dir := sub.TempDir()
		inPath := filepath.Join(dir, "in.json")
		if err := os.WriteFile(inPath, []byte(`{"task":"design x"}`), 0o600); err != nil {
			sub.Fatalf("write: %v", err)
		}
		var stdout, stderr bytes.Buffer
		code := runOneShotCmd([]string{
			"decompose",
			"--input", inPath,
			"--audit-endpoint", "http://127.0.0.1:1/audit-never-reached",
		}, &stdout, &stderr)
		if code != oneshot.ExitOK {
			sub.Errorf("exit=%d stderr=%s", code, stderr.String())
		}
	})
}

func TestRunOneShotCmd_CorrelationIDPrecedence(harness *testing.T) {
	stubMemLimitForCLITests(harness)

	harness.Run("env wins over flag", func(sub *testing.T) {
		sub.Setenv("R1_CORRELATION_ID", "env-id-123")
		var stdout, stderr bytes.Buffer
		code := runOneShotCmd([]string{
			"decompose",
			"--correlation-id", "flag-id-456",
			"--input", "/does/not/exist",
		}, &stdout, &stderr)
		if code != oneshot.ExitRuntime {
			sub.Fatalf("exit=%d stderr=%s", code, stderr.String())
		}
		var env map[string]string
		if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &env); err != nil {
			sub.Fatalf("parse stderr: %v", err)
		}
		if env["correlation_id"] != "env-id-123" {
			sub.Errorf("correlation_id=%q want env-id-123", env["correlation_id"])
		}
	})

	harness.Run("flag wins when env empty", func(sub *testing.T) {
		sub.Setenv("R1_CORRELATION_ID", "")
		var stdout, stderr bytes.Buffer
		code := runOneShotCmd([]string{
			"decompose",
			"--correlation-id", "flag-id-789",
			"--input", "/does/not/exist",
		}, &stdout, &stderr)
		if code != oneshot.ExitRuntime {
			sub.Fatalf("exit=%d stderr=%s", code, stderr.String())
		}
		var env map[string]string
		if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &env); err != nil {
			sub.Fatalf("parse: %v", err)
		}
		if env["correlation_id"] != "flag-id-789" {
			sub.Errorf("correlation_id=%q want flag-id-789", env["correlation_id"])
		}
	})

	harness.Run("generated when both empty", func(sub *testing.T) {
		sub.Setenv("R1_CORRELATION_ID", "")
		var stdout, stderr bytes.Buffer
		code := runOneShotCmd([]string{
			"decompose",
			"--input", "/does/not/exist",
		}, &stdout, &stderr)
		if code != oneshot.ExitRuntime {
			sub.Fatalf("exit=%d stderr=%s", code, stderr.String())
		}
		var env map[string]string
		if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &env); err != nil {
			sub.Fatalf("parse: %v", err)
		}
		if len(env["correlation_id"]) != 32 {
			sub.Errorf("generated correlation_id should be 32 hex chars, got %q (len %d)",
				env["correlation_id"], len(env["correlation_id"]))
		}
	})
}

func TestRunOneShotCmd_MemoryLimitFailureSurfacesEnvelope(harness *testing.T) {
	prev := applyMemoryLimitWrapped
	applyMemoryLimitWrapped = func(int) error {
		return fmt.Errorf("simulated prlimit failure")
	}
	harness.Cleanup(func() { applyMemoryLimitWrapped = prev })

	var stdout, stderr bytes.Buffer
	code := runOneShotCmd([]string{"decompose", "--max-mem", "64"}, &stdout, &stderr)
	if code != oneshot.ExitMemory {
		harness.Errorf("exit=%d want %d", code, oneshot.ExitMemory)
	}
	var env map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &env); err != nil {
		harness.Fatalf("parse stderr: %v (%s)", err, stderr.String())
	}
	if env["event"] != oneshot.EventMemoryLimitHit {
		harness.Errorf("event=%v want %s", env["event"], oneshot.EventMemoryLimitHit)
	}
	if env["reason"] != "prlimit_failed" {
		harness.Errorf("reason=%v want prlimit_failed", env["reason"])
	}
}

// TestRunOneShotCmd_SignalCtxRegistered exercises the
// signal.NotifyContext wiring from spec §T3.1: a SIGTERM
// sent to the current process must cause runOneShotCmd to
// return within 1 s with ExitSIGTERM.
func TestRunOneShotCmd_SignalCtxRegistered(harness *testing.T) {
	stubMemLimitForCLITests(harness)
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		harness.Fatalf("pipe: %v", err)
	}
	defer wPipe.Close()
	defer rPipe.Close()

	if !procPathAvailable("/proc/self/fd/0") {
		harness.Skip("SignalCtxRegistered needs /proc; skipping on non-Linux")
	}
	procPath := fmt.Sprintf("/proc/self/fd/%d", rPipe.Fd())

	done := make(chan int, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		code := runOneShotCmd([]string{
			"decompose",
			"--input", procPath,
			"--timeout", "10s",
		}, &stdout, &stderr)
		done <- code
	}()

	time.Sleep(80 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		harness.Fatalf("kill: %v", err)
	}
	select {
	case code := <-done:
		if code != oneshot.ExitSIGTERM {
			harness.Errorf("exit=%d want %d", code, oneshot.ExitSIGTERM)
		}
	case <-time.After(1 * time.Second):
		harness.Fatal("runOneShotCmd did not return within 1s of SIGTERM")
	}
}
