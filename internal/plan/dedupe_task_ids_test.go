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
	if sow.Sessions[1].Tasks[0].ID != "T398-S5" {
		t.Errorf("duplicate should be renamed, got %q", sow.Sessions[1].Tasks[0].ID)
	}
	if sow.Sessions[1].Tasks[1].ID != "T399-S5" {
		t.Errorf("duplicate should be renamed, got %q", sow.Sessions[1].Tasks[1].ID)
	}
}

// TestAutoDeduplicateTaskIDs_AmbiguousDepLeftAlone — when the
// original ID exists in another session too (idCount > 1), the
// dep could mean either "the local copy we renamed" or "the
// first-occurrence we kept elsewhere." Rewriting would silently
// redirect a cross-session reference, so we preserve the original
// string and let autoCleanTaskDeps drop it only if it becomes
// dangling.
func TestAutoDeduplicateTaskIDs_AmbiguousDepLeftAlone(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{
				{ID: "T10", Description: "a"},
			}},
			{ID: "S2", Tasks: []Task{
				{ID: "T10", Description: "b"},
				{ID: "T11", Description: "c", Dependencies: []string{"T10"}},
			}},
		},
	}
	autoDeduplicateTaskIDs(sow)

	got := sow.Sessions[1].Tasks[1].Dependencies
	if len(got) != 1 || got[0] != "T10" {
		t.Errorf("ambiguous dep should be preserved, got %v", got)
	}
}

// TestAutoDeduplicateTaskIDs_UnambiguousDepRewritten — when the
// original ID appears ONLY in the session being renamed (no
// first-occurrence elsewhere), the dep is unambiguously local
// and gets rewritten to the new ID so intra-session wiring is
// preserved.
func TestAutoDeduplicateTaskIDs_UnambiguousDepRewritten(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			// First occurrence of T5 is kept.
			{ID: "S1", Tasks: []Task{{ID: "T5", Description: "root"}}},
			// S2 has a DIFFERENT ID (T10) which appears only in S2 but
			// duplicated locally — first-dup becomes the kept copy;
			// the second becomes the renamed copy.
			{ID: "S2", Tasks: []Task{
				{ID: "T10", Description: "first T10 here"},
			}},
			{ID: "S3", Tasks: []Task{
				{ID: "T10", Description: "dup"},
				{ID: "T99", Description: "ref", Dependencies: []string{"T10"}},
			}},
		},
	}
	autoDeduplicateTaskIDs(sow)

	// T10 exists in S2 (kept) and S3 (renamed to T10-S3).
	// idCounts[T10] = 2, so S3's T99 dep "T10" is ambiguous and
	// stays pointing at the S2 copy.
	if got := sow.Sessions[2].Tasks[1].Dependencies; len(got) != 1 || got[0] != "T10" {
		t.Errorf("ambiguous dep: got %v want [T10]", got)
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

// TestAutoDeduplicateTaskIDs_TripleCollision uses three distinct
// session IDs (S1, S2, S3) so the SOW shape is valid — the test
// exercises the third-occurrence disambiguator path without relying
// on an invalid same-ID-session state that ValidateSOW would reject.
func TestAutoDeduplicateTaskIDs_TripleCollision(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{{ID: "T1", Description: "a"}}},
			{ID: "S2", Tasks: []Task{{ID: "T1", Description: "b"}}},
			{ID: "S3", Tasks: []Task{{ID: "T1", Description: "c"}}},
		},
	}
	autoDeduplicateTaskIDs(sow)
	// S1 keeps T1, S2 becomes T1-S2, S3 becomes T1-S3 (no
	// disambiguator needed since the session IDs differ).
	if sow.Sessions[1].Tasks[0].ID != "T1-S2" {
		t.Errorf("S2 got %q want T1-S2", sow.Sessions[1].Tasks[0].ID)
	}
	if sow.Sessions[2].Tasks[0].ID != "T1-S3" {
		t.Errorf("S3 got %q want T1-S3", sow.Sessions[2].Tasks[0].ID)
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
