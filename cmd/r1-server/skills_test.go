// Package main — skills_test.go
//
// Spec 3 §10 T2 — covers the (load → unload) lifecycle predicates.
package main

import (
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestSkillEventMap_NeverLoaded(t *testing.T) {
	m := &SkillEventMap{byStanceSkill: map[stanceSkillKey][]SkillEvent{}}
	if m.IsActiveAt("st-1", "sk-x", ts("2026-05-05T00:00:00Z")) {
		t.Errorf("never-loaded skill should not be active")
	}
}

func TestSkillEventMap_LoadedThenUnloaded(t *testing.T) {
	m := &SkillEventMap{byStanceSkill: map[stanceSkillKey][]SkillEvent{
		{"st-1", "sk-x"}: {
			{Type: "skill_loaded", StanceID: "st-1", SkillRef: "sk-x", At: ts("2026-05-05T10:00:00Z")},
			{Type: "skill_unloaded", StanceID: "st-1", SkillRef: "sk-x", At: ts("2026-05-05T11:00:00Z"), Reason: "compactor_evicted"},
		},
	}}
	cases := []struct {
		at       string
		expected bool
	}{
		{"2026-05-05T09:59:00Z", false}, // before load
		{"2026-05-05T10:00:00Z", true},  // exactly at load (≤ t boundary inclusive)
		{"2026-05-05T10:30:00Z", true},  // active window
		{"2026-05-05T11:00:00Z", false}, // exactly at unload
		{"2026-05-05T12:00:00Z", false}, // after unload
	}
	for _, c := range cases {
		got := m.IsActiveAt("st-1", "sk-x", ts(c.at))
		if got != c.expected {
			t.Errorf("IsActiveAt(%s) = %v, want %v", c.at, got, c.expected)
		}
	}
}

func TestSkillEventMap_DoubleLoadNoUnloadIsStillActive(t *testing.T) {
	// Edge case: skill reloaded in same stance (shouldn't typically
	// happen, but must not flip active-state to false).
	m := &SkillEventMap{byStanceSkill: map[stanceSkillKey][]SkillEvent{
		{"st-1", "sk-x"}: {
			{Type: "skill_loaded", StanceID: "st-1", SkillRef: "sk-x", At: ts("2026-05-05T10:00:00Z")},
			{Type: "skill_loaded", StanceID: "st-1", SkillRef: "sk-x", At: ts("2026-05-05T10:30:00Z")},
		},
	}}
	if !m.IsActiveAt("st-1", "sk-x", ts("2026-05-05T11:00:00Z")) {
		t.Errorf("double-load should leave skill active")
	}
}

func TestSkillEventMap_DifferentStancesIndependent(t *testing.T) {
	m := &SkillEventMap{byStanceSkill: map[stanceSkillKey][]SkillEvent{
		{"st-1", "sk-x"}: {
			{Type: "skill_loaded", StanceID: "st-1", SkillRef: "sk-x", At: ts("2026-05-05T10:00:00Z")},
		},
	}}
	if m.IsActiveAt("st-2", "sk-x", ts("2026-05-05T11:00:00Z")) {
		t.Errorf("st-2 should NOT be considered active just because st-1 loaded sk-x")
	}
}

func TestSkillEventMap_ScopeExitEvictedReason(t *testing.T) {
	m := &SkillEventMap{byStanceSkill: map[stanceSkillKey][]SkillEvent{
		{"st-1", "sk-x"}: {
			{Type: "skill_loaded", StanceID: "st-1", SkillRef: "sk-x", At: ts("2026-05-05T10:00:00Z")},
			{Type: "skill_unloaded", StanceID: "st-1", SkillRef: "sk-x", At: ts("2026-05-05T10:30:00Z"), Reason: "scope_exit"},
		},
	}}
	events := m.EventsForStance("st-1", "sk-x")
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[1].Reason != "scope_exit" {
		t.Errorf("expected scope_exit reason on unload event")
	}
}

func TestSkillEventMap_EventsBySkill_AcrossStances(t *testing.T) {
	m := &SkillEventMap{byStanceSkill: map[stanceSkillKey][]SkillEvent{
		{"st-1", "sk-x"}: {
			{Type: "skill_loaded", StanceID: "st-1", SkillRef: "sk-x", At: ts("2026-05-05T10:00:00Z")},
		},
		{"st-2", "sk-x"}: {
			{Type: "skill_loaded", StanceID: "st-2", SkillRef: "sk-x", At: ts("2026-05-05T10:30:00Z")},
		},
		{"st-1", "sk-y"}: {
			{Type: "skill_loaded", StanceID: "st-1", SkillRef: "sk-y", At: ts("2026-05-05T10:15:00Z")},
		},
	}}
	got := m.EventsBySkill("sk-x")
	if len(got) != 2 {
		t.Fatalf("got %d sk-x events, want 2", len(got))
	}
	if !got[0].At.Before(got[1].At) {
		t.Errorf("EventsBySkill must return chronological order")
	}
	if got[0].StanceID != "st-1" || got[1].StanceID != "st-2" {
		t.Errorf("unexpected stance order: %+v", got)
	}
}

func TestHumanSkillReason_KnownAndFallback(t *testing.T) {
	cases := map[string]string{
		"":                  "(reason unknown)",
		"compactor_evicted": "evicted by context compactor (budget pressure)",
		"scope_exit":        "scope exited (task DAG closed)",
		"explicit_unload":   "unloaded by operator",
		"weird":             "unloaded: weird",
	}
	for in, want := range cases {
		if got := HumanSkillReason(in); got != want {
			t.Errorf("HumanSkillReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkillEventMap_NilReceiverIsSafe(t *testing.T) {
	var m *SkillEventMap
	if m.IsActiveAt("st", "sk", ts("2026-05-05T10:00:00Z")) {
		t.Errorf("nil map should not flag active")
	}
	if m.EventsForStance("st", "sk") != nil {
		t.Errorf("nil map should return nil events")
	}
	if m.EventsBySkill("sk") != nil {
		t.Errorf("nil map should return nil for EventsBySkill")
	}
}
