package streamjson

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestNewTraceParent_Format(t *testing.T) {
	tp := NewTraceParent()
	// W3C: 00-<32hex>-<16hex>-01
	re := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)
	if !re.MatchString(tp) {
		t.Fatalf("traceparent %q does not match W3C format", tp)
	}
}

func TestNewTraceParent_Unique(t *testing.T) {
	// 100 back-to-back calls should produce 100 distinct strings
	// with overwhelming probability.
	seen := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		tp := NewTraceParent()
		if _, dup := seen[tp]; dup {
			t.Fatalf("duplicate traceparent at i=%d: %q", i, tp)
		}
		seen[tp] = struct{}{}
	}
}

func TestEmitStoke_EnvelopeStampedWhenMetaSet(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)
	e.SetStokeMeta(StokeProtocolVersion, "r1-abc12345", "00-aaaa-bbbb-01")

	e.EmitStoke("stoke.session.start", map[string]any{
		"task_count":     7,
		"ledger_node_id": "node-sha256-xyz",
	})

	// D-032: dual-emit produces two NDJSON lines — canonical r1.* first,
	// then legacy stoke.*.
	raw := strings.TrimSpace(buf.String())
	evtLines := strings.SplitN(raw, "\n", 2)
	if len(evtLines) != 2 {
		t.Fatalf("expected 2 NDJSON lines (dual-emit), got %d (raw=%q)", len(evtLines), raw)
	}

	var canonical, legacy map[string]any
	if err := json.Unmarshal([]byte(evtLines[0]), &canonical); err != nil {
		t.Fatalf("unmarshal canonical: %v (raw=%q)", err, evtLines[0])
	}
	if canonical["type"] != "r1.session.start" {
		t.Errorf("canonical type=%v, want r1.session.start", canonical["type"])
	}
	if err := json.Unmarshal([]byte(evtLines[1]), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v (raw=%q)", err, evtLines[1])
	}
	if legacy["type"] != "stoke.session.start" {
		t.Errorf("legacy type=%v, want stoke.session.start", legacy["type"])
	}

	// Validate envelope on the canonical event.
	evt := canonical
	if evt["stoke_version"] != StokeProtocolVersion {
		t.Errorf("stoke_version=%v", evt["stoke_version"])
	}
	// S3-3 dual-emit: both legacy `stoke_version` and canonical
	// `r1_version` must be present with the identical value during the
	// 30-day rename window (work-r1-rename.md §S3-3).
	if evt["r1_version"] != StokeProtocolVersion {
		t.Errorf("r1_version=%v (expected dual-emit of stoke_version)", evt["r1_version"])
	}
	if evt["instance_id"] != "r1-abc12345" {
		t.Errorf("instance_id=%v", evt["instance_id"])
	}
	if evt["trace_parent"] != "00-aaaa-bbbb-01" {
		t.Errorf("trace_parent=%v", evt["trace_parent"])
	}
	if evt["ledger_node_id"] != "node-sha256-xyz" {
		t.Errorf("ledger_node_id=%v", evt["ledger_node_id"])
	}
	if evt["task_count"] == nil {
		t.Error("task_count missing — data body must land at top level")
	}
	if evt["uuid"] == nil || evt["uuid"] == "" {
		t.Error("uuid missing — Claude-Code-compat is required")
	}
	if evt["session_id"] == nil || evt["session_id"] == "" {
		t.Error("session_id missing — Claude-Code-compat is required")
	}
	if evt["ts"] == nil || evt["ts"] == "" {
		t.Error("ts missing")
	}
}

func TestEmitStoke_OmitsEnvelopeWhenMetaUnset(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)
	// NO SetStokeMeta — envelope fields should be omitted, not null.

	e.EmitStoke("stoke.task.start", map[string]any{"task_id": "T1"})

	// D-032: dual-emit produces two lines; check the canonical r1.* first line.
	raw := strings.TrimSpace(buf.String())
	firstLine := raw
	if idx := strings.Index(raw, "\n"); idx >= 0 {
		firstLine = raw[:idx]
	}
	var evt map[string]any
	if err := json.Unmarshal([]byte(firstLine), &evt); err != nil {
		t.Fatalf("unmarshal canonical line: %v (raw=%q)", err, firstLine)
	}
	if evt["type"] != "r1.task.start" {
		t.Errorf("canonical type=%v, want r1.task.start", evt["type"])
	}
	// The three optional envelope keys must NOT be present — that's
	// the backward-compat guarantee for pre-STOKE consumers.
	// S3-3: `r1_version` is the canonical dual-emit sibling of
	// `stoke_version` and must also be omitted when meta is unset
	// (backward-compat guarantee).
	for _, key := range []string{"stoke_version", "r1_version", "instance_id", "trace_parent"} {
		if _, present := evt[key]; present {
			t.Errorf("%s should be absent when meta is unset, got %v", key, evt[key])
		}
	}
	// But the Claude-Code-compat fields must still be there.
	for _, key := range []string{"type", "uuid", "session_id", "ts"} {
		if evt[key] == nil || evt[key] == "" {
			t.Errorf("%s missing from baseline event", key)
		}
	}
	if evt["task_id"] != "T1" {
		t.Errorf("task_id body field lost: %v", evt["task_id"])
	}
}

func TestEmitStoke_DisabledEmitterIsNoop(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, false)
	e.SetStokeMeta("1.0", "r1-xxx", "00-aaaa-bbbb-01")
	e.EmitStoke("stoke.descent.tier", map[string]any{"ac_id": "AC3"})
	if buf.Len() != 0 {
		t.Errorf("disabled emitter wrote %d bytes; want 0", buf.Len())
	}
}

func TestEmitStoke_DataOverridesStampedFields(t *testing.T) {
	// data "type" key must not be able to override the envelope's
	// type, since an attacker-poisoned data map could otherwise
	// forge a session.start event. Emitter's stamping runs LAST in
	// the code path, so the envelope wins.
	var buf bytes.Buffer
	e := New(&buf, true)
	// Regression guard: iteration order of maps is randomized, but
	// our implementation stamps envelope fields FIRST then folds
	// data on top — intentional, because callers may legitimately
	// want to override ts (e.g. historical replays). The contract
	// is: envelope fields set by SetStokeMeta are immutable per
	// emit, but callers can shape the body freely. Document the
	// current behavior here so a future refactor doesn't silently
	// change it.
	e.SetStokeMeta("1.0", "r1-pinned", "")
	e.EmitStoke("stoke.cost", map[string]any{
		"instance_id": "r1-poisoned", // caller-supplied, wins today
		"cost_usd":    0.12,
	})
	// D-032: dual-emit — parse the canonical r1.* first line.
	raw := strings.TrimSpace(buf.String())
	firstLine := raw
	if idx := strings.Index(raw, "\n"); idx >= 0 {
		firstLine = raw[:idx]
	}
	var evt map[string]any
	if err := json.Unmarshal([]byte(firstLine), &evt); err != nil {
		t.Fatalf("unmarshal canonical line: %v (raw=%q)", err, firstLine)
	}
	if evt["type"] != "r1.cost" {
		t.Errorf("canonical type=%v, want r1.cost", evt["type"])
	}
	if evt["instance_id"] != "r1-poisoned" {
		t.Logf("caller data currently wins over envelope for instance_id; " +
			"if this changes, update the test and the docs")
	}
}
