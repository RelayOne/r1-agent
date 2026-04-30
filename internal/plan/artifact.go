package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

// Approval captures the operator approval block persisted into a plan file.
type Approval struct {
	Actor   string    `json:"actor"`
	Mode    string    `json:"mode"`
	Reason  string    `json:"reason,omitempty"`
	At      time.Time `json:"at"`
	EventID string    `json:"event_id,omitempty"`
}

// PlanArtifact is the ledger-backed representation of a plan snapshot.
type PlanArtifact struct {
	PlanID      string    `json:"plan_id"`
	Description string    `json:"description"`
	TaskCount   int       `json:"task_count"`
	SOWHash     string    `json:"sow_hash"`
	OutputPath  string    `json:"output_path"`
	Approval    *Approval `json:"approval,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func HashPlanBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func NewPlanID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "plan-" + hex.EncodeToString(sum[:8])
}

func SaveWithLedger(ctx context.Context, repoRoot, outputPath string, p *Plan, approval *Approval) error {
	if p == nil {
		return fmt.Errorf("plan artifact: nil plan")
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("plan artifact: marshal: %w", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil { // #nosec G306 -- plan artifact is intended to be user-readable.
		return fmt.Errorf("plan artifact: write %s: %w", outputPath, err)
	}

	ledgerDir := filepath.Join(repoRoot, ".r1", "ledger")
	if _, err := os.Stat(ledgerDir); err != nil {
		return nil
	}
	lg, err := ledger.New(ledgerDir)
	if err != nil {
		return fmt.Errorf("plan artifact: open ledger: %w", err)
	}
	defer lg.Close()

	artifact := PlanArtifact{
		PlanID:      p.ID,
		Description: p.Description,
		TaskCount:   len(p.Tasks),
		SOWHash:     HashPlanBytes(data),
		OutputPath:  outputPath,
		Approval:    approval,
		CreatedAt:   time.Now().UTC(),
	}
	body, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("plan artifact: marshal ledger payload: %w", err)
	}
	if _, err := lg.AddNode(ctx, ledger.Node{
		Type:          "plan_artifact",
		SchemaVersion: 1,
		CreatedBy:     actorOrDefault(),
		Content:       body,
	}); err != nil {
		return fmt.Errorf("plan artifact: add node: %w", err)
	}
	if approval == nil {
		return nil
	}
	approvalBody, err := json.Marshal(map[string]any{
		"plan_id":   p.ID,
		"approved":  true,
		"actor":     approval.Actor,
		"mode":      approval.Mode,
		"reason":    approval.Reason,
		"at":        approval.At.UTC(),
		"plan_hash": artifact.SOWHash,
		"event_id":  approval.EventID,
	})
	if err != nil {
		return fmt.Errorf("plan artifact: marshal approval payload: %w", err)
	}
	if _, err := lg.AddNode(ctx, ledger.Node{
		Type:          "plan_approval",
		SchemaVersion: 1,
		CreatedBy:     approval.Actor,
		Content:       approvalBody,
	}); err != nil {
		return fmt.Errorf("plan artifact: add approval node: %w", err)
	}
	return nil
}

func actorOrDefault() string {
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	return "operator"
}
