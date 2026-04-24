package bridge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/RelayOne/r1/internal/audit"
	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

// AuditBridge wraps audit report recording into bus events and ledger nodes.
type AuditBridge struct {
	bus    *bus.Bus
	ledger *ledger.Ledger
}

// NewAuditBridge creates an AuditBridge.
func NewAuditBridge(b *bus.Bus, l *ledger.Ledger) *AuditBridge {
	return &AuditBridge{bus: b, ledger: l}
}

// RecordReport publishes an audit completed event and writes an audit_report
// node to the ledger. If taskID matches an existing ledger node, a "references"
// edge is created from the audit node to the task node.
func (ab *AuditBridge) RecordReport(ctx context.Context, taskID, missionID string, report audit.AuditReport) error {
	payload, err := json.Marshal(report)
	if err != nil {
		return err
	}

	scope := bus.Scope{TaskID: taskID, MissionID: missionID}
	if pubErr := ab.bus.Publish(bus.Event{
		Type:      EvtAuditCompleted,
		Timestamp: time.Now(),
		EmitterID: "bridge.audit",
		Scope:     scope,
		Payload:   payload,
	}); pubErr != nil {
		return pubErr
	}

	nodeID, err := ab.ledger.AddNode(ctx, ledger.Node{
		Type:          "audit_report",
		SchemaVersion: 1,
		CreatedBy:     "bridge.audit",
		MissionID:     missionID,
		Content:       payload,
	})
	if err != nil {
		return err
	}

	// Try to link to an existing task node if one exists.
	if taskID != "" {
		if _, getErr := ab.ledger.Get(ctx, taskID); getErr == nil {
			_ = ab.ledger.AddEdge(ctx, ledger.Edge{
				From: nodeID,
				To:   taskID,
				Type: ledger.EdgeReferences,
			})
		}
	}

	return nil
}
