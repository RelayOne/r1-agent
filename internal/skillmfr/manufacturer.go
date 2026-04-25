package skillmfr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
)

const (
	nodeTypeSkill = "skill"
	createdBy     = "skillmfr"
	schemaVersion = 1

	// Bus event types this manufacturer listens to.
	evtExtractionRequested bus.EventType = "skill.extraction.requested"
	evtImportApproved      bus.EventType = "skill.import.approved"
	evtReviewCompleted     bus.EventType = "skill.review.completed"
)

// Manufacturer handles the full lifecycle of skills in Stoke. It subscribes
// to bus events and writes skill-related ledger nodes.
type Manufacturer struct {
	bus    *bus.Bus
	ledger *ledger.Ledger
	sub    *bus.Subscription
	stopCh chan struct{}
}

// New creates a Manufacturer wired to the given bus and ledger.
func New(b *bus.Bus, l *ledger.Ledger) *Manufacturer {
	return &Manufacturer{
		bus:    b,
		ledger: l,
		stopCh: make(chan struct{}),
	}
}

// Start begins listening for skill-related events on the bus.
func (m *Manufacturer) Start(ctx context.Context) error {
	m.sub = m.bus.Subscribe(bus.Pattern{TypePrefix: "skill."}, func(evt bus.Event) {
		select {
		case <-m.stopCh:
			return
		default:
		}
		m.handleEvent(ctx, evt)
	})
	return nil
}

// Stop gracefully stops the manufacturer.
func (m *Manufacturer) Stop() error {
	close(m.stopCh)
	if m.sub != nil {
		m.sub.Cancel()
	}
	return nil
}

// handleEvent routes incoming bus events to the appropriate workflow.
func (m *Manufacturer) handleEvent(ctx context.Context, evt bus.Event) {
	switch evt.Type {
	case evtExtractionRequested:
		_ = m.ExtractFromMission(ctx, evt.Scope.MissionID)
	case evtImportApproved:
		var payload struct {
			ProposalID string `json:"proposal_id"`
		}
		if err := json.Unmarshal(evt.Payload, &payload); err == nil {
			_ = m.ProcessImport(ctx, payload.ProposalID)
		}
	case evtReviewCompleted:
		var payload struct {
			ReviewID string `json:"review_id"`
		}
		if err := json.Unmarshal(evt.Payload, &payload); err == nil {
			_ = m.ProcessReview(ctx, payload.ReviewID)
		}
	}
}

// ImportShippedLibrary reads embedded skill files and writes them to the ledger.
// Each skill becomes a node of type "skill" with provenance "shipped_with_stoke"
// and confidence "proven".
func (m *Manufacturer) ImportShippedLibrary(ctx context.Context, skills []SkillFile) error {
	for i := range skills {
		sf := skills[i]
		sf.Provenance = ProvenanceShipped
		sf.Confidence = ConfidenceProven

		content, err := json.Marshal(sf)
		if err != nil {
			return fmt.Errorf("skillmfr: marshal skill %q: %w", sf.Name, err)
		}

		node := ledger.Node{
			Type:          nodeTypeSkill,
			SchemaVersion: schemaVersion,
			CreatedAt:     time.Now().UTC(),
			CreatedBy:     createdBy,
			Content:       content,
		}
		if _, err := m.ledger.AddNode(ctx, node); err != nil {
			return fmt.Errorf("skillmfr: write skill %q: %w", sf.Name, err)
		}
	}
	return nil
}

// ExtractFromMission reads completed mission decisions and manufactures skills.
// The extracted skill starts at "candidate" confidence.
func (m *Manufacturer) ExtractFromMission(ctx context.Context, missionID string) error {
	nodes, err := m.ledger.Query(ctx, ledger.QueryFilter{
		MissionID: missionID,
		Type:      "decision",
	})
	if err != nil {
		return fmt.Errorf("skillmfr: query decisions for mission %s: %w", missionID, err)
	}
	if len(nodes) == 0 {
		return nil
	}

	sf := SkillFile{
		Name:       fmt.Sprintf("extracted-%s", missionID),
		Confidence: ConfidenceCandidate,
		Provenance: ProvenanceManufactured,
	}

	content, err := json.Marshal(sf)
	if err != nil {
		return fmt.Errorf("skillmfr: marshal extracted skill: %w", err)
	}

	node := ledger.Node{
		Type:          nodeTypeSkill,
		SchemaVersion: schemaVersion,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     createdBy,
		MissionID:     missionID,
		Content:       content,
	}
	if _, err := m.ledger.AddNode(ctx, node); err != nil {
		return fmt.Errorf("skillmfr: write extracted skill: %w", err)
	}
	return nil
}

// ProcessImport validates and writes an approved external skill import.
// The imported skill starts at "candidate" confidence.
func (m *Manufacturer) ProcessImport(ctx context.Context, proposalID string) error {
	proposal, err := m.ledger.Get(ctx, proposalID)
	if err != nil {
		return fmt.Errorf("skillmfr: get proposal %s: %w", proposalID, err)
	}

	var sf SkillFile
	if err = json.Unmarshal(proposal.Content, &sf); err != nil {
		return fmt.Errorf("skillmfr: unmarshal proposal: %w", err)
	}

	sf.Provenance = ProvenanceImported
	sf.Confidence = ConfidenceCandidate

	content, err := json.Marshal(sf)
	if err != nil {
		return fmt.Errorf("skillmfr: marshal imported skill: %w", err)
	}

	node := ledger.Node{
		Type:          nodeTypeSkill,
		SchemaVersion: schemaVersion,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     createdBy,
		Content:       content,
	}
	newID, err := m.ledger.AddNode(ctx, node)
	if err != nil {
		return fmt.Errorf("skillmfr: write imported skill: %w", err)
	}

	// Link the new skill node back to the proposal.
	if err := m.ledger.AddEdge(ctx, ledger.Edge{
		From: newID,
		To:   proposalID,
		Type: ledger.EdgeReferences,
	}); err != nil {
		return fmt.Errorf("skillmfr: link to proposal: %w", err)
	}
	return nil
}

// ProcessReview handles confidence promotion/demotion based on review results.
// It reads the review node from the ledger, determines the action, and writes
// a new superseding skill node with the updated confidence or footgun note.
func (m *Manufacturer) ProcessReview(ctx context.Context, reviewID string) error {
	reviewNode, err := m.ledger.Get(ctx, reviewID)
	if err != nil {
		return fmt.Errorf("skillmfr: get review %s: %w", reviewID, err)
	}

	var rr ReviewResult
	if err = json.Unmarshal(reviewNode.Content, &rr); err != nil {
		return fmt.Errorf("skillmfr: unmarshal review: %w", err)
	}

	skillNode, err := m.ledger.Get(ctx, rr.SkillID)
	if err != nil {
		return fmt.Errorf("skillmfr: get skill %s: %w", rr.SkillID, err)
	}

	var sf SkillFile
	if err = json.Unmarshal(skillNode.Content, &sf); err != nil {
		return fmt.Errorf("skillmfr: unmarshal skill: %w", err)
	}

	switch rr.Action {
	case ActionPromote:
		if rr.NewConfidence != "" {
			sf.Confidence = rr.NewConfidence
		} else {
			sf.Confidence = promoteConfidence(sf.Confidence)
		}
	case ActionDemote:
		if rr.NewConfidence != "" {
			sf.Confidence = rr.NewConfidence
		} else {
			sf.Confidence = demoteConfidence(sf.Confidence)
		}
	case ActionMarkFootgun:
		sf.FootgunNote = rr.Reasoning
	case ActionSupersede:
		// Supersede keeps the same skill data but marks a new version.
	}

	content, err := json.Marshal(sf)
	if err != nil {
		return fmt.Errorf("skillmfr: marshal updated skill: %w", err)
	}

	newNode := ledger.Node{
		Type:          nodeTypeSkill,
		SchemaVersion: schemaVersion,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     createdBy,
		Content:       content,
	}
	newID, err := m.ledger.AddNode(ctx, newNode)
	if err != nil {
		return fmt.Errorf("skillmfr: write updated skill: %w", err)
	}

	// The new node supersedes the old one.
	if err := m.ledger.AddEdge(ctx, ledger.Edge{
		From: newID,
		To:   rr.SkillID,
		Type: ledger.EdgeSupersedes,
		Metadata: map[string]string{
			"action":    string(rr.Action),
			"reasoning": rr.Reasoning,
		},
	}); err != nil {
		return fmt.Errorf("skillmfr: add supersedes edge: %w", err)
	}

	return nil
}
