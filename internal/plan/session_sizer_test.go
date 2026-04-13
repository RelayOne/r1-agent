package plan

import (
	"testing"
)

func TestApplySessionSplit_ValidSplit(t *testing.T) {
	parent := Session{
		ID:    "S2",
		Title: "Shared Packages",
		Tasks: []Task{
			{ID: "t1", Description: "types core"},
			{ID: "t2", Description: "types user"},
			{ID: "t3", Description: "api client core"},
			{ID: "t4", Description: "api client auth"},
		},
		AcceptanceCriteria: []AcceptanceCriterion{
			{ID: "ac-types", Description: "types package compiles"},
			{ID: "ac-api", Description: "api-client compiles"},
			{ID: "ac-mono", Description: "monorepo builds"},
		},
	}
	split := SessionSplit{
		ShouldSplit: true,
		Reasoning:   "two disjoint packages",
		SubSessions: []SessionSplitPart{
			{SuffixID: "types", Title: "Types", TaskIDs: []string{"t1", "t2"}, AcceptanceCriteriaIDs: []string{"ac-types"}},
			{SuffixID: "api-client", Title: "API Client", TaskIDs: []string{"t3", "t4"}, AcceptanceCriteriaIDs: []string{"ac-api"}},
		},
	}
	subs, err := ApplySessionSplit(parent, split)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 sub-sessions, got %d", len(subs))
	}
	if subs[0].ID != "S2-types" {
		t.Errorf("expected S2-types, got %s", subs[0].ID)
	}
	if subs[1].ID != "S2-api-client" {
		t.Errorf("expected S2-api-client, got %s", subs[1].ID)
	}
	if len(subs[0].Tasks) != 2 || len(subs[1].Tasks) != 2 {
		t.Errorf("tasks not distributed evenly: %d / %d", len(subs[0].Tasks), len(subs[1].Tasks))
	}
	// Unallocated AC "ac-mono" must carry to last sub-session.
	foundMono := false
	for _, ac := range subs[1].AcceptanceCriteria {
		if ac.ID == "ac-mono" {
			foundMono = true
			break
		}
	}
	if !foundMono {
		t.Errorf("unallocated AC ac-mono should carry to last sub-session; got %+v", subs[1].AcceptanceCriteria)
	}
}

func TestApplySessionSplit_MissingTask(t *testing.T) {
	parent := Session{
		ID: "S1",
		Tasks: []Task{
			{ID: "t1"}, {ID: "t2"}, {ID: "t3"},
		},
	}
	split := SessionSplit{
		ShouldSplit: true,
		SubSessions: []SessionSplitPart{
			{SuffixID: "a", TaskIDs: []string{"t1"}},
			{SuffixID: "b", TaskIDs: []string{"t2"}},
		},
	}
	if _, err := ApplySessionSplit(parent, split); err == nil {
		t.Fatal("expected error for missing task, got nil")
	}
}

func TestApplySessionSplit_DuplicateTask(t *testing.T) {
	parent := Session{
		ID: "S1",
		Tasks: []Task{
			{ID: "t1"}, {ID: "t2"},
		},
	}
	split := SessionSplit{
		ShouldSplit: true,
		SubSessions: []SessionSplitPart{
			{SuffixID: "a", TaskIDs: []string{"t1", "t2"}},
			{SuffixID: "b", TaskIDs: []string{"t1"}},
		},
	}
	if _, err := ApplySessionSplit(parent, split); err == nil {
		t.Fatal("expected error for duplicated task, got nil")
	}
}

func TestApplySessionSplit_UnknownTask(t *testing.T) {
	parent := Session{
		ID:    "S1",
		Tasks: []Task{{ID: "t1"}},
	}
	split := SessionSplit{
		ShouldSplit: true,
		SubSessions: []SessionSplitPart{
			{SuffixID: "a", TaskIDs: []string{"t1", "t-ghost"}},
		},
	}
	if _, err := ApplySessionSplit(parent, split); err == nil {
		t.Fatal("expected error for unknown task reference, got nil")
	}
}

func TestApplySessionSplit_EmptySuffix(t *testing.T) {
	parent := Session{ID: "S1", Tasks: []Task{{ID: "t1"}}}
	split := SessionSplit{
		ShouldSplit: true,
		SubSessions: []SessionSplitPart{
			{SuffixID: "", TaskIDs: []string{"t1"}},
		},
	}
	if _, err := ApplySessionSplit(parent, split); err == nil {
		t.Fatal("expected error for empty suffix_id, got nil")
	}
}

func TestApplySessionSplit_DuplicateSuffix(t *testing.T) {
	parent := Session{ID: "S1", Tasks: []Task{{ID: "t1"}, {ID: "t2"}}}
	split := SessionSplit{
		ShouldSplit: true,
		SubSessions: []SessionSplitPart{
			{SuffixID: "x", TaskIDs: []string{"t1"}},
			{SuffixID: "x", TaskIDs: []string{"t2"}},
		},
	}
	if _, err := ApplySessionSplit(parent, split); err == nil {
		t.Fatal("expected error for duplicate suffix, got nil")
	}
}

func TestApplySessionSplit_NotActionable(t *testing.T) {
	parent := Session{ID: "S1", Tasks: []Task{{ID: "t1"}}}
	if _, err := ApplySessionSplit(parent, SessionSplit{ShouldSplit: false}); err == nil {
		t.Error("expected error when ShouldSplit=false")
	}
	if _, err := ApplySessionSplit(parent, SessionSplit{ShouldSplit: true}); err == nil {
		t.Error("expected error when SubSessions is empty")
	}
}

func TestJudgeSessionSize_NilProviderNoop(t *testing.T) {
	// Spec: returns nil+nil when prov is nil.
	got, err := JudgeSessionSize(nil, nil, "", SessionSizerInput{
		Session: Session{ID: "S1", Tasks: make([]Task, 12)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil split for nil provider, got %+v", got)
	}
}

func TestJudgeSessionSize_SmallSessionNoop(t *testing.T) {
	// Spec: returns nil+nil when session has <6 tasks.
	got, err := JudgeSessionSize(nil, nil, "", SessionSizerInput{
		Session: Session{ID: "S1", Tasks: make([]Task, 3)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil split for small session, got %+v", got)
	}
}
