//go:build e2e

// Package main — graph_e2e_fixture_test.go
//
// Direct proof for the TestGraph3kFPS fixture seeder (audit A050):
// seeding a temp data dir must produce exactly e2eFixtureNodes ledger
// nodes for the e2e-fixture session and a graph payload the v2 page
// would hydrate 3000 nodes from. Runs under -tags=e2e without any
// browser or Node dependency:
//
//	go test -tags=e2e -run TestSeedGraphFixture ./cmd/r1-server
package main

import (
	"encoding/json"
	"testing"
)

func TestSeedGraphFixture(t *testing.T) {
	dataDir := t.TempDir()
	if err := seedGraphFixture(dataDir, e2eFixtureSessionID, e2eFixtureNodes); err != nil {
		t.Fatalf("seedGraphFixture: %v", err)
	}

	db, err := OpenDB(dataDir)
	if err != nil {
		t.Fatalf("reopen fixture db: %v", err)
	}
	defer db.Close()

	snap, err := db.GetLedger(e2eFixtureSessionID)
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if got := len(snap.Nodes); got != e2eFixtureNodes {
		t.Errorf("seeded nodes = %d, want %d", got, e2eFixtureNodes)
	}
	if got := len(snap.Edges); got != e2eFixtureNodes-1 {
		t.Errorf("seeded edges = %d, want %d (connected topology)", got, e2eFixtureNodes-1)
	}

	// The payload the graph page hydrates from must carry all nodes.
	prev := graphPayloadDB
	graphPayloadDB = db
	defer func() { graphPayloadDB = prev }()
	var payload struct {
		Nodes []struct {
			ID    string `json:"id"`
			Shape string `json:"shape"`
		} `json:"nodes"`
		Edges []struct {
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(buildGraphPayload(e2eFixtureSessionID, ""), &payload); err != nil {
		t.Fatalf("unmarshal graph payload: %v", err)
	}
	if len(payload.Nodes) != e2eFixtureNodes {
		t.Errorf("payload nodes = %d, want %d", len(payload.Nodes), e2eFixtureNodes)
	}
	shapes := map[string]bool{}
	for _, n := range payload.Nodes {
		if n.Shape == "" {
			t.Fatalf("node %s has empty shape", n.ID)
		}
		shapes[n.Shape] = true
	}
	if len(shapes) < 2 {
		t.Errorf("fixture should exercise multiple shape pools, got %v", shapes)
	}
}
