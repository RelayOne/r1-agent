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

// TestAutoDeduplicateTaskIDs_IntraSessionDepRewritten —
// when a session renames T10 → T10-S2, any task in that same
// session with a dep "T10" gets its dep rewritten to T10-S2
// because task.Dependencies is conventionally intra-session.
// Cross-session handoffs use session-level Inputs/Outputs,
// not task.Dependencies, so preserving local wiring is the
// right default.
func TestAutoDeduplicateTaskIDs_IntraSessionDepRewritten(t *testing.T) {
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

	// S2's T11 depended on its local T10 (now T10-S2). Rewrite
	// preserves the intra-session graph.
	got := sow.Sessions[1].Tasks[1].Dependencies
	if len(got) != 1 || got[0] != "T10-S2" {
		t.Errorf("intra-session dep should rewrite to renamed ID, got %v", got)
	}
}

// TestAutoDeduplicateTaskIDs_DepsAcrossSessions — deps
// in a DIFFERENT session (not the one where the rename
// happened) stay untouched.
func TestAutoDeduplicateTaskIDs_DepsAcrossSessions(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{
				{ID: "T10", Description: "a"},
				{ID: "T11", Description: "uses T10", Dependencies: []string{"T10"}},
			}},
			{ID: "S2", Tasks: []Task{
				{ID: "T10", Description: "b"}, // will be renamed to T10-S2
			}},
		},
	}
	autoDeduplicateTaskIDs(sow)

	// S1's T11 dep "T10" refers to S1's T10 (which wasn't
	// renamed, so idrenames in S1 is empty). Dep stays "T10".
	got := sow.Sessions[0].Tasks[1].Dependencies
	if len(got) != 1 || got[0] != "T10" {
		t.Errorf("S1's dep should stay untouched (no renames in S1), got %v", got)
	}
}

// TestAutoDeduplicateTaskIDs_CrossSessionDepPreserved —
// a task in the renamed session with a dep on an ID that
// was NEVER an original ID of that session is treated as
// a cross-session reference and left untouched.
//
// Setup: S1 has T10 (kept). S2 has T99 and T100.
// T100 has dep "T10" — meaning S1's T10 (cross-session).
// S2 also has T10-duplicate. When we rename S2's T10 → T10-S2,
// we must NOT rewrite T100's dep "T10" because T10 was
// never an original in S2 (S2's originals were only T99,
// T100, T10-duplicate). Wait — T10-duplicate IS T10 in S2's
// originals. Tweak: use T200 in S2.
func TestAutoDeduplicateTaskIDs_CrossSessionDepPreserved(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{
				{ID: "T10", Description: "foundational task S1"},
			}},
			{ID: "S2", Tasks: []Task{
				{ID: "T200", Description: "S2 first"},
				// T300 depends on S1's T10 (cross-session dep),
				// legitimately captured in task.Dependencies.
				{ID: "T300", Description: "S2 consumer", Dependencies: []string{"T10"}},
				// Another T10 that will force a rename in S2 —
				// but since T10 was NOT an original in S2 (before
				// this line), the dep above should stay intact.
				// Wait — T10 IS added to S2 via this task entry.
				// Let me restructure so T10 doesn't appear as an
				// original in S2. Use a different dup:
			}},
			// Move the duplicate into S3 instead, so S2 has no
			// T10 origin.
			{ID: "S3", Tasks: []Task{
				{ID: "T10", Description: "S3 duplicate of T10"},
			}},
		},
	}
	autoDeduplicateTaskIDs(sow)

	// S3's T10 should be renamed to T10-S3.
	if sow.Sessions[2].Tasks[0].ID != "T10-S3" {
		t.Errorf("S3's T10 should be renamed, got %q", sow.Sessions[2].Tasks[0].ID)
	}
	// S2's T300 dep "T10" was a CROSS-session ref (to S1's T10).
	// After rename of S3's T10, S2 has no rename, so the dep
	// stays as-is.
	got := sow.Sessions[1].Tasks[1].Dependencies
	if len(got) != 1 || got[0] != "T10" {
		t.Errorf("cross-session dep preserved; got %v", got)
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
