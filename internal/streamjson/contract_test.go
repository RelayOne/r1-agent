// Package streamjson — contract_test.go
//
// Spec-2 cloudswarm-protocol item 13: contract tests. CloudSwarm's
// test_execute_stoke.py fixture parser asserts that every emitted
// NDJSON line has a "type" string field (or a legacy "event_type"
// alias) and that well-known subtypes carry their required fields.
// These tests lock that contract from the Go side: any regression in
// the emitter's field shape is caught at build time rather than
// surfacing as a runtime parse failure inside the Temporal activity.
package streamjson

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scanLines reads newline-delimited output and returns the non-empty
// lines in order. Kept as a helper so tests don't all repeat the
// bufio.Scanner wiring.
func scanLines(b []byte) []string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for sc.Scan() {
		text := sc.Text()
		if text == "" {
			continue
		}
		lines = append(lines, text)
	}
	return lines
}

// TestContractEveryEmittedLineIsValidJSON exercises every emitter
// entrypoint used by the CloudSwarm protocol and asserts the output
// is strictly line-delimited JSON with a non-empty "type" string.
func TestContractEveryEmittedLineIsValidJSON(t *testing.T) {
	var buf bytes.Buffer
	tl := NewTwoLane(&buf, true)
	// Exercise every surface the run_cmd.go dispatcher touches.
	tl.EmitSystem("session.start", map[string]any{"_stoke.dev/governance_tier": "community"})
	tl.EmitSystem("plan.ready", map[string]any{"_stoke.dev/session_count": 2})
	tl.EmitSystem("task.complete", map[string]any{"_stoke.dev/success": true})
	tl.EmitSystem("descent.tier", map[string]any{
		"_stoke.dev/tier":     "T4-code-repair",
		"_stoke.dev/ac_id":    "AC-03",
		"_stoke.dev/attempt":  2,
		"_stoke.dev/category": "code_bug",
	})
	tl.EmitSystem("stoke.cost", map[string]any{"_stoke.dev/total_usd": 0.42})
	tl.EmitTopLevel("hitl_required", map[string]any{
		"reason":        "soft-pass at T8",
		"approval_type": "soft_pass",
		"file":          "internal/foo/bar.go",
	})
	tl.EmitTopLevel("mission.aborted", map[string]any{"reason": "signal"})
	tl.EmitTopLevel("complete", map[string]any{
		"subtype":              "success",
		"_stoke.dev/exit_code": 0,
	})
	tl.Drain(time.Second)

	lines := scanLines(buf.Bytes())
	if len(lines) != 8 {
		t.Fatalf("emitted %d lines, want 8: %q", len(lines), buf.String())
	}
	for _, ln := range lines {
		if !json.Valid([]byte(ln)) {
			t.Errorf("line is not valid JSON: %q", ln)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Errorf("unmarshal failed: %v (line=%q)", err, ln)
			continue
		}
		typeField, ok := m["type"].(string)
		if !ok || typeField == "" {
			t.Errorf("line missing type field: %q", ln)
		}
	}
	// Every line must end with exactly one \n.
	out := buf.String()
	if len(out) == 0 || out[len(out)-1] != '\n' {
		t.Errorf("final byte is not \\n: %q", out)
	}
}

// TestContractHITLRequiredCarriesReason checks that hitl_required
// lines satisfy CloudSwarm's execute_stoke.py:343-387 parser, which
// reads the "reason" string field. Missing/empty reason is a
// breaking contract change.
func TestContractHITLRequiredCarriesReason(t *testing.T) {
	var buf bytes.Buffer
	tl := NewTwoLane(&buf, true)
	tl.EmitTopLevel(TypeHITLRequired, map[string]any{
		"reason":        "soft-pass at T8",
		"approval_type": "soft_pass",
	})
	tl.Drain(time.Second)

	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["type"] != "hitl_required" {
		t.Errorf("type=%v, want hitl_required", m["type"])
	}
	reason, ok := m["reason"].(string)
	if !ok || reason == "" {
		t.Errorf("hitl_required must carry a non-empty reason string")
	}
}

// TestContractCloudSwarmFixtures loads every *.jsonl under testdata/
// cloudswarm_fixtures/ and asserts each line satisfies the contract:
// valid JSON, present "type" field, and the well-known subtype
// carries its required fields.
func TestContractCloudSwarmFixtures(t *testing.T) {
	root := filepath.Join("testdata", "cloudswarm_fixtures")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no fixtures found in %s", root)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		lines := scanLines(data)
		for i, ln := range lines {
			if !json.Valid([]byte(ln)) {
				t.Errorf("%s:%d not valid JSON: %q", path, i+1, ln)
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(ln), &m); err != nil {
				t.Errorf("%s:%d unmarshal: %v", path, i+1, err)
				continue
			}
			typeField, ok := m["type"].(string)
			if !ok || typeField == "" {
				t.Errorf("%s:%d missing type", path, i+1)
			}
			switch e.Name() {
			case "hitl_required.jsonl":
				if _, ok := m["reason"].(string); !ok {
					t.Errorf("%s:%d hitl_required missing reason", path, i+1)
				}
				if _, ok := m["approval_type"].(string); !ok {
					t.Errorf("%s:%d hitl_required missing approval_type", path, i+1)
				}
			case "task_complete.jsonl":
				if m["subtype"] != "task.complete" {
					t.Errorf("%s:%d subtype=%v, want task.complete", path, i+1, m["subtype"])
				}
			case "descent_tier.jsonl":
				if m["subtype"] != "descent.tier" {
					t.Errorf("%s:%d subtype=%v, want descent.tier", path, i+1, m["subtype"])
				}
			case "complete.jsonl":
				if typeField != "complete" {
					t.Errorf("%s:%d type=%v, want complete", path, i+1, typeField)
				}
			}
		}
	}
}

// TestContractCompletePresentAfterDrain verifies that the complete
// event survives drain and is parseable by CloudSwarm's fixture
// reader. The TwoLane drainer does not guarantee critical-vs-
// observability ordering within a single drain cycle (the select
// is nondeterministic), but the contract only requires that
// complete is in the output before the process exits — which Drain
// preserves.
func TestContractCompletePresentAfterDrain(t *testing.T) {
	var buf bytes.Buffer
	tl := NewTwoLane(&buf, true)
	tl.EmitSystem("session.start", map[string]any{})
	tl.EmitSystem("plan.ready", map[string]any{})
	tl.EmitTopLevel("complete", map[string]any{"subtype": "success"})
	tl.Drain(time.Second)

	out := strings.TrimSpace(buf.String())
	if out == "" {
		t.Fatalf("empty output")
	}
	lines := scanLines([]byte(out))
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), out)
	}
	foundComplete := false
	for _, ln := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Errorf("unmarshal: %v (line=%q)", err, ln)
			continue
		}
		if m["type"] == "complete" {
			foundComplete = true
			if m["subtype"] != "success" {
				t.Errorf("complete subtype=%v, want success", m["subtype"])
			}
		}
	}
	if !foundComplete {
		t.Errorf("complete line not found in output: %q", out)
	}
}
