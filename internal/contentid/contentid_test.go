package contentid

import (
	"testing"
)

func TestDeterminism(t *testing.T) {
	a := New(PrefixTask, []byte("hello world"))
	b := New(PrefixTask, []byte("hello world"))
	if a != b {
		t.Fatalf("same content produced different IDs: %s vs %s", a, b)
	}
}

func TestDifferentContentDifferentID(t *testing.T) {
	a := New(PrefixTask, []byte("hello"))
	b := New(PrefixTask, []byte("world"))
	if a == b {
		t.Fatalf("different content produced same ID: %s", a)
	}
}

func TestNewFromString(t *testing.T) {
	a := New(PrefixDraft, []byte("test"))
	b := NewFromString(PrefixDraft, "test")
	if a != b {
		t.Fatalf("New and NewFromString differ: %s vs %s", a, b)
	}
}

func TestPrefixExtraction(t *testing.T) {
	id := NewFromString(PrefixEvent, "data")
	p := Prefix(id)
	if p != PrefixEvent {
		t.Fatalf("expected prefix %q, got %q", PrefixEvent, p)
	}
}

func TestHashExtraction(t *testing.T) {
	id := NewFromString(PrefixSnapshot, "snap-data")
	h := Hash(id)
	if len(h) != hashLen {
		t.Fatalf("expected hash length %d, got %d (%q)", hashLen, len(h), h)
	}
}

func TestValid(t *testing.T) {
	id := NewFromString(PrefixLoop, "content")
	if !Valid(id) {
		t.Fatalf("expected valid ID: %s", id)
	}
}

func TestValidRejectsGarbage(t *testing.T) {
	cases := []string{
		"",
		"not-an-id",
		"task-ZZZZZZZZZZZZ", // uppercase not hex
		"task-abc",          // too short
		"unknown-abcdef012345",
	}
	for _, c := range cases {
		if Valid(c) {
			t.Errorf("expected invalid for %q", c)
		}
	}
}

func TestAllPrefixes(t *testing.T) {
	prefixes := []string{
		PrefixDecisionInternal, PrefixDecisionRepo, PrefixTask, PrefixDraft,
		PrefixLoop, PrefixSkill, PrefixSnapshot, PrefixEvent,
		PrefixEscalation, PrefixResearch, PrefixAdvisory, PrefixAgree,
		PrefixDissent, PrefixJudge, PrefixStakeholder, PrefixSupervisor,
	}
	for _, p := range prefixes {
		id := NewFromString(p, "test")
		if !Valid(id) {
			t.Errorf("ID with prefix %q not valid: %s", p, id)
		}
		if Prefix(id) != p {
			t.Errorf("prefix mismatch for %s: got %q, want %q", id, Prefix(id), p)
		}
	}
}
