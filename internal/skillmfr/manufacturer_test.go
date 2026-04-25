package skillmfr

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
)

// setup creates a temporary bus and ledger for testing.
func setup(t *testing.T) (*bus.Bus, *ledger.Ledger, func()) {
	t.Helper()
	dir := t.TempDir()

	b, err := bus.New(filepath.Join(dir, "bus"))
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}

	l, err := ledger.New(filepath.Join(dir, "ledger"))
	if err != nil {
		b.Close()
		t.Fatalf("ledger.New: %v", err)
	}

	cleanup := func() {
		l.Close()
		b.Close()
	}
	return b, l, cleanup
}

func TestImportShippedLibrary(t *testing.T) {
	b, l, cleanup := setup(t)
	defer cleanup()

	m := New(b, l)
	ctx := context.Background()

	skills := []SkillFile{
		{
			Name:          "tdd",
			Description:   "Test-driven development workflow",
			Keywords:      []string{"tdd", "test-driven"},
			Applicability: []string{"go", "python"},
			Content:       "Write tests first, then implement.",
		},
		{
			Name:          "ralph",
			Description:   "Persistent execution discipline",
			Keywords:      []string{"ralph", "persistence"},
			Applicability: []string{"all"},
			Content:       "Never give up. Retry with escalation.",
		},
	}

	if err := m.ImportShippedLibrary(ctx, skills); err != nil {
		t.Fatalf("ImportShippedLibrary: %v", err)
	}

	// Verify nodes were written.
	nodes, err := l.Query(ctx, ledger.QueryFilter{Type: "skill"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}

	// Verify content of first node.
	var sf SkillFile
	if err := json.Unmarshal(nodes[0].Content, &sf); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sf.Provenance != ProvenanceShipped {
		t.Errorf("provenance = %q, want %q", sf.Provenance, ProvenanceShipped)
	}
	if sf.Confidence != ConfidenceProven {
		t.Errorf("confidence = %q, want %q", sf.Confidence, ConfidenceProven)
	}
}

func TestProcessReviewPromote(t *testing.T) {
	b, l, cleanup := setup(t)
	defer cleanup()

	m := New(b, l)
	ctx := context.Background()

	// Write an initial skill node at candidate confidence.
	sf := SkillFile{
		Name:       "test-skill",
		Confidence: ConfidenceCandidate,
		Provenance: ProvenanceManufactured,
		Content:    "Some content",
	}
	sfBytes, _ := json.Marshal(sf)
	skillID, err := l.AddNode(ctx, ledger.Node{
		Type:          "skill",
		SchemaVersion: 1,
		CreatedBy:     "test",
		Content:       sfBytes,
	})
	if err != nil {
		t.Fatalf("AddNode skill: %v", err)
	}

	// Write a review node requesting promotion.
	rr := ReviewResult{
		SkillID:   skillID,
		Action:    ActionPromote,
		Reasoning: "Works well in practice",
	}
	rrBytes, _ := json.Marshal(rr)
	reviewID, err := l.AddNode(ctx, ledger.Node{
		Type:          "review",
		SchemaVersion: 1,
		CreatedBy:     "test",
		Content:       rrBytes,
	})
	if err != nil {
		t.Fatalf("AddNode review: %v", err)
	}

	if err := m.ProcessReview(ctx, reviewID); err != nil {
		t.Fatalf("ProcessReview: %v", err)
	}

	// The effective node should now be tentative (promoted from candidate).
	resolved, err := l.Resolve(ctx, skillID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var updated SkillFile
	if err := json.Unmarshal(resolved.Content, &updated); err != nil {
		t.Fatalf("unmarshal resolved: %v", err)
	}
	if updated.Confidence != ConfidenceTentative {
		t.Errorf("confidence = %q, want %q", updated.Confidence, ConfidenceTentative)
	}
}

func TestProcessReviewDemote(t *testing.T) {
	b, l, cleanup := setup(t)
	defer cleanup()

	m := New(b, l)
	ctx := context.Background()

	// Write an initial skill at proven confidence.
	sf := SkillFile{
		Name:       "fragile-skill",
		Confidence: ConfidenceProven,
		Provenance: ProvenanceManufactured,
		Content:    "Fragile content",
	}
	sfBytes, _ := json.Marshal(sf)
	skillID, err := l.AddNode(ctx, ledger.Node{
		Type:          "skill",
		SchemaVersion: 1,
		CreatedBy:     "test",
		Content:       sfBytes,
	})
	if err != nil {
		t.Fatalf("AddNode skill: %v", err)
	}

	// Write a review node requesting demotion.
	rr := ReviewResult{
		SkillID:   skillID,
		Action:    ActionDemote,
		Reasoning: "Caused regressions",
	}
	rrBytes, _ := json.Marshal(rr)
	reviewID, err := l.AddNode(ctx, ledger.Node{
		Type:          "review",
		SchemaVersion: 1,
		CreatedBy:     "test",
		Content:       rrBytes,
	})
	if err != nil {
		t.Fatalf("AddNode review: %v", err)
	}

	if err := m.ProcessReview(ctx, reviewID); err != nil {
		t.Fatalf("ProcessReview: %v", err)
	}

	resolved, err := l.Resolve(ctx, skillID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var updated SkillFile
	if err := json.Unmarshal(resolved.Content, &updated); err != nil {
		t.Fatalf("unmarshal resolved: %v", err)
	}
	if updated.Confidence != ConfidenceTentative {
		t.Errorf("confidence = %q, want %q", updated.Confidence, ConfidenceTentative)
	}
}

func TestProcessReviewMarkFootgun(t *testing.T) {
	b, l, cleanup := setup(t)
	defer cleanup()

	m := New(b, l)
	ctx := context.Background()

	sf := SkillFile{
		Name:       "dangerous-skill",
		Confidence: ConfidenceTentative,
		Provenance: ProvenanceManufactured,
		Content:    "Dangerous content",
	}
	sfBytes, _ := json.Marshal(sf)
	skillID, err := l.AddNode(ctx, ledger.Node{
		Type:          "skill",
		SchemaVersion: 1,
		CreatedBy:     "test",
		Content:       sfBytes,
	})
	if err != nil {
		t.Fatalf("AddNode skill: %v", err)
	}

	rr := ReviewResult{
		SkillID:   skillID,
		Action:    ActionMarkFootgun,
		Reasoning: "Can corrupt database if used on production",
	}
	rrBytes, _ := json.Marshal(rr)
	reviewID, err := l.AddNode(ctx, ledger.Node{
		Type:          "review",
		SchemaVersion: 1,
		CreatedBy:     "test",
		Content:       rrBytes,
	})
	if err != nil {
		t.Fatalf("AddNode review: %v", err)
	}

	if err := m.ProcessReview(ctx, reviewID); err != nil {
		t.Fatalf("ProcessReview: %v", err)
	}

	resolved, err := l.Resolve(ctx, skillID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var updated SkillFile
	if err := json.Unmarshal(resolved.Content, &updated); err != nil {
		t.Fatalf("unmarshal resolved: %v", err)
	}
	if updated.FootgunNote != rr.Reasoning {
		t.Errorf("footgun_note = %q, want %q", updated.FootgunNote, rr.Reasoning)
	}
	// Confidence should remain unchanged for mark_footgun.
	if updated.Confidence != ConfidenceTentative {
		t.Errorf("confidence = %q, want %q (unchanged)", updated.Confidence, ConfidenceTentative)
	}
}

func TestStartStop(t *testing.T) {
	b, l, cleanup := setup(t)
	defer cleanup()

	m := New(b, l)
	ctx := context.Background()

	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if m.sub == nil {
		t.Fatal("subscription should be set after Start")
	}

	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestEventHandlerRouting(t *testing.T) {
	dir := t.TempDir()

	b, err := bus.New(filepath.Join(dir, "bus"))
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	defer b.Close()

	l, err := ledger.New(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	defer l.Close()

	m := New(b, l)
	ctx := context.Background()

	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	// Publish a skill.extraction.requested event with a mission ID.
	// Pre-create a decision node so ExtractFromMission has something to find.
	decContent, _ := json.Marshal(map[string]string{"decision": "use TDD"})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "decision",
		SchemaVersion: 1,
		CreatedBy:     "test",
		MissionID:     "mission-123",
		Content:       decContent,
	})
	if err != nil {
		t.Fatalf("AddNode decision: %v", err)
	}

	err = b.Publish(bus.Event{
		Type: bus.EvtSkillExtraction,
		Scope: bus.Scope{
			MissionID: "mission-123",
		},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Give the handler a moment to process (subscriber is synchronous in
	// the bus implementation, but be defensive).
	time.Sleep(50 * time.Millisecond)

	// Verify a manufactured skill node was created for mission-123.
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "skill",
		MissionID: "mission-123",
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected at least one manufactured skill node")
	}

	var sf SkillFile
	if err := json.Unmarshal(nodes[0].Content, &sf); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sf.Provenance != ProvenanceManufactured {
		t.Errorf("provenance = %q, want %q", sf.Provenance, ProvenanceManufactured)
	}
}
