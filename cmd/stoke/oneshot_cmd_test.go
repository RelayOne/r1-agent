package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Unit tests for runOneShotCmd — the CLI adapter around
// internal/oneshot. Covers flag parsing + verb dispatch per
// CLOUDSWARM-R1-INTEGRATION §5.6.1.

func TestRunOneShotCmd_MissingVerbExits2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runOneShotCmd(nil, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("stderr should contain usage hint, got: %s", stderr.String())
	}
}

func TestRunOneShotCmd_FlagBeforeVerbExits2(t *testing.T) {
	// `stoke --one-shot --input foo` — no verb given.
	var stdout, stderr bytes.Buffer
	code := runOneShotCmd([]string{"--input", "foo.json"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit=%d want 2", code)
	}
}

func TestRunOneShotCmd_UnknownVerbExits2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runOneShotCmd([]string{"made-up-verb"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown verb") {
		t.Errorf("stderr should mention unknown verb, got: %s", stderr.String())
	}
}

func TestRunOneShotCmd_DecomposeWritesScaffoldJSON(t *testing.T) {
	// Feed an input file containing a JSON payload; expect a
	// scaffold response on stdout with Status=scaffold.
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inPath, []byte(`{"task":"design a landing page"}`), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := runOneShotCmd([]string{"decompose", "--input", inPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var resp struct {
		Verb   string `json:"verb"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("parse: %v (%s)", err, stdout.String())
	}
	if resp.Verb != "decompose" || resp.Status != "scaffold" {
		t.Errorf("got verb=%q status=%q want decompose/scaffold", resp.Verb, resp.Status)
	}
}

func TestRunOneShotCmd_VerifyAndCritiqueAlsoScaffold(t *testing.T) {
	for _, verb := range []string{"verify", "critique"} {
		verb := verb
		t.Run(verb, func(t *testing.T) {
			dir := t.TempDir()
			inPath := filepath.Join(dir, "in.json")
			if err := os.WriteFile(inPath, []byte(`{}`), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			var stdout, stderr bytes.Buffer
			code := runOneShotCmd([]string{verb, "--input", inPath}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr.String())
			}
			var resp struct {
				Verb   string `json:"verb"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if resp.Verb != verb {
				t.Errorf("verb=%q want %q", resp.Verb, verb)
			}
			if resp.Status != "scaffold" {
				t.Errorf("status=%q want scaffold", resp.Status)
			}
		})
	}
}

func TestRunOneShotCmd_NonexistentInputFileExits1(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runOneShotCmd([]string{"decompose", "--input", "/does/not/exist.json"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit=%d want 1", code)
	}
	// Error envelope must parse as JSON for CloudSwarm.
	var envelope map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &envelope); err != nil {
		t.Errorf("stderr should be JSON error envelope, got: %s (err=%v)", stderr.String(), err)
	} else {
		if envelope["status"] != "error" {
			t.Errorf("envelope.status=%q want error", envelope["status"])
		}
		if envelope["verb"] != "decompose" {
			t.Errorf("envelope.verb=%q want decompose", envelope["verb"])
		}
	}
}
