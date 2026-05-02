package agentserve

// server_settle_test.go — TASK-T20 settlement callback coverage.
//
// The callback is the seam `cmd/r1/agent_serve_cmd.go` uses to
// drive TrustPlane Settle/Dispute from a terminal task transition.
// This test does not touch TrustPlane; it asserts the seam fires
// after a real task completes through the HTTP surface, with the
// correct (taskID, passed, evidence) signature.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/executor"
)

// TestServer_OnTaskComplete_FiresOnSuccess posts a single research
// task and blocks until the Server fires OnTaskComplete. The fake
// callback records the payload under a mutex-free atomic pointer;
// the assertion then re-checks id, passed=true, and the submitted
// contract_id round-trips through Server.TaskMetadata.
func TestServer_OnTaskComplete_FiresOnSuccess(t *testing.T) {
	done := make(chan struct{}, 1)
	var seenID atomic.Value
	var seenPassed int32
	var seenEvidenceCount int32

	cfg := Config{
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: okExecutor(executor.TaskResearch),
		},
		OnTaskComplete: func(taskID string, passed bool, evidence [][]byte) {
			seenID.Store(taskID)
			if passed {
				atomic.StoreInt32(&seenPassed, 1)
			}
			atomic.StoreInt32(&seenEvidenceCount, int32(len(evidence)))
			select {
			case done <- struct{}{}:
			default:
			}
		},
	}

	s, ts := newTestServer(t, cfg)

	body, err := json.Marshal(TaskRequest{
		TaskType:    "research",
		Description: "hello world",
		Extra: map[string]any{
			"contract_id": "contract-xyz-123",
			"amount_usd":  2.5,
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp, err := http.Post(ts.URL+"/api/task", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var posted TaskState
	if err := json.NewDecoder(resp.Body).Decode(&posted); err != nil {
		t.Fatalf("decode: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("OnTaskComplete did not fire within 3s")
	}

	gotID, _ := seenID.Load().(string)
	if gotID != posted.ID {
		t.Fatalf("callback taskID = %q, want %q", gotID, posted.ID)
	}
	if atomic.LoadInt32(&seenPassed) != 1 {
		t.Fatalf("callback passed flag was not set on success")
	}
	if atomic.LoadInt32(&seenEvidenceCount) != 0 {
		t.Fatalf("successful task should carry no evidence bytes (got %d)",
			atomic.LoadInt32(&seenEvidenceCount))
	}

	// Server.TaskMetadata is the supported accessor for callbacks that
	// need to reach into the submitted TaskRequest.Extra (e.g. pulling
	// contract_id for Settle). Assert it returns the exact contract_id
	// and amount the client submitted.
	meta := s.TaskMetadata(posted.ID)
	if got, _ := meta["contract_id"].(string); got != "contract-xyz-123" {
		t.Fatalf("TaskMetadata contract_id = %v, want contract-xyz-123", meta["contract_id"])
	}
	if got, _ := meta["amount_usd"].(float64); got != 2.5 {
		t.Fatalf("TaskMetadata amount_usd = %v, want 2.5", meta["amount_usd"])
	}
}
