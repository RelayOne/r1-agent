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
