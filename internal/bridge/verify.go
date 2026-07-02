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
	vb.PublishStarted(dir, taskID, missionID)
	outcomes, err := vb.pipeline.Run(ctx, dir)
	vb.PublishCompleted(ctx, taskID, missionID, outcomes, err == nil)
	return outcomes, err
}

// PublishStarted emits the verify.started bus event. Split out from Run
// (audit A037) so callers whose verification is executed elsewhere — the
// workflow engine rebuilds its own policy-filtered pipeline per attempt —
// can still announce the run on the governance bus.
func (vb *VerifyBridge) PublishStarted(dir, taskID, missionID string) {
	startPayload, _ := json.Marshal(map[string]string{
		"dir":     dir,
		"task_id": taskID,
	})
	_ = vb.bus.Publish(bus.Event{
		Type:      EvtVerifyStarted,
		Timestamp: time.Now(),
		EmitterID: "bridge.verify",
		Scope:     bus.Scope{TaskID: taskID, MissionID: missionID},
		Payload:   startPayload,
	})
}

// PublishCompleted emits the verify.completed bus event and writes the
// "verification" ledger node for outcomes produced by an external runner
// (audit A037). Use alongside PublishStarted when the pipeline itself is
// executed outside the bridge.
func (vb *VerifyBridge) PublishCompleted(ctx context.Context, taskID, missionID string, outcomes []verify.Outcome, success bool) {
	completePayload, _ := json.Marshal(struct {
		Outcomes []verify.Outcome `json:"outcomes"`
		Success  bool             `json:"success"`
	}{
		Outcomes: outcomes,
		Success:  success,
	})

	_ = vb.bus.Publish(bus.Event{
		Type:      EvtVerifyCompleted,
		Timestamp: time.Now(),
		EmitterID: "bridge.verify",
		Scope:     bus.Scope{TaskID: taskID, MissionID: missionID},
		Payload:   completePayload,
	})

	_, _ = vb.ledger.AddNode(ctx, ledger.Node{
		Type:          "verification",
		SchemaVersion: 1,
		CreatedBy:     "bridge.verify",
		MissionID:     missionID,
		Content:       completePayload,
	})
}
