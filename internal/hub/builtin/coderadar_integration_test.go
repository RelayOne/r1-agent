//go:build coderadar_integration

// Package builtin — integration test for end-to-end correlation
// propagation through the bus → subscriber → ingest pipeline (T6).
//
// The test starts an httptest server that records every event, then
// drives a sequence of hub events that simulate a mission's lifecycle
// (plan → execute → provider call (start+done) → tool → completion).
// assert.SixDistinctNames appear on the wire and assert.AllShareOne
// correlation_id derived from the originating session.
package builtin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/coderadar"
	"github.com/RelayOne/r1/internal/hub"
)

// TestEndToEndCorrelationPropagation drives a synthetic mission
// through the hub bus and asserts the subscriber emits events with
// shared correlation IDs reaching the recorded ingest endpoint.
func TestEndToEndCorrelationPropagation(t *testing.T) {
	var (
		mu     sync.Mutex
		events []map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.Unmarshal(body, &batch)
		mu.Lock()
		events = append(events, batch.Events...)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	t.Setenv("CODERADAR_SAMPLE_RATE", "1.0")
	dsn := strings.Replace(srv.URL, "http://", "http://cw_test@", 1)
	client := coderadar.New(dsn, "r1-server", "dev")
	sub := NewCoderadarSubscriber(client)
	defer sub.Close()
	bus := hub.New()
	sub.Register(bus)

	missionID := "m-int-7"
	ctx := context.Background()
	publishEvent(bus, ctx, &hub.Event{Type: hub.EventMissionPlanDone, MissionID: missionID, Timestamp: time.Now()})
	publishEvent(bus, ctx, &hub.Event{Type: hub.EventMissionExecuteStart, MissionID: missionID, Timestamp: time.Now()})
	publishEvent(bus, ctx, &hub.Event{
		Type: hub.EventModelPreCall, MissionID: missionID, Timestamp: time.Now(),
		Model: &hub.ModelEvent{Provider: "anthropic", Model: "claude-opus-4-7"},
	})
	publishEvent(bus, ctx, &hub.Event{
		Type: hub.EventModelPostCall, MissionID: missionID, Timestamp: time.Now(),
		Model: &hub.ModelEvent{Provider: "anthropic", Model: "claude-opus-4-7", InputTokens: 100, OutputTokens: 50, CostUSD: 0.01},
	})
	publishEvent(bus, ctx, &hub.Event{
		Type: hub.EventToolPostUse, MissionID: missionID, Timestamp: time.Now(),
		Tool: &hub.ToolEvent{Name: "Bash", ExitCode: 0, Duration: 10 * time.Millisecond},
	})
	publishEvent(bus, ctx, &hub.Event{Type: hub.EventMissionConverged, MissionID: missionID, Timestamp: time.Now()})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 6 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 6 {
		t.Fatalf("expected >=6 events, got %d", len(events))
	}
	names := make(map[string]bool)
	corrIDs := make(map[string]int)
	for _, e := range events {
		if m, ok := e["message"].(string); ok {
			names[m] = true
		}
		if c, ok := e["correlation_id"].(string); ok {
			corrIDs[c]++
		}
	}
	if len(names) < 6 {
		t.Errorf("expected >=6 distinct event names, got %d (%v)", len(names), names)
	}
	if len(corrIDs) != 1 {
		t.Errorf("expected single correlation_id, got %d distinct (%v)", len(corrIDs), corrIDs)
	}
	for c := range corrIDs {
		if !strings.Contains(c, missionID) {
			t.Errorf("correlation_id=%q missing mission_id=%q", c, missionID)
		}
	}
}
