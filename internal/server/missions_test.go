package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RelayOne/r1/internal/convergence"
	"github.com/RelayOne/r1/internal/mission"
	"github.com/RelayOne/r1/internal/orchestrate"
)

func newTestMissionAPI(t *testing.T) (*Server, *orchestrate.Orchestrator) {
	t.Helper()
	bus := NewEventBus()
	srv := New(0, "", bus)

	orch, err := orchestrate.New(orchestrate.Config{
		StoreDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New orchestrator: %v", err)
	}
	t.Cleanup(func() { orch.Close() })

	RegisterMissionAPI(srv, orch)
	return srv, orch
}

func doReq(t *testing.T, handler http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode JSON: %v\nBody: %s", err, w.Body.String())
	}
	return result
}

// --- Create Mission ---

func TestAPICreateMission(t *testing.T) {
	srv, _ := newTestMissionAPI(t)

	w := doReq(t, srv.Handler(), "POST", "/api/missions/create", map[string]interface{}{
		"title":    "JWT Auth",
		"intent":   "Add JWT to API",
		"criteria": []string{"Tokens issued", "401 on invalid"},
	})

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201\nBody: %s", w.Code, w.Body.String())
	}

	result := decodeJSON(t, w)
	if result["id"] == nil || result["id"] == "" {
		t.Error("should return mission ID")
	}
	if result["title"] != "JWT Auth" {
		t.Errorf("title = %v", result["title"])
	}
}

func TestAPICreateMissionValidation(t *testing.T) {
	srv, _ := newTestMissionAPI(t)

	w := doReq(t, srv.Handler(), "POST", "/api/missions/create", map[string]interface{}{
		"title": "", "intent": "test",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestAPICreateMissionWrongMethod(t *testing.T) {
	srv, _ := newTestMissionAPI(t)
	w := doReq(t, srv.Handler(), "GET", "/api/missions/create", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// --- List Missions ---

func TestAPIListMissions(t *testing.T) {
	srv, orch := newTestMissionAPI(t)

	orch.CreateMission("M1", "intent 1", nil)
	orch.CreateMission("M2", "intent 2", nil)

	w := doReq(t, srv.Handler(), "GET", "/api/missions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	result := decodeJSON(t, w)
	count, ok := result["count"].(float64)
	if !ok {
		t.Fatalf("count: unexpected type: %T", result["count"])
	}
	if count != 2 {
		t.Errorf("count = %v, want 2", count)
	}
}

func TestAPIListMissionsFilterPhase(t *testing.T) {
	srv, orch := newTestMissionAPI(t)

	m, _ := orch.CreateMission("M1", "intent", nil)
	orch.CreateMission("M2", "intent", nil)
	orch.AdvanceMission(m.ID, mission.PhaseResearching, "test", "test")

	w := doReq(t, srv.Handler(), "GET", "/api/missions?phase=created", nil)
	result := decodeJSON(t, w)
	count, ok := result["count"].(float64)
	if !ok {
		t.Fatalf("count: unexpected type: %T", result["count"])
	}
	if count != 1 {
		t.Errorf("count = %v, want 1 (only created phase)", count)
	}
}

// --- Get Mission ---

func TestAPIGetMission(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("Test", "test intent", nil)

	w := doReq(t, srv.Handler(), "GET", "/api/missions/get?id="+m.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	result := decodeJSON(t, w)
	if result["title"] != "Test" {
		t.Errorf("title = %v", result["title"])
	}
}

func TestAPIGetMissionNotFound(t *testing.T) {
	srv, _ := newTestMissionAPI(t)
	w := doReq(t, srv.Handler(), "GET", "/api/missions/get?id=ghost", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestAPIGetMissionMissingID(t *testing.T) {
	srv, _ := newTestMissionAPI(t)
	w := doReq(t, srv.Handler(), "GET", "/api/missions/get", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- Advance Mission ---

func TestAPIAdvanceMission(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("Test", "test", nil)

	w := doReq(t, srv.Handler(), "POST", "/api/missions/advance", map[string]interface{}{
		"mission_id": m.ID,
		"phase":      "researching",
		"reason":     "starting research",
		"agent":      "test",
	})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d\nBody: %s", w.Code, w.Body.String())
	}

	got, _ := orch.GetMission(m.ID)
	if got.Phase != mission.PhaseResearching {
		t.Errorf("phase = %s, want researching", got.Phase)
	}
}

func TestAPIAdvanceMissionInvalid(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("Test", "test", nil)

	w := doReq(t, srv.Handler(), "POST", "/api/missions/advance", map[string]interface{}{
		"mission_id": m.ID,
		"phase":      "completed", // invalid from created
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- Convergence Status ---

func TestAPIConvergenceStatus(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("Test", "test", []string{"Done"})

	w := doReq(t, srv.Handler(), "GET", "/api/missions/convergence?id="+m.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	result := decodeJSON(t, w)
	if result["is_converged"] != false {
		t.Error("should not be converged with unsatisfied criteria")
	}
}

// --- Build Context ---

func TestAPIBuildContext(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("JWT Auth", "Add JWT auth", []string{"Tokens issued"})

	w := doReq(t, srv.Handler(), "POST", "/api/missions/context", map[string]interface{}{
		"mission_id": m.ID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d\nBody: %s", w.Code, w.Body.String())
	}

	result := decodeJSON(t, w)
	ctx, ok := result["context"].(string)
	if !ok {
		t.Fatalf("context: unexpected type: %T", result["context"])
	}
	if ctx == "" {
		t.Error("context should not be empty")
	}
	if !contains(ctx, "JWT Auth") {
		t.Error("context should contain mission title")
	}
}

// --- Record Handoff ---

func TestAPIRecordHandoff(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("Test", "test", []string{"Done"})

	w := doReq(t, srv.Handler(), "POST", "/api/missions/handoff", map[string]interface{}{
		"mission_id": m.ID,
		"from_agent": "agent-1",
		"to_agent":   "agent-2",
		"summary":    "Implemented feature X",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d\nBody: %s", w.Code, w.Body.String())
	}
}

// --- Record Consensus ---

func TestAPIRecordConsensus(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("Test", "test", nil)

	w := doReq(t, srv.Handler(), "POST", "/api/missions/consensus", map[string]interface{}{
		"mission_id": m.ID,
		"model":      "claude",
		"verdict":    "complete",
		"reasoning":  "all good",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d\nBody: %s", w.Code, w.Body.String())
	}

	result := decodeJSON(t, w)
	if result["has_consensus"] != false {
		t.Error("should not have consensus with 1 vote")
	}
}

// --- Findings ---

func TestAPIFindingsEmpty(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("Test", "test", nil)

	w := doReq(t, srv.Handler(), "GET", "/api/missions/findings?id="+m.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d\nBody: %s", w.Code, w.Body.String())
	}

	result := decodeJSON(t, w)
	count, ok := result["count"].(float64)
	if !ok {
		t.Fatalf("count: unexpected type: %T", result["count"])
	}
	if count != 0 {
		t.Errorf("count = %v, want 0 (no findings yet)", count)
	}
}

func TestAPIFindingsMissingID(t *testing.T) {
	srv, _ := newTestMissionAPI(t)
	w := doReq(t, srv.Handler(), "GET", "/api/missions/findings", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestAPIFindingsWrongMethod(t *testing.T) {
	srv, _ := newTestMissionAPI(t)
	w := doReq(t, srv.Handler(), "POST", "/api/missions/findings", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestAPIFindingsAfterConvergenceRun(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("Test", "test", []string{"Feature works"})

	// Run convergence directly via orchestrator (avoids base64 encoding issues)
	report, err := orch.RunConvergence(m.ID, []convergence.FileInput{
		{Path: "main.go", Content: []byte("// TODO: implement this\nfunc main() {}")},
	})
	if err != nil {
		t.Fatalf("RunConvergence: %v", err)
	}
	t.Logf("convergence: converged=%v, findings=%d", report.IsConverged, len(report.Findings))

	// Now check findings endpoint
	w := doReq(t, srv.Handler(), "GET", "/api/missions/findings?id="+m.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	result := decodeJSON(t, w)
	count, ok := result["count"].(float64)
	if !ok {
		t.Fatalf("count: unexpected type: %T", result["count"])
	}
	// The convergence run should have stored gaps from its findings
	if len(report.Findings) > 0 && count == 0 {
		t.Error("convergence produced findings but findings endpoint returned none")
	}
	if len(report.Findings) > 0 {
		t.Logf("findings endpoint returned %v findings", count)
	}
}

func TestAPIFindingsFilterSeverity(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("Test", "test", []string{"Feature works"})

	// Run convergence to populate gaps
	orch.RunConvergence(m.ID, []convergence.FileInput{
		{Path: "main.go", Content: []byte("// TODO: implement\nfunc main() {}")},
	})

	// Filter by blocking severity only
	w := doReq(t, srv.Handler(), "GET", "/api/missions/findings?id="+m.ID+"&severity=blocking", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	result := decodeJSON(t, w)
	findings := result["findings"]
	if findings != nil {
		list, ok := findings.([]interface{})
		if !ok {
			t.Fatalf("findings: unexpected type: %T", findings)
		}
		for _, f := range list {
			finding, ok := f.(map[string]interface{})
			if !ok {
				t.Fatalf("finding entry: unexpected type: %T", f)
			}
			if finding["severity"] != "blocking" {
				t.Errorf("found non-blocking finding when filtering: %v", finding["severity"])
			}
		}
	}
}

func TestAPIFindingsFilterCategory(t *testing.T) {
	srv, orch := newTestMissionAPI(t)
	m, _ := orch.CreateMission("Test", "test", []string{"Feature works"})

	orch.RunConvergence(m.ID, []convergence.FileInput{
		{Path: "main.go", Content: []byte("// TODO: implement\nfunc main() {}")},
	})

	w := doReq(t, srv.Handler(), "GET", "/api/missions/findings?id="+m.ID+"&category=test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	result := decodeJSON(t, w)
	findings := result["findings"]
	if findings != nil {
		list, ok := findings.([]interface{})
		if !ok {
			t.Fatalf("findings: unexpected type: %T", findings)
		}
		for _, f := range list {
			finding, ok := f.(map[string]interface{})
			if !ok {
				t.Fatalf("finding entry: unexpected type: %T", f)
			}
			if finding["category"] != "test" {
				t.Errorf("found non-test finding when filtering: %v", finding["category"])
			}
		}
	}
}

// --- RegisterMissionAPI with nil ---

func TestRegisterMissionAPINilOrch(t *testing.T) {
	srv := New(0, "", NewEventBus())
	// Should not panic
	RegisterMissionAPI(srv, nil)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
