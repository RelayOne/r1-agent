package governance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/skillmfr"
)

// These tests cover the audit A071 activation: the skill manufacturing
// pipeline must be live on every governed run. The full chain under
// test is:
//
//	hub git.post_merge → Governor.onDecision ("decision" ledger node)
//	                   → Governor.publishLoopConverged ("loop.converged")
//	→ supervisor skill.extraction_trigger → skill.extraction.requested
//	→ skillmfr.Manufacturer → "skill" ledger node
//
// Every hop runs on the real wired components (durable bus, ledger,
// mission supervisor manifest, Manufacturer started by New).

// TestGovernorManufacturesSkillOnMissionCompletion drives a task merge
// through the wired HubSubscriber and asserts a manufactured skill
// ledger node appears.
func TestGovernorManufacturesSkillOnMissionCompletion(t *testing.T) {
	g := newTestGovernor(t, 0)
	sub := g.HubSubscriber()
	ctx := context.Background()

	// Pre-merge writes the "decision" node ExtractFromMission reads;
	// post-merge writes a second decision node, then publishes
	// merge.done + loop.converged.
	sub.Handler(ctx, &hub.Event{
		Type:   hub.EventGitPreMerge,
		TaskID: "task-1",
		Git:    &hub.GitEvent{Operation: "merge", Branch: "feature"},
	})
	sub.Handler(ctx, &hub.Event{
		Type:   hub.EventGitPostMerge,
		TaskID: "task-1",
		Git:    &hub.GitEvent{Operation: "merge"},
	})

	nodes := pollNodeCount(t, g, "skill", 1)
	if len(nodes) < 1 {
		t.Fatalf("expected >=1 skill ledger node after merge.done, got %d", len(nodes))
	}

	var sf skillmfr.SkillFile
	if err := json.Unmarshal(nodes[0].Content, &sf); err != nil {
		t.Fatalf("unmarshal skill content: %v", err)
	}
	if sf.Name != "extracted-mission-test" {
		t.Errorf("skill Name = %q, want extracted-mission-test", sf.Name)
	}
	if sf.Provenance != skillmfr.ProvenanceManufactured {
		t.Errorf("skill Provenance = %q, want %q", sf.Provenance, skillmfr.ProvenanceManufactured)
	}
	if sf.Confidence != skillmfr.ConfidenceCandidate {
		t.Errorf("skill Confidence = %q, want %q", sf.Confidence, skillmfr.ConfidenceCandidate)
	}
	if nodes[0].MissionID != "mission-test" {
		t.Errorf("skill node MissionID = %q, want mission-test", nodes[0].MissionID)
	}
	if nodes[0].CreatedBy != "skillmfr" {
		t.Errorf("skill node CreatedBy = %q, want skillmfr", nodes[0].CreatedBy)
	}
}

// TestGovernorManufacturesSkillOnMissionConverged covers the
// mission-runner completion path: a mission.converged hub event alone
// (after at least one recorded decision) must produce the skill node.
func TestGovernorManufacturesSkillOnMissionConverged(t *testing.T) {
	g := newTestGovernor(t, 0)
	sub := g.HubSubscriber()
	ctx := context.Background()

	// Record a decision without a post-merge (pre-merge only) so the
	// only loop.converged comes from mission.converged.
	sub.Handler(ctx, &hub.Event{
		Type:   hub.EventGitPreMerge,
		TaskID: "task-1",
		Git:    &hub.GitEvent{Operation: "merge", Branch: "feature"},
	})
	sub.Handler(ctx, &hub.Event{Type: hub.EventMissionConverged})

	if nodes := pollNodeCount(t, g, "skill", 1); len(nodes) < 1 {
		t.Fatalf("expected >=1 skill node after mission.converged, got %d", len(nodes))
	}
}

// TestGovernorSkillExtractionIdempotent asserts that repeated merges in
// one mission manufacture exactly one extracted skill node.
func TestGovernorSkillExtractionIdempotent(t *testing.T) {
	g := newTestGovernor(t, 0)
	sub := g.HubSubscriber()
	ctx := context.Background()

	for _, taskID := range []string{"task-1", "task-2", "task-3"} {
		sub.Handler(ctx, &hub.Event{
			Type:   hub.EventGitPreMerge,
			TaskID: taskID,
			Git:    &hub.GitEvent{Operation: "merge", Branch: "feature"},
		})
		sub.Handler(ctx, &hub.Event{
			Type:   hub.EventGitPostMerge,
			TaskID: taskID,
			Git:    &hub.GitEvent{Operation: "merge"},
		})
	}

	// Wait for the first extraction, then give the remaining
	// loop.converged deliveries time to (not) extract again.
	if nodes := pollNodeCount(t, g, "skill", 1); len(nodes) < 1 {
		t.Fatalf("expected >=1 skill node, got %d", len(nodes))
	}
	time.Sleep(300 * time.Millisecond)

	nodes := pollNodeCount(t, g, "skill", 1)
	extracted := 0
	for _, n := range nodes {
		var sf skillmfr.SkillFile
		if err := json.Unmarshal(n.Content, &sf); err != nil {
			t.Fatalf("unmarshal skill content: %v", err)
		}
		if sf.Name == "extracted-mission-test" {
			extracted++
		}
	}
	if extracted != 1 {
		t.Errorf("expected exactly 1 extracted skill node, got %d (of %d skill nodes)", extracted, len(nodes))
	}
}
