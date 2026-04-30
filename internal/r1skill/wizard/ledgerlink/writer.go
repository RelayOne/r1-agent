package ledgerlink

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/RelayOne/r1/internal/artifact"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/r1skill/analyze"
	"github.com/RelayOne/r1/internal/r1skill/wizard"
	"github.com/RelayOne/r1/internal/r1skill/wizard/adapter"
)

type PersistOptions struct {
	MissionID  string
	CreatedBy  string
	StanceID   string
	SourcePath string
}

type PersistedSession struct {
	SessionNodeID   ledger.NodeID
	SourceNodeID    ledger.NodeID
	IRNodeID        ledger.NodeID
	ProofNodeID     ledger.NodeID
	DecisionsNodeID ledger.NodeID
}

type Writer struct {
	ledger    *ledger.Ledger
	artifacts *artifact.Builder
}

func NewWriter(l *ledger.Ledger, artifactRoot string) (*Writer, error) {
	if l == nil {
		return nil, fmt.Errorf("wizard/ledgerlink: ledger is required")
	}
	store, err := artifact.NewStore(artifactRoot)
	if err != nil {
		return nil, err
	}
	return &Writer{
		ledger:    l,
		artifacts: artifact.NewBuilder(store, l),
	}, nil
}

func (w *Writer) Persist(ctx context.Context, result *wizard.RunResult, proof *analyze.CompileProof, opts PersistOptions) (*PersistedSession, error) {
	if result == nil || result.Skill == nil || result.Decisions == nil {
		return nil, fmt.Errorf("wizard/ledgerlink: run result is incomplete")
	}
	if proof == nil {
		return nil, fmt.Errorf("wizard/ledgerlink: proof is required")
	}
	if opts.CreatedBy == "" {
		opts.CreatedBy = "stoke wizard"
	}
	if opts.StanceID == "" {
		opts.StanceID = opts.CreatedBy
	}
	skillBytes, err := json.MarshalIndent(result.Skill, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("wizard/ledgerlink: marshal skill: %w", err)
	}
	proofBytes, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("wizard/ledgerlink: marshal proof: %w", err)
	}
	out := &PersistedSession{}
	if result.Source != nil {
		sourceBytes, sourceErr := sourceArtifactBytes(result.Source)
		if sourceErr != nil {
			return nil, sourceErr
		}
		sourceID, emitErr := w.artifacts.Emit(ctx, artifact.EmitParams{
			Kind:        "custom",
			Title:       "wizard source " + filepath.Base(result.SourcePath),
			Content:     sourceBytes,
			ContentType: "application/json",
			MissionID:   opts.MissionID,
			StanceID:    opts.StanceID,
		})
		if emitErr != nil {
			return nil, emitErr
		}
		result.Decisions.SourceArtifactRef = sourceID
		out.SourceNodeID = sourceID
	}
	irID, err := w.artifacts.Emit(ctx, artifact.EmitParams{
		Kind:        "custom",
		Title:       "wizard generated skill ir",
		Content:     skillBytes,
		ContentType: "application/json",
		MissionID:   opts.MissionID,
		StanceID:    opts.StanceID,
	})
	if err != nil {
		return nil, err
	}
	out.IRNodeID = irID
	result.Decisions.ProducedIRRef = irID
	proofID, err := w.artifacts.Emit(ctx, artifact.EmitParams{
		Kind:        "custom",
		Title:       "wizard analyzer proof",
		Content:     proofBytes,
		ContentType: "application/json",
		MissionID:   opts.MissionID,
		StanceID:    opts.StanceID,
	})
	if err != nil {
		return nil, err
	}
	out.ProofNodeID = proofID
	result.Decisions.AnalyzerProofRef = proofID

	decisionBody, err := json.Marshal(result.Decisions)
	if err != nil {
		return nil, fmt.Errorf("wizard/ledgerlink: marshal decisions: %w", err)
	}
	sessionID, err := w.ledger.AddNode(ctx, ledger.Node{
		Type:          "skill_authoring_decisions",
		SchemaVersion: result.Decisions.Version,
		CreatedBy:     opts.CreatedBy,
		MissionID:     opts.MissionID,
		Content:       decisionBody,
	})
	if err != nil {
		return nil, fmt.Errorf("wizard/ledgerlink: add session node: %w", err)
	}
	out.SessionNodeID = sessionID
	out.DecisionsNodeID = sessionID

	for _, ref := range []ledger.NodeID{out.SourceNodeID, out.IRNodeID, out.ProofNodeID} {
		if ref == "" {
			continue
		}
		if err := w.ledger.AddEdge(ctx, ledger.Edge{From: sessionID, To: ref, Type: ledger.EdgeReferences}); err != nil {
			return nil, fmt.Errorf("wizard/ledgerlink: add session edge: %w", err)
		}
	}
	return out, nil
}

func sourceArtifactBytes(src *adapter.SourceArtifact) ([]byte, error) {
	data, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("wizard/ledgerlink: marshal source artifact: %w", err)
	}
	return data, nil
}
