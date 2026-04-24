package env

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
)

func TestRecordProvision(t *testing.T) {
	dir := t.TempDir()
	lg, err := ledger.New(dir)
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	defer lg.Close()

	h := &Handle{
		ID:        "env-1",
		Backend:   BackendDocker,
		WorkDir:   "/workspace",
		Meta:      map[string]string{"container_id": "abc123"},
		CreatedAt: time.Now(),
	}
	spec := Spec{
		Backend:   BackendDocker,
		BaseImage: "golang:1.22",
		CPUs:      4,
		MemoryMB:  2048,
		Size:      "performance-4x",
		Services:  []ServiceSpec{{Name: "postgres"}, {Name: "redis"}},
	}

	nodeID, err := RecordProvision(context.Background(), lg, h, spec)
	if err != nil {
		t.Fatalf("RecordProvision: %v", err)
	}
	if nodeID == "" {
		t.Fatal("nodeID should not be empty")
	}

	// Retrieve the node and verify its content.
	node, err := lg.Get(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.Type != "execution_environment" {
		t.Errorf("type=%q, want execution_environment", node.Type)
	}
	if node.SchemaVersion != 1 {
		t.Errorf("schema_version=%d, want 1", node.SchemaVersion)
	}

	var envNode nodes.ExecutionEnvironment
	if err := json.Unmarshal(node.Content, &envNode); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envNode.Backend != "docker" {
		t.Errorf("backend=%q, want docker", envNode.Backend)
	}
	if envNode.BaseImage != "golang:1.22" {
		t.Errorf("base_image=%q, want golang:1.22", envNode.BaseImage)
	}
	if envNode.WorkDir != "/workspace" {
		t.Errorf("work_dir=%q, want /workspace", envNode.WorkDir)
	}
	if envNode.CPUs != 4 {
		t.Errorf("cpus=%d, want 4", envNode.CPUs)
	}
	if envNode.MemoryMB != 2048 {
		t.Errorf("memory_mb=%d, want 2048", envNode.MemoryMB)
	}
	if envNode.Size != "performance-4x" {
		t.Errorf("size=%q, want performance-4x", envNode.Size)
	}
	if len(envNode.Services) != 2 {
		t.Errorf("services=%d, want 2", len(envNode.Services))
	}
	if envNode.Meta["container_id"] != "abc123" {
		t.Errorf("meta=%v, want container_id=abc123", envNode.Meta)
	}
}

func TestRecordTeardown(t *testing.T) {
	dir := t.TempDir()
	lg, err := ledger.New(dir)
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	defer lg.Close()

	h := &Handle{
		ID:        "env-2",
		Backend:   BackendFly,
		WorkDir:   "/app",
		CreatedAt: time.Now().Add(-5 * time.Minute),
	}
	cost := CostEstimate{
		TotalUSD: 0.0125,
		Elapsed:  5 * time.Minute,
	}

	nodeID, err := RecordTeardown(context.Background(), lg, h, cost)
	if err != nil {
		t.Fatalf("RecordTeardown: %v", err)
	}
	if nodeID == "" {
		t.Fatal("nodeID should not be empty")
	}

	node, err := lg.Get(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}

	var envNode nodes.ExecutionEnvironment
	if err := json.Unmarshal(node.Content, &envNode); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envNode.Backend != "fly" {
		t.Errorf("backend=%q, want fly", envNode.Backend)
	}
	if envNode.TornDownAt == nil {
		t.Fatal("torn_down_at should be set")
	}
	if envNode.CostUSD != 0.0125 {
		t.Errorf("cost_usd=%f, want 0.0125", envNode.CostUSD)
	}
}

func TestRecordProvisionNilLedger(t *testing.T) {
	h := &Handle{ID: "env-3", Backend: BackendDocker}
	nodeID, err := RecordProvision(context.Background(), nil, h, Spec{})
	if err != nil {
		t.Fatalf("should not error with nil ledger: %v", err)
	}
	if nodeID != "" {
		t.Errorf("nodeID=%q, want empty for nil ledger", nodeID)
	}
}

func TestRecordProvisionNilHandle(t *testing.T) {
	dir := t.TempDir()
	lg, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	nodeID, err := RecordProvision(context.Background(), lg, nil, Spec{})
	if err != nil {
		t.Fatalf("should not error with nil handle: %v", err)
	}
	if nodeID != "" {
		t.Errorf("nodeID=%q, want empty for nil handle", nodeID)
	}
}

func TestRecordTeardownNilLedger(t *testing.T) {
	h := &Handle{ID: "env-4", Backend: BackendDocker}
	nodeID, err := RecordTeardown(context.Background(), nil, h, CostEstimate{})
	if err != nil {
		t.Fatalf("should not error with nil ledger: %v", err)
	}
	if nodeID != "" {
		t.Errorf("nodeID=%q, want empty for nil ledger", nodeID)
	}
}

func TestRecordTeardownNilHandle(t *testing.T) {
	dir := t.TempDir()
	lg, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	nodeID, err := RecordTeardown(context.Background(), lg, nil, CostEstimate{})
	if err != nil {
		t.Fatalf("should not error with nil handle: %v", err)
	}
	if nodeID != "" {
		t.Errorf("nodeID=%q, want empty for nil handle", nodeID)
	}
}
