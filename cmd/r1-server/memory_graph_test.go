// Package main — memory_graph_test.go
//
// Spec 4 §6.2 + §10 T11: memory-scoped graph view.
package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeMemoryGraph_404OnFlagOff(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	req := httptest.NewRequest("GET", "/memories/m-1/graph", nil)
	req.SetPathValue("id", "m-1")
	rec := httptest.NewRecorder()
	serveMemoryGraph(rec, req)
	if rec.Code != 404 {
		t.Errorf("flag off: status = %d, want 404", rec.Code)
	}
}

func TestServeMemoryGraph_404OnEmptyID(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	req := httptest.NewRequest("GET", "/memories//graph", nil)
	req.SetPathValue("id", "")
	rec := httptest.NewRecorder()
	serveMemoryGraph(rec, req)
	if rec.Code != 404 {
		t.Errorf("empty id: status = %d, want 404", rec.Code)
	}
}

func TestServeMemoryGraph_HydratesPayloadWithMemoryFilter(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	req := httptest.NewRequest("GET", "/memories/m-target/graph", nil)
	req.SetPathValue("id", "m-target")
	rec := httptest.NewRecorder()
	serveMemoryGraph(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"filter":"memory"`,
		`"memory_id":"m-target"`,
		// Title threaded through V2BaseContext
		`Memory m-target — Graph`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestServeSessionGraph_PassesMemoryQueryParamThrough(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	req := httptest.NewRequest("GET", "/session/sess-x/graph?memory_id=m-7", nil)
	req.SetPathValue("id", "sess-x")
	rec := httptest.NewRecorder()
	serveSessionGraph(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"memory_id":"m-7"`) {
		t.Errorf("body should propagate memory_id query param into the graph payload\n--- body ---\n%s", body)
	}
}

func TestServeSessionGraph_NoMemoryParamRendersBareGraph(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	req := httptest.NewRequest("GET", "/session/sess-y/graph", nil)
	req.SetPathValue("id", "sess-y")
	rec := httptest.NewRecorder()
	serveSessionGraph(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `"memory_id"`) {
		t.Errorf("body should NOT include memory_id when query param absent\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, `"session_id":"sess-y"`) {
		t.Errorf("body should include session_id\n--- body ---\n%s", body)
	}
}
