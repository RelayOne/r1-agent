// Package main — redaction_test.go
//
// Spec 3 §10 T1 — eight-test coverage for RedactionMap helpers.
package main

import (
	"strings"
	"testing"
)

func TestRedactionMap_NilReceiverIsSafe(t *testing.T) {
	var m *RedactionMap
	if m.IsRedacted("x") {
		t.Errorf("nil RedactionMap.IsRedacted should be false")
	}
	if got := m.Events("x"); got != nil {
		t.Errorf("nil RedactionMap.Events should be nil, got %v", got)
	}
	if m.IsAnomaly("x") {
		t.Errorf("nil RedactionMap.IsAnomaly should be false")
	}
}

func TestRedactionMap_EmptyMapHasNoRedactions(t *testing.T) {
	m := &RedactionMap{byNode: map[string][]RedactionEvent{}}
	if m.IsRedacted("any") {
		t.Errorf("empty map should not flag any node redacted")
	}
}

func TestRedactionMap_PresentEntryIsRedacted(t *testing.T) {
	m := &RedactionMap{byNode: map[string][]RedactionEvent{
		"n-1": {{NodeID: "n-1", Reason: "retention_policy"}},
	}}
	if !m.IsRedacted("n-1") {
		t.Errorf("n-1 should be redacted")
	}
	if m.IsRedacted("n-2") {
		t.Errorf("n-2 should not be redacted")
	}
}

func TestRedactionMap_AnomalyCase_RedactedNoEvents(t *testing.T) {
	m := &RedactionMap{byNode: map[string][]RedactionEvent{
		"n-mystery": {},
	}}
	if !m.IsRedacted("n-mystery") {
		t.Errorf("redaction-without-event-log should still mark IsRedacted=true")
	}
	if !m.IsAnomaly("n-mystery") {
		t.Errorf("redaction-without-event-log should be the anomaly case")
	}
}

func TestRedactionMap_NonAnomaly_HasEvents(t *testing.T) {
	m := &RedactionMap{byNode: map[string][]RedactionEvent{
		"n-1": {{NodeID: "n-1", Reason: "retention_policy", HumanReason: "redacted by retention policy"}},
	}}
	if m.IsAnomaly("n-1") {
		t.Errorf("redaction with events is NOT the anomaly case")
	}
}

func TestRedactionMap_MultipleEventsForOneNode(t *testing.T) {
	m := &RedactionMap{byNode: map[string][]RedactionEvent{
		"n-1": {
			{NodeID: "n-1", Reason: "retention_policy"},
			{NodeID: "n-1", Reason: "gdpr_erasure"},
		},
	}}
	got := m.Events("n-1")
	if len(got) != 2 {
		t.Errorf("got %d events, want 2", len(got))
	}
}

func TestRedactionMap_WithEvents_OverridesBaseEntries(t *testing.T) {
	m := &RedactionMap{byNode: map[string][]RedactionEvent{
		"n-1": {},
	}}
	added := map[string][]RedactionEvent{
		"n-1": {{NodeID: "n-1", Reason: "operator_request"}},
		"n-2": {{NodeID: "n-2", Reason: "retention_policy"}},
	}
	m.WithEvents(added)
	if len(m.Events("n-1")) != 1 || m.Events("n-1")[0].Reason != "operator_request" {
		t.Errorf("WithEvents should replace n-1 events")
	}
	if !m.IsRedacted("n-2") {
		t.Errorf("WithEvents should add n-2 to the map")
	}
}

func TestHumanReason_KnownReasonsMapToUserCopy(t *testing.T) {
	cases := map[string]string{
		"":                "(reason unknown)",
		"retention_policy": "redacted by retention policy",
		"gdpr_erasure":     "redacted under GDPR right-to-erasure",
		"operator_request": "redacted by operator request",
		"weird_internal":   "redacted: weird_internal",
	}
	for in, want := range cases {
		if got := HumanReason(in); got != want {
			t.Errorf("HumanReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanReason_NeverEchoesBareWordRedacted(t *testing.T) {
	bad := []string{"redacted", "removed", "deleted", "hidden"}
	for _, r := range bad {
		got := HumanReason(r)
		if got == r {
			t.Errorf("HumanReason(%q) echoed the raw string — RT-REDACTION-UI-PATTERNS forbids this", r)
		}
		if !strings.Contains(got, "redacted") {
			t.Errorf("HumanReason(%q) = %q — should still convey \"redacted\" semantically", r, got)
		}
	}
}
