//go:build e2e

// Package main — graph_e2e_fixture.go
//
// Spec 2 §6 + checklist T11: the 3000-node fixture seeder for
// TestGraph3kFPS (graph_e2e_test.go). Behind the e2e build tag so the
// shipped binary and the default `go test ./...` lane never compile
// it; the release-rehearsal lane builds the test binary with -tags=e2e
// and the test seeds a temp R1_DATA_DIR before starting the server.
//
// The seed reuses the same DB machinery the import path uses
// (OpenDB + UpsertSession + UpsertLedgerNode/UpsertLedgerEdge), so
// the fixture exercises the exact ledger projection
// /session/{id}/graph hydrates its data island from (audit A050).

package main

import (
	"fmt"
	"time"

	"github.com/RelayOne/r1/internal/session"
)

// e2eFixtureSessionID matches the default session id graph-fps.mjs
// navigates to (its R1_SERVER_E2E_SESSION_ID fallback).
const e2eFixtureSessionID = "e2e-fixture"

// fixtureNodeTypes cycles through ledger-ish node types so the seeded
// graph exercises multiple InstancedMesh shape pools, not one.
var fixtureNodeTypes = []string{
	"mission", "task", "worker", "verify", "merge",
	"note", "loop", "skill", "artifact", "review", "snapshot",
}

// seedGraphFixture creates the server DB under dataDir and seeds a
// completed session with n ledger nodes plus a connected edge set:
// every node links to its cluster hub (one hub per 250 nodes) and
// hubs chain together, so the force layout has real structure to
// relax instead of a disconnected point cloud.
func seedGraphFixture(dataDir, sessionID string, n int) error {
	db, err := OpenDB(dataDir)
	if err != nil {
		return fmt.Errorf("open fixture db: %w", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	if err := db.UpsertSession(session.SignatureFile{
		Version:    "1",
		InstanceID: sessionID,
		StartedAt:  now.Add(-time.Hour),
		UpdatedAt:  now,
		RepoRoot:   "/fixtures/e2e",
		Mode:       "e2e-fixture",
		Status:     "completed",
	}); err != nil {
		return fmt.Errorf("seed session: %w", err)
	}

	for i := 0; i < n; i++ {
		nodeID := fmt.Sprintf("fixture-node-%04d", i)
		nodeType := fixtureNodeTypes[i%len(fixtureNodeTypes)]
		raw := []byte(fmt.Sprintf(`{"id":%q,"type":%q,"seq":%d}`, nodeID, nodeType, i))
		createdAt := now.Add(time.Duration(i-n) * time.Second).Format(time.RFC3339Nano)
		if err := db.UpsertLedgerNode(sessionID, nodeID, nodeType, "e2e-mission", createdAt, "fixture", "", raw); err != nil {
			return fmt.Errorf("seed node %d: %w", i, err)
		}
		if i == 0 {
			continue
		}
		hub := (i / 250) * 250
		target := hub
		if i == hub {
			target = hub - 250 // chain hubs together
		}
		edgeID := fmt.Sprintf("fixture-edge-%04d", i)
		from := nodeID
		to := fmt.Sprintf("fixture-node-%04d", target)
		eraw := []byte(fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"type":"fixture"}`, edgeID, from, to))
		if err := db.UpsertLedgerEdge(sessionID, edgeID, from, to, "fixture", eraw); err != nil {
			return fmt.Errorf("seed edge %d: %w", i, err)
		}
	}
	return nil
}
