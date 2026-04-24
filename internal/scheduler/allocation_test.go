package scheduler

import (
	"testing"

	"github.com/RelayOne/r1/internal/plan"
)

func TestScoreAllocationRoleMatch(t *testing.T) {
	w := Worker{ID: "w1", Roles: []string{"executor"}, Expertise: nil}
	task := plan.Task{ID: "t1", Type: "refactor"}
	weights := DefaultWeights()

	score := ScoreAllocation(w, task, weights)
	if score.Score < weights.RoleMatch-1 {
		t.Errorf("expected score >= %f for role match, got %f", weights.RoleMatch, score.Score)
	}
}

func TestScoreAllocationRoleMismatch(t *testing.T) {
	w := Worker{ID: "w1", Roles: []string{"security"}}
	task := plan.Task{ID: "t1", Type: "docs"}
	weights := DefaultWeights()

	score := ScoreAllocation(w, task, weights)
	if score.Score >= 0 {
		t.Errorf("expected negative score for mismatch, got %f", score.Score)
	}
}

func TestScoreAllocationExpertiseBonus(t *testing.T) {
	w := Worker{ID: "w1", Roles: []string{"executor"}, Expertise: []string{"internal/auth/"}}
	task := plan.Task{ID: "t1", Type: "refactor", Files: []string{"internal/auth/handler.go"}}
	weights := DefaultWeights()

	score := ScoreAllocation(w, task, weights)
	// Should have role match + expertise match
	expected := weights.RoleMatch + weights.ExpertiseMatch
	if score.Score < expected-1 {
		t.Errorf("expected score >= %f, got %f", expected, score.Score)
	}
}

func TestScoreAllocationWorkloadPenalty(t *testing.T) {
	idle := Worker{ID: "w1", Roles: []string{"executor"}, ActiveLoad: 0}
	busy := Worker{ID: "w2", Roles: []string{"executor"}, ActiveLoad: 3}
	task := plan.Task{ID: "t1", Type: "refactor"}
	weights := DefaultWeights()

	s1 := ScoreAllocation(idle, task, weights)
	s2 := ScoreAllocation(busy, task, weights)

	if s2.Score >= s1.Score {
		t.Errorf("busy worker should score lower: idle=%f, busy=%f", s1.Score, s2.Score)
	}
}

func TestBestWorker(t *testing.T) {
	workers := []Worker{
		{ID: "w1", Roles: []string{"security"}, ActiveLoad: 0},
		{ID: "w2", Roles: []string{"executor"}, ActiveLoad: 0},
		{ID: "w3", Roles: []string{"executor"}, ActiveLoad: 2},
	}
	task := plan.Task{ID: "t1", Type: "refactor"}

	best, score := BestWorker(workers, task, DefaultWeights())
	if best != "w2" {
		t.Errorf("expected w2 (idle executor), got %s (score: %f, reason: %s)", best, score.Score, score.Reason)
	}
}

func TestBestWorkerEmpty(t *testing.T) {
	best, _ := BestWorker(nil, plan.Task{ID: "t1"}, DefaultWeights())
	if best != "" {
		t.Error("expected empty for no workers")
	}
}

func TestAssignTasks(t *testing.T) {
	workers := []Worker{
		{ID: "w1", Roles: []string{"executor"}, ActiveLoad: 0},
		{ID: "w2", Roles: []string{"security"}, ActiveLoad: 0},
	}
	tasks := []plan.Task{
		{ID: "t1", Type: "refactor"},
		{ID: "t2", Type: "security"},
		{ID: "t3", Type: "refactor"},
	}

	assignments := AssignTasks(workers, tasks, DefaultWeights())
	if len(assignments) != 3 {
		t.Fatalf("expected 3 assignments, got %d", len(assignments))
	}
	if assignments["t2"] != "w2" {
		t.Errorf("expected security task assigned to security worker, got %s", assignments["t2"])
	}
}

func TestMatchesTaskType(t *testing.T) {
	tests := []struct {
		role, taskType string
		want           bool
	}{
		{"executor", "refactor", true},
		{"executor", "implement", true},
		{"security", "security", true},
		{"security", "docs", false},
		{"reviewer", "review", true},
		{"reviewer", "refactor", false},
	}
	for _, tc := range tests {
		got := matchesTaskType(tc.role, tc.taskType)
		if got != tc.want {
			t.Errorf("matchesTaskType(%q, %q) = %v, want %v", tc.role, tc.taskType, got, tc.want)
		}
	}
}

func TestMatchExpertise(t *testing.T) {
	tests := []struct {
		expertise, file string
		want            bool
	}{
		{"internal/auth/", "internal/auth/handler.go", true},
		{"internal/auth/", "internal/model/user.go", false},
		{"*.go", "main.go", true},
		{"*.go", "style.css", false},
		{"auth", "internal/auth/handler.go", true},
	}
	for _, tc := range tests {
		got := matchExpertise(tc.expertise, tc.file)
		if got != tc.want {
			t.Errorf("matchExpertise(%q, %q) = %v, want %v", tc.expertise, tc.file, got, tc.want)
		}
	}
}
