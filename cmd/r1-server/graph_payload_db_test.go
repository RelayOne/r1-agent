// Package main — graph_payload_db_test.go
//
// Audit A050: /session/{id}/graph's data island must hydrate from the
// per-session ledger projection (buildGraphPayload was an
// empty-payload stub with the lookup unwired). Runs in the default
// unit-test lane — no e2e tag, no browser.
package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestBuildGraphPayloadFromLedger(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	const sid = "graph-payload-test"
	const n = 40
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("n-%02d", i)
		if err := db.UpsertLedgerNode(sid, id, "task", "m1", "", "test", "", []byte(`{}`)); err != nil {
			t.Fatalf("UpsertLedgerNode: %v", err)
		}
		if i > 0 {
			eid := fmt.Sprintf("e-%02d", i)
			if err := db.UpsertLedgerEdge(sid, eid, id, "n-00", "dep", []byte(`{}`)); err != nil {
				t.Fatalf("UpsertLedgerEdge: %v", err)
			}
		}
	}

	prev := graphPayloadDB
	graphPayloadDB = db
	defer func() { graphPayloadDB = prev }()

	var payload struct {
		Nodes []struct {
			ID    string `json:"id"`
			Shape string `json:"shape"`
			Type  string `json:"node_type"`
		} `json:"nodes"`
		Edges []struct {
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"edges"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(buildGraphPayload(sid, ""), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Nodes) != n {
		t.Errorf("nodes = %d, want %d", len(payload.Nodes), n)
	}
	if len(payload.Edges) != n-1 {
		t.Errorf("edges = %d, want %d", len(payload.Edges), n-1)
	}
	if payload.SessionID != sid {
		t.Errorf("session_id = %q, want %q", payload.SessionID, sid)
	}
	for _, node := range payload.Nodes {
		if node.Shape != "cube" { // shapeForNodeType("task")
			t.Fatalf("node %s shape = %q, want cube", node.ID, node.Shape)
		}
	}

	// Unknown sessions and nil DBs degrade to the empty payload.
	var empty struct {
		Nodes []any `json:"nodes"`
	}
	if err := json.Unmarshal(buildGraphPayload("no-such-session", ""), &empty); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if len(empty.Nodes) != 0 {
		t.Errorf("unknown session should hydrate empty, got %d nodes", len(empty.Nodes))
	}
}

func TestShapeForNodeTypeDeterministicAndValid(t *testing.T) {
	valid := map[string]bool{}
	for _, s := range v2GraphShapes {
		valid[s] = true
	}
	for _, typ := range []string{"", "mission", "task", "worker", "verify", "merge", "note", "loop", "custom-type-xyz"} {
		a, b := shapeForNodeType(typ), shapeForNodeType(typ)
		if a != b {
			t.Errorf("shapeForNodeType(%q) not deterministic: %q vs %q", typ, a, b)
		}
		if !valid[a] {
			t.Errorf("shapeForNodeType(%q) = %q, not a renderer shape pool", typ, a)
		}
	}
}
