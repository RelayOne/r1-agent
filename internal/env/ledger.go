package env

import (
	"context"
	"encoding/json"
	"time"

	"github.com/RelayOne/r1-agent/internal/ledger"
	"github.com/RelayOne/r1-agent/internal/ledger/nodes"
)

// RecordProvision writes an ExecutionEnvironment ledger node when an
// environment is provisioned. Returns the node ID for linking.
func RecordProvision(ctx context.Context, l *ledger.Ledger, h *Handle, spec Spec) (ledger.NodeID, error) {
	if l == nil || h == nil {
		return "", nil
	}

	envNode := &nodes.ExecutionEnvironment{
		Backend:       string(h.Backend),
		BaseImage:     spec.BaseImage,
		WorkDir:       h.WorkDir,
		CPUs:          spec.CPUs,
		MemoryMB:      spec.MemoryMB,
		Size:          spec.Size,
		ProvisionedAt: h.CreatedAt,
		Meta:          h.Meta,
		Version:       1,
	}
	for _, svc := range spec.Services {
		envNode.Services = append(envNode.Services, svc.Name)
	}

	content, err := json.Marshal(envNode)
	if err != nil {
		return "", err
	}

	return l.AddNode(ctx, ledger.Node{
		Type:          "execution_environment",
		Content:       content,
		SchemaVersion: 1,
		CreatedAt:     time.Now(),
	})
}

// RecordTeardown updates the ExecutionEnvironment ledger node with teardown
// time and final cost. Since ledger nodes are immutable, this creates a new
// node with the updated data.
func RecordTeardown(ctx context.Context, l *ledger.Ledger, h *Handle, cost CostEstimate) (ledger.NodeID, error) {
	if l == nil || h == nil {
		return "", nil
	}

	now := time.Now()
	envNode := &nodes.ExecutionEnvironment{
		Backend:       string(h.Backend),
		WorkDir:       h.WorkDir,
		ProvisionedAt: h.CreatedAt,
		TornDownAt:    &now,
		CostUSD:       cost.TotalUSD,
		Meta:          h.Meta,
		Version:       1,
	}

	content, err := json.Marshal(envNode)
	if err != nil {
		return "", err
	}

	return l.AddNode(ctx, ledger.Node{
		Type:          "execution_environment",
		Content:       content,
		SchemaVersion: 1,
		CreatedAt:     time.Now(),
	})
}
