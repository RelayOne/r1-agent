package plan

import (
	"strings"
	"testing"
)

// TestAutoDeduplicateTaskIDs_RenamesDuplicates covers the
// run-41 failure mode: refine passes emit the same counter-
// style task ID in two sessions, and ValidateSOW used to
// reject the whole SOW. Now the duplicates get renamed with
// a session-ID suffix.
func TestAutoDeduplicateTaskIDs_RenamesDuplicates(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{
				{ID: "T398", Description: "first"},
				{ID: "T399", Description: "second"},
			}},
			{ID: "S5", Tasks: []Task{
				{ID: "T398", Description: "third"},
				{ID: "T399", Description: "fourth"},
			}},
		},
	}
	autoDeduplicateTaskIDs(sow)

	if sow.Sessions[0].Tasks[0].ID != "T398" {
		t.Errorf("first occurrence should be kept, got %q", sow.Sessions[0].Tasks[0].ID)
	}
	if sow.Sessions[1].Tasks[0].ID != "T398-s5" {
		t.Errorf("duplicate should be renamed, got %q", sow.Sessions[1].Tasks[0].ID)
	}
	if sow.Sessions[1].Tasks[1].ID != "T399-s5" {
		t.Errorf("duplicate should be renamed, got %q", sow.Sessions[1].Tasks[1].ID)
	}
}

func TestAutoDeduplicateTaskIDs_UpdatesIntraSessionDeps(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{
				{ID: "T10", Description: "a"},
			}},
			{ID: "S2", Tasks: []Task{
				{ID: "T10", Description: "b", Dependencies: []string{"T10"}}, // self-ref nonsense but still
				{ID: "T11", Description: "c", Dependencies: []string{"T10"}}, // should rewrite to T10-s2
			}},
		},
	}
	autoDeduplicateTaskIDs(sow)

	// T11 was unique; its dep "T10" should now point to the
	// renamed T10-s2 (because T11 is in S2, where T10 got renamed).
	got := sow.Sessions[1].Tasks[1].Dependencies
	if len(got) != 1 || got[0] != "T10-s2" {
		t.Errorf("dep not rewritten: got %v, want [T10-s2]", got)
	}
}

func TestAutoDeduplicateTaskIDs_NoOpWhenUnique(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{{ID: "T1", Description: "a"}}},
			{ID: "S2", Tasks: []Task{{ID: "T2", Description: "b"}}},
		},
	}
	autoDeduplicateTaskIDs(sow)
	if sow.Sessions[0].Tasks[0].ID != "T1" || sow.Sessions[1].Tasks[0].ID != "T2" {
		t.Errorf("no-op should not change IDs, got %v / %v",
			sow.Sessions[0].Tasks[0].ID, sow.Sessions[1].Tasks[0].ID)
	}
}

func TestAutoDeduplicateTaskIDs_NilSafe(t *testing.T) {
	autoDeduplicateTaskIDs(nil)
	// Must not panic.
}

func TestAutoDeduplicateTaskIDs_EmptySessionID(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "", Tasks: []Task{{ID: "T1", Description: "a"}}},
			{ID: "", Tasks: []Task{{ID: "T1", Description: "b"}}},
		},
	}
	autoDeduplicateTaskIDs(sow)
	if sow.Sessions[1].Tasks[0].ID == "T1" {
		t.Errorf("duplicate still present when session IDs empty: %q", sow.Sessions[1].Tasks[0].ID)
	}
	if !strings.Contains(sow.Sessions[1].Tasks[0].ID, "sess") {
		t.Errorf("expected fallback sess<n> suffix, got %q", sow.Sessions[1].Tasks[0].ID)
	}
}

func TestAutoDeduplicateTaskIDs_TripleCollision(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{{ID: "T1", Description: "a"}}},
			{ID: "S2", Tasks: []Task{{ID: "T1", Description: "b"}}},
			{ID: "S2", Tasks: []Task{{ID: "T1", Description: "c"}}},
		},
	}
	autoDeduplicateTaskIDs(sow)
	// S1 keeps T1, S2 (first) becomes T1-s2, S2 (second) conflicts
	// with T1-s2 already seen, so it should disambiguate further.
	// It's actually two different session entries both with ID=S2;
	// the second dup gets T1-s2-2.
	if sow.Sessions[1].Tasks[0].ID != "T1-s2" {
		t.Errorf("second occurrence got %q want T1-s2", sow.Sessions[1].Tasks[0].ID)
	}
	if sow.Sessions[2].Tasks[0].ID != "T1-s2-2" {
		t.Errorf("third occurrence got %q want T1-s2-2", sow.Sessions[2].Tasks[0].ID)
	}
}

func TestAutoDeduplicateTaskIDs_RunsBeforeValidate(t *testing.T) {
	// Reproduces the run-41 failure: two sessions with the same
	// task IDs (T398/T399) — ValidateSOW rejects BEFORE dedup,
	// accepts AFTER dedup.
	sow := &SOW{
		ID:    "sow-test",
		Name:  "test",
		Stack: StackSpec{Language: "go"},
		Sessions: []Session{
			{
				ID:    "S1",
				Title: "first",
				Tasks: []Task{{ID: "T398", Description: "a"}},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC1", Description: "ok", Command: "true"},
				},
			},
			{
				ID:    "S2",
				Title: "second",
				Tasks: []Task{{ID: "T398", Description: "b"}},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC2", Description: "ok", Command: "true"},
				},
			},
		},
	}
	// Without dedup: validation fails.
	errsBefore := ValidateSOW(sow)
	foundDupErr := false
	for _, e := range errsBefore {
		if strings.Contains(e, "duplicate task ID across sessions") {
			foundDupErr = true
		}
	}
	if !foundDupErr {
		t.Fatalf("expected ValidateSOW to fail with dup-task-ID before dedup, got: %v", errsBefore)
	}
	autoDeduplicateTaskIDs(sow)
	errsAfter := ValidateSOW(sow)
	for _, e := range errsAfter {
		if strings.Contains(e, "duplicate task ID") {
			t.Errorf("post-dedup still has dup-task-ID error: %q", e)
		}
	}
}
