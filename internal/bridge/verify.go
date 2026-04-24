package bridge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/verify"
)

// VerifyBridge wraps a verify.Pipeline and emits bus events around verification runs.
type VerifyBridge struct {
	pipeline *verify.Pipeline
	bus      *bus.Bus
	ledger   *ledger.Ledger
}

// NewVerifyBridge creates a VerifyBridge with the given build/test/lint commands.
func NewVerifyBridge(b *bus.Bus, l *ledger.Ledger, buildCmd, testCmd, lintCmd string) *VerifyBridge {
	return &VerifyBridge{
		pipeline: verify.NewPipeline(buildCmd, testCmd, lintCmd),
		bus:      b,
		ledger:   l,
	}
}

// Run executes the verification pipeline, emitting start/complete events and
// writing outcomes to the ledger.
func (vb *VerifyBridge) Run(ctx context.Context, dir, taskID, missionID string) ([]verify.Outcome, error) {
	scope := bus.Scope{TaskID: taskID, MissionID: missionID}

	startPayload, _ := json.Marshal(map[string]string{
		"dir":     dir,
		"task_id": taskID,
	})
	_ = vb.bus.Publish(bus.Event{
		Type:      EvtVerifyStarted,
		Timestamp: time.Now(),
		EmitterID: "bridge.verify",
		Scope:     scope,
		Payload:   startPayload,
	})

	outcomes, err := vb.pipeline.Run(ctx, dir)

	completePayload, _ := json.Marshal(struct {
		Outcomes []verify.Outcome `json:"outcomes"`
		Success  bool             `json:"success"`
	}{
		Outcomes: outcomes,
		Success:  err == nil,
	})

	_ = vb.bus.Publish(bus.Event{
		Type:      EvtVerifyCompleted,
		Timestamp: time.Now(),
		EmitterID: "bridge.verify",
		Scope:     scope,
		Payload:   completePayload,
	})

	_, _ = vb.ledger.AddNode(ctx, ledger.Node{
		Type:          "verification",
		SchemaVersion: 1,
		CreatedBy:     "bridge.verify",
		MissionID:     missionID,
		Content:       completePayload,
	})

	return outcomes, err
}
