package remote

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestRegisterSession_Success verifies that RegisterSession POSTs to
// /v1/sessions with the plan_id, decodes session_id + url from the
// response, and stores the sessionID for later calls.
func TestRegisterSession_Success(t *testing.T) {
	var (
		mu          sync.Mutex
		gotMethod   string
		gotPath     string
		gotAuth     string
		gotBodyRaw  []byte
		gotContent  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContent = r.Header.Get("Content-Type")
		gotBodyRaw, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"session_id":"sess-xyz","url":"https://dash.example/s/sess-xyz"}`))
	}))
	defer srv.Close()

	r := &SessionReporter{
		endpoint: srv.URL,
		apiKey:   "secret-key",
		client:   srv.Client(),
	}

	url, err := r.RegisterSession("plan-42")
	if err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	if url != "https://dash.example/s/sess-xyz" {
		t.Errorf("url = %q, want %q", url, "https://dash.example/s/sess-xyz")
	}
	if r.sessionID != "sess-xyz" {
		t.Errorf("sessionID = %q, want %q", r.sessionID, "sess-xyz")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/sessions" {
		t.Errorf("path = %q, want /v1/sessions", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-key")
	}
	if gotContent != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContent)
	}
	var reqBody map[string]string
	if err := json.Unmarshal(gotBodyRaw, &reqBody); err != nil {
		t.Fatalf("request body not valid JSON: %v (%s)", err, string(gotBodyRaw))
	}
	if reqBody["plan_id"] != "plan-42" {
		t.Errorf("request plan_id = %q, want %q", reqBody["plan_id"], "plan-42")
	}
}

// TestRegisterSession_HTTPError verifies the error message when the
// server returns a non-2xx status.
func TestRegisterSession_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden-by-policy"))
	}))
	defer srv.Close()

	r := &SessionReporter{endpoint: srv.URL, apiKey: "k", client: srv.Client()}
	url, err := r.RegisterSession("plan-1")
	if err == nil {
		t.Fatalf("RegisterSession: expected error on 403, got url=%q", url)
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("error = %v, want to contain %q", err, "HTTP 403")
	}
	if !strings.Contains(err.Error(), "forbidden-by-policy") {
		t.Errorf("error = %v, want to contain server body", err)
	}
	if url != "" {
		t.Errorf("url = %q, want empty on error", url)
	}
	if r.sessionID != "" {
		t.Errorf("sessionID = %q, want empty after error", r.sessionID)
	}
}

// TestUpdate_SendsSnapshot verifies Update sends a PUT to
// /v1/sessions/<id>, fills in the SessionID on the body, and accepts
// 204 No Content as success.
func TestUpdate_SendsSnapshot(t *testing.T) {
	var (
		mu        sync.Mutex
		gotMethod string
		gotPath   string
		gotBody   SessionUpdate
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	r := &SessionReporter{
		endpoint:  srv.URL,
		apiKey:    "k",
		sessionID: "sess-abc",
		client:    srv.Client(),
	}

	upd := SessionUpdate{
		PlanID: "plan-9",
		Tasks: []TaskProgress{
			{TaskID: "t1", Description: "build", Phase: "execute", Worker: "claude", CostUSD: 0.42, DurationMs: 1200},
		},
		TotalCostUSD: 0.42,
		BurstWorkers: 3,
	}

	if err := r.Update(upd); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != "PUT" {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/v1/sessions/sess-abc" {
		t.Errorf("path = %q, want /v1/sessions/sess-abc", gotPath)
	}
	if gotBody.SessionID != "sess-abc" {
		t.Errorf("body.session_id = %q, want sess-abc", gotBody.SessionID)
	}
	if gotBody.PlanID != "plan-9" {
		t.Errorf("body.plan_id = %q, want plan-9", gotBody.PlanID)
	}
	if gotBody.TotalCostUSD != 0.42 {
		t.Errorf("body.total_cost_usd = %v, want 0.42", gotBody.TotalCostUSD)
	}
	if gotBody.BurstWorkers != 3 {
		t.Errorf("body.burst_workers = %d, want 3", gotBody.BurstWorkers)
	}
	if len(gotBody.Tasks) != 1 || gotBody.Tasks[0].TaskID != "t1" {
		t.Errorf("body.tasks = %+v, want single task with id t1", gotBody.Tasks)
	}
	if gotBody.UpdatedAt.IsZero() {
		t.Error("body.updated_at should be populated by Update()")
	}
}

// TestUpdate_NoOpWithoutSession confirms Update is a no-op (no HTTP call
// and no error) when the reporter has no sessionID yet.
func TestUpdate_NoOpWithoutSession(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := &SessionReporter{endpoint: srv.URL, apiKey: "k", client: srv.Client()}
	if err := r.Update(SessionUpdate{PlanID: "p"}); err != nil {
		t.Errorf("Update without sessionID should not error, got %v", err)
	}
	if called {
		t.Error("Update should not make HTTP call when sessionID is empty")
	}
}

// TestComplete_PostsStatus verifies Complete sends a typed complete
// request with success + summary and treats 200 as success.
func TestComplete_PostsStatus(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody completeRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := &SessionReporter{
		endpoint:  srv.URL,
		apiKey:    "k",
		sessionID: "sess-done",
		client:    srv.Client(),
	}

	if err := r.Complete(true, "all green"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotBody.Status != "completed" {
		t.Errorf("status = %q, want completed", gotBody.Status)
	}
	if !gotBody.Success {
		t.Error("success = false, want true")
	}
	if gotBody.Summary != "all green" {
		t.Errorf("summary = %q, want %q", gotBody.Summary, "all green")
	}
}

// TestComplete_HTTPError surfaces the server status code in the
// returned error so operators can debug a failed completion.
func TestComplete_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer srv.Close()

	r := &SessionReporter{
		endpoint:  srv.URL,
		apiKey:    "k",
		sessionID: "sess-x",
		client:    srv.Client(),
	}

	err := r.Complete(false, "failed")
	if err == nil {
		t.Fatal("Complete: expected error on 502, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("error = %v, want to contain HTTP 502", err)
	}
	if !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("error = %v, want to contain server body", err)
	}
}
