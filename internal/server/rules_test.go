package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRulesAPIAddListPauseResumeDelete(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	bus := NewEventBus()
	srv := New(0, "", bus)
	RegisterRulesAPI(srv, repo)

	body := bytes.NewBufferString(`{"text":"never call tool delete_branch with name matching ^(staging|dev|prod)$"}`)
	req := httptest.NewRequest(http.MethodPost, "/rules", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /rules status = %d, want %d body=%s", rr.Code, http.StatusCreated, rr.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	ruleID, _ := created["id"].(string)
	if ruleID == "" {
		t.Fatalf("created rule id empty: %v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/rules?format=json", nil)
	req.Header.Set("Accept", "application/json")
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /rules status = %d, want %d", rr.Code, http.StatusOK)
	}

	req = httptest.NewRequest(http.MethodPost, "/rules/"+ruleID+"/pause", nil)
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /rules/:id/pause status = %d, want %d", rr.Code, http.StatusOK)
	}

	req = httptest.NewRequest(http.MethodPost, "/rules/"+ruleID+"/resume", nil)
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /rules/:id/resume status = %d, want %d", rr.Code, http.StatusOK)
	}

	req = httptest.NewRequest(http.MethodDelete, "/rules/"+ruleID, nil)
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE /rules/:id status = %d, want %d", rr.Code, http.StatusOK)
	}
}
