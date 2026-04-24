package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/costtrack"
)

func newTestDashboardServer() (*Server, *DashboardState) {
	bus := NewEventBus()
	srv := New(0, "", bus)
	state := NewDashboardState()
	cost := costtrack.NewTracker(10.0, nil)
	RegisterDashboardAPI(srv, cost, nil, state)
	return srv, state
}

func TestDashboardTasksEmpty(t *testing.T) {
	srv, _ := newTestDashboardServer()
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/tasks", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	count, ok := resp["count"].(float64)
	if !ok {
		t.Fatalf("count: unexpected type: %T", resp["count"])
	}
	if int(count) != 0 {
		t.Errorf("count=%v, want 0", resp["count"])
	}
}

func TestDashboardTasksWithData(t *testing.T) {
	srv, state := newTestDashboardServer()
	state.Update(TaskSnapshot{ID: "t1", Status: "running", Phase: "execute"})
	state.Update(TaskSnapshot{ID: "t2", Status: "completed", Phase: "verify"})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/tasks", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	count, ok := resp["count"].(float64)
	if !ok {
		t.Fatalf("count: unexpected type: %T", resp["count"])
	}
	if int(count) != 2 {
		t.Errorf("count=%v, want 2", resp["count"])
	}
}

func TestDashboardTasksFilterByStatus(t *testing.T) {
	srv, state := newTestDashboardServer()
	state.Update(TaskSnapshot{ID: "t1", Status: "running"})
	state.Update(TaskSnapshot{ID: "t2", Status: "completed"})
	state.Update(TaskSnapshot{ID: "t3", Status: "running"})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/tasks?status=running", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	count, ok := resp["count"].(float64)
	if !ok {
		t.Fatalf("count: unexpected type: %T", resp["count"])
	}
	if int(count) != 2 {
		t.Errorf("count=%v, want 2", resp["count"])
	}
}

func TestDashboardTaskGet(t *testing.T) {
	srv, state := newTestDashboardServer()
	state.Update(TaskSnapshot{ID: "t1", Status: "running", Phase: "execute", CostUSD: 0.05})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/tasks/get?id=t1", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var snap TaskSnapshot
	json.NewDecoder(w.Body).Decode(&snap)
	if snap.ID != "t1" || snap.Status != "running" {
		t.Errorf("snap=%+v", snap)
	}
}

func TestDashboardTaskGetNotFound(t *testing.T) {
	srv, _ := newTestDashboardServer()
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/tasks/get?id=nope", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

func TestDashboardCost(t *testing.T) {
	srv, _ := newTestDashboardServer()
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/cost", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["total_usd"]; !ok {
		t.Error("missing total_usd field")
	}
	if _, ok := resp["tokens"]; !ok {
		t.Error("missing tokens field")
	}
}

func TestDashboardPools(t *testing.T) {
	srv, _ := newTestDashboardServer()
	// pools is nil in this test, should return empty
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/pools", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	count, ok := resp["count"].(float64)
	if !ok {
		t.Fatalf("count: unexpected type: %T", resp["count"])
	}
	if int(count) != 0 {
		t.Errorf("count=%v, want 0", resp["count"])
	}
}

func TestDashboardSummary(t *testing.T) {
	srv, state := newTestDashboardServer()
	state.Update(TaskSnapshot{ID: "t1", Status: "running"})
	state.Update(TaskSnapshot{ID: "t2", Status: "completed"})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/summary", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	tasks, ok := resp["tasks"].(map[string]interface{})
	if !ok {
		t.Fatalf("tasks: unexpected type: %T", resp["tasks"])
	}
	total, ok := tasks["total"].(float64)
	if !ok {
		t.Fatalf("tasks.total: unexpected type: %T", tasks["total"])
	}
	if int(total) != 2 {
		t.Errorf("total=%v, want 2", tasks["total"])
	}
}

func TestDashboardStateUpdate(t *testing.T) {
	state := NewDashboardState()

	state.Update(TaskSnapshot{ID: "t1", Status: "pending"})
	snap := state.Get("t1")
	if snap == nil || snap.Status != "pending" {
		t.Fatal("expected pending snapshot")
	}

	// Update existing task.
	state.Update(TaskSnapshot{ID: "t1", Status: "running", Phase: "execute"})
	snap = state.Get("t1")
	if snap.Status != "running" || snap.Phase != "execute" {
		t.Errorf("after update: %+v", snap)
	}

	// Verify UpdatedAt is recent.
	if time.Since(snap.UpdatedAt) > time.Second {
		t.Errorf("updated_at too old: %v", snap.UpdatedAt)
	}
}

func TestDashboardStateGetNotFound(t *testing.T) {
	state := NewDashboardState()
	if snap := state.Get("nope"); snap != nil {
		t.Errorf("expected nil, got %+v", snap)
	}
}

func TestComputeAcceptKey(t *testing.T) {
	// RFC 6455 Section 4.2.2 example.
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	got := computeAcceptKey(key)
	if got != want {
		t.Errorf("accept key = %q, want %q", got, want)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{500, "500ms"},
		{1500, "1.5s"},
		{90000, "1.5m"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.ms)
		if got != tt.want {
			t.Errorf("formatDuration(%d) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}
