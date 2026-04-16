package plan

import (
	"testing"
)

// TestSpliceDroppedIDs_RestoresTasks: refiner drops task T290
// from session S5 during a refine pass. spliceDroppedIDs must
// restore T290 into the refined S5 so the conservation gate
// doesn't reject the whole refinement.
func TestSpliceDroppedIDs_RestoresTasks(t *testing.T) {
	original := &SOW{
		Sessions: []Session{
			{ID: "S5", Tasks: []Task{
				{ID: "T289", Description: "keep"},
				{ID: "T290", Description: "dropped by refiner"},
				{ID: "T291", Description: "keep"},
			}},
		},
	}
	refined := &SOW{
		Sessions: []Session{
			{ID: "S5", Tasks: []Task{
				{ID: "T289", Description: "keep"},
				{ID: "T291", Description: "keep"},
			}},
		},
	}
	spliceDroppedIDs(original, refined, []string{"T290"}, nil)

	var foundT290 *Task
	for i, task := range refined.Sessions[0].Tasks {
		if task.ID == "T290" {
			foundT290 = &refined.Sessions[0].Tasks[i]
			break
		}
	}
	if foundT290 == nil {
		t.Fatal("T290 not restored to S5")
	}
	if foundT290.Description != "dropped by refiner" {
		t.Errorf("restored task has wrong description: %q", foundT290.Description)
	}
}

// TestSpliceDroppedIDs_RestoresACs: same behavior for
// acceptance criteria.
func TestSpliceDroppedIDs_RestoresACs(t *testing.T) {
	original := &SOW{
		Sessions: []Session{
			{ID: "S1", AcceptanceCriteria: []AcceptanceCriterion{
				{ID: "AC1", Description: "keep", Command: "true"},
				{ID: "AC2", Description: "dropped", Command: "echo"},
			}},
		},
	}
	refined := &SOW{
		Sessions: []Session{
			{ID: "S1", AcceptanceCriteria: []AcceptanceCriterion{
				{ID: "AC1", Description: "keep", Command: "true"},
			}},
		},
	}
	spliceDroppedIDs(original, refined, nil, []string{"AC2"})

	if len(refined.Sessions[0].AcceptanceCriteria) != 2 {
		t.Fatalf("expected 2 ACs after splice, got %d", len(refined.Sessions[0].AcceptanceCriteria))
	}
}

// TestSpliceDroppedIDs_FallbackToFirstSession: when the
// original session that owned the dropped task no longer
// exists in the refined SOW (refiner removed/renamed the
// session), the task goes into refined.Sessions[0].
func TestSpliceDroppedIDs_FallbackToFirstSession(t *testing.T) {
	original := &SOW{
		Sessions: []Session{
			{ID: "S-removed", Tasks: []Task{
				{ID: "T99", Description: "homeless task"},
			}},
		},
	}
	refined := &SOW{
		Sessions: []Session{
			{ID: "S-new", Tasks: []Task{
				{ID: "T1", Description: "refined"},
			}},
		},
	}
	spliceDroppedIDs(original, refined, []string{"T99"}, nil)

	found := false
	for _, task := range refined.Sessions[0].Tasks {
		if task.ID == "T99" {
			found = true
		}
	}
	if !found {
		t.Error("T99 should have landed in S-new (fallback to Sessions[0])")
	}
}

func TestSpliceDroppedIDs_NoOpOnNil(t *testing.T) {
	spliceDroppedIDs(nil, nil, []string{"T1"}, nil)
	spliceDroppedIDs(&SOW{}, &SOW{}, []string{"T1"}, nil)
	// Must not panic.
}

// TestSpliceDroppedIDs_EmptyRefinedNoOp: if the refiner
// produced zero sessions there's nowhere to splice; the
// function quietly no-ops (caller still has the original
// available and surfaces via a warning).
func TestSpliceDroppedIDs_EmptyRefinedNoOp(t *testing.T) {
	original := &SOW{
		Sessions: []Session{{ID: "S1", Tasks: []Task{{ID: "T1", Description: "x"}}}},
	}
	refined := &SOW{Sessions: nil}
	spliceDroppedIDs(original, refined, []string{"T1"}, nil)
	if len(refined.Sessions) != 0 {
		t.Errorf("expected refined.Sessions unchanged; got %d", len(refined.Sessions))
	}
}

// TestSpliceDroppedIDs_RenamedSessionPrefixMatch: refiner
// split S5 into S5-api and S5-ui. The original's S5 task
// T290 should land in the closest prefix match (S5-api,
// alphabetically first) rather than being dumped into an
// unrelated Sessions[0].
func TestSpliceDroppedIDs_RenamedSessionPrefixMatch(t *testing.T) {
	original := &SOW{
		Sessions: []Session{
			{ID: "S5", Tasks: []Task{
				{ID: "T290", Description: "implement handler for refund endpoint"},
			}},
		},
	}
	refined := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{{ID: "T1", Description: "init project"}}},
			{ID: "S5-api", Tasks: []Task{{ID: "T200", Description: "api split"}}},
			{ID: "S5-ui", Tasks: []Task{{ID: "T210", Description: "ui split"}}},
		},
	}
	spliceDroppedIDs(original, refined, []string{"T290"}, nil)

	// T290 should land in S5-api (first prefix match for "S5"),
	// NOT in S1.
	found := false
	for _, task := range refined.Sessions[1].Tasks {
		if task.ID == "T290" {
			found = true
		}
	}
	if !found {
		t.Error("T290 should be in S5-api (prefix match), not S1")
	}
	for _, task := range refined.Sessions[0].Tasks {
		if task.ID == "T290" {
			t.Error("T290 should NOT be in S1 when S5-api exists")
		}
	}
}

// TestSpliceDroppedIDs_SkipsRenamedTasks: refiner renamed
// T290 → T290-new but kept the same description. The "missing
// T290" signal is a false alarm — splice must NOT re-add the
// original or we end up with both the stale T290 and the new
// T290-new pointing at duplicated content.
func TestSpliceDroppedIDs_SkipsRenamedTasks(t *testing.T) {
	original := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{
				{ID: "T290", Description: "implement the refund webhook handler"},
			}},
		},
	}
	refined := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{
				// Same description, different ID (refiner rename).
				{ID: "T290-new", Description: "implement the refund webhook handler"},
			}},
		},
	}
	spliceDroppedIDs(original, refined, []string{"T290"}, nil)

	// refined should still have exactly 1 task — the rename.
	// If we restored T290, we'd have 2 duplicated tasks.
	if len(refined.Sessions[0].Tasks) != 1 {
		t.Errorf("splice should detect rename and skip; got %d tasks", len(refined.Sessions[0].Tasks))
		for _, task := range refined.Sessions[0].Tasks {
			t.Logf("  %s: %q", task.ID, task.Description)
		}
	}
}

// TestSpliceDroppedIDs_SkipsRenamedACsByPayload: same
// rename-detection logic on acceptance criteria, keyed on
// the verifier payload (command / ContentMatch fields)
// rather than description.
func TestSpliceDroppedIDs_SkipsRenamedACsByPayload(t *testing.T) {
	original := &SOW{
		Sessions: []Session{
			{ID: "S1", AcceptanceCriteria: []AcceptanceCriterion{
				{ID: "AC1", Description: "build passes", Command: "go build ./..."},
			}},
		},
	}
	refined := &SOW{
		Sessions: []Session{
			{ID: "S1", AcceptanceCriteria: []AcceptanceCriterion{
				{ID: "AC99", Description: "build passes", Command: "go build ./..."},
			}},
		},
	}
	spliceDroppedIDs(original, refined, nil, []string{"AC1"})

	if len(refined.Sessions[0].AcceptanceCriteria) != 1 {
		t.Errorf("splice should detect AC rename via command match; got %d ACs",
			len(refined.Sessions[0].AcceptanceCriteria))
	}
}

// TestSpliceDroppedIDs_ShortDescNotTreatedAsRename: tasks
// with short descriptions (< 8 chars) shouldn't match by
// description alone — the signal is too weak. Falls back to
// ID-only handling.
func TestSpliceDroppedIDs_ShortDescNotTreatedAsRename(t *testing.T) {
	original := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{
				{ID: "T1", Description: "init"},
			}},
		},
	}
	refined := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{
				{ID: "T2", Description: "init"},
			}},
		},
	}
	spliceDroppedIDs(original, refined, []string{"T1"}, nil)

	// Short desc ("init" = 4 chars) is NOT a reliable rename
	// signal, so T1 gets restored. Result: 2 tasks.
	if len(refined.Sessions[0].Tasks) != 2 {
		t.Errorf("short-desc should NOT be treated as rename; got %d tasks",
			len(refined.Sessions[0].Tasks))
	}
}
