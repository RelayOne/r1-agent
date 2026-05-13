//go:build integration_session_migrate

// integration_roundtrip_test.go — spec C1 §T22.
//
// 1000-turn round-trip integration test across two in-process
// daemons. Build with `-tags integration_session_migrate` to opt in:
//
//   go test -count=1 -tags integration_session_migrate ./internal/migration/...
//
// The test is heavy (allocates a full ledger.Store, a full bus.WAL,
// 1000 events, signs + verifies, replays) and is not part of the
// default unit-test gate; CI runs it separately.

package migration

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

// TestIntegration_RoundTrip1000Turns seeds a source-side ledger.Store
// + bus WAL bytes with 1000 turns, exports a .r1session bundle,
// imports it into a destination-side stub stack, and asserts:
//
//   - destination's ChainRootHashForSession byte-equals source's
//   - total wall-clock <60s (per spec §12 acceptance criterion)
//   - bundle size <100MB (per same acceptance criterion)
//
// The destination's ledger is a fresh ledger.Store rather than the
// SQLite projection used by cmd/r1-server — this keeps the
// integration test focused on the migration package's correctness
// rather than the cmd/r1-server adapter's. The full daemon-to-
// daemon HTTP round-trip is exercised by separate cmd/r1-server
// integration tests (out of scope here).
func TestIntegration_RoundTrip1000Turns(t *testing.T) {
	// The -tags integration_session_migrate build tag is the gate;
	// no need to additionally gate on -short. The test runs only when
	// the operator opts in via the tag (matches the spec's "author
	// it but don't run live" instruction — it's wired into CI but
	// out of the default unit-test gate).
	start := time.Now()
	const numTurns = 1000
	sessionID := "integration-source-sess"

	// Source-side ledger.
	srcRoot := t.TempDir()
	srcStore, err := ledger.NewStore(filepath.Join(srcRoot, "ledger"))
	if err != nil {
		t.Fatalf("source ledger: %v", err)
	}

	// Build a deterministic chain of 1000 nodes.
	now := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	for i := 0; i < numTurns; i++ {
		n := ledger.Node{
			ID:                fmt.Sprintf("node-%04d", i),
			Type:              "test",
			SchemaVersion:     1,
			CreatedAt:         now.Add(time.Duration(i) * time.Second),
			CreatedBy:         "integration",
			MissionID:         sessionID,
			Content:           json.RawMessage(fmt.Sprintf(`{"turn":%d}`, i)),
			ContentCommitment: fmt.Sprintf("commit-%04d", i),
		}
		if err := srcStore.WriteNode(n); err != nil {
			t.Fatalf("write node %d: %v", i, err)
		}
	}

	// Source chain root.
	srcRoot1, err := srcStore.ChainRootHashForSession(sessionID)
	if err != nil {
		t.Fatalf("source chain root: %v", err)
	}
	if srcRoot1 == "" {
		t.Fatalf("empty source chain root")
	}

	// Build the bundle source. WAL bytes are kept small (one event
	// per 10 turns) to keep memory bounded.
	walBuf := &bytes.Buffer{}
	for i := 0; i < numTurns; i += 10 {
		line, _ := json.Marshal(map[string]any{
			"id":         fmt.Sprintf("ev-%04d", i),
			"type":       "test.event",
			"sequence":   i + 1,
			"emitter_id": "integration",
			"scope":      map[string]any{"mission_id": sessionID},
		})
		walBuf.Write(line)
		walBuf.WriteByte('\n')
	}

	src := &LedgerBundleSource{
		HostID:              "source-host",
		DaemonID:            "source-daemon",
		SessionID:           sessionID,
		TenantIDValue:       "tenant-default",
		ModelID:             "integration-model",
		Store:               srcStore,
		WALBytesValue:       walBuf.Bytes(),
		WALFirstSeqValue:    1,
		WALLastSeqValue:     uint64(numTurns),
		WALCheckpointsValue: nil,
		MemoryRowsValue:     []byte{},
		MemoryRowCountValue: 0,
		MemoryTargetsValue:  nil,
		SkillPackRefsValue:  []SkillPackRef{},
		LobeStatesValue:     map[string][]byte{},
		LanesSnapshotValue:  []byte(`{}`),
		CheckpointValue:     []byte(`{}`),
	}

	// Signer.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer := SignerFingerHex(pub)

	// Write the bundle.
	var bundle bytes.Buffer
	if err := WriteBundle(&bundle, src, signer, priv, time.Now().UTC()); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	bundleSize := bundle.Len()
	if bundleSize > 100*1024*1024 {
		t.Errorf("bundle size %d > 100MB", bundleSize)
	}

	// Save the bundle to a file so we exercise the disk-round-trip
	// path the operator's `scp ; r1 session import` workflow uses.
	bundlePath := filepath.Join(t.TempDir(), sessionID+".r1session")
	if err := os.WriteFile(bundlePath, bundle.Bytes(), 0o600); err != nil {
		t.Fatalf("save bundle: %v", err)
	}

	// Re-read for import.
	bundleBytes, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("re-read bundle: %v", err)
	}

	// Destination-side ledger + stubs.
	destRoot := t.TempDir()
	destStore, err := ledger.NewStore(filepath.Join(destRoot, "ledger"))
	if err != nil {
		t.Fatalf("dest ledger: %v", err)
	}
	destLedger := &liveLedgerHydrator{store: destStore}
	pb, err := ReadBundle(bytes.NewReader(bundleBytes))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}

	allocator := newStubAllocator()
	importer := &Importer{
		PublicKey:   pub,
		Allocator:   allocator,
		Ledger:      destLedger,
		Idempotency: NewMemoryIdempotencyStore(),
		PackChecker: allowAllPacks{},
		Emitter:     &CaptureEventEmitter{},
		WALReplayer: &stubWALReplayer{},
	}
	out, err := importer.Import(pb)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !out.Verified {
		t.Errorf("destination not verified: %+v", out)
	}
	if out.NodeCount != numTurns {
		t.Errorf("dest node_count=%d want %d", out.NodeCount, numTurns)
	}

	// Final assertion: destination chain root byte-equals source's.
	// We query via the destination session id the allocator minted
	// — the importer rewrites MissionID from source-id to dest-id,
	// and ChainRootHashForSession filters by MissionID. Hash equality
	// holds because the hash algorithm itself does NOT include
	// MissionID in its hash input (see ledger/store_session.go).
	destRootHash, err := destStore.ChainRootHashForSession(out.NewSessionID)
	if err != nil {
		t.Fatalf("dest chain root: %v", err)
	}
	destNodes, _ := destStore.ListNodesForSession(out.NewSessionID)
	if destRootHash != srcRoot1 {
		t.Errorf("chain root mismatch:\n  source=%s\n  dest  =%s\n  dest_node_count=%d", srcRoot1, destRootHash, len(destNodes))
	}
	if len(destNodes) != numTurns {
		t.Errorf("dest persisted %d nodes; expected %d", len(destNodes), numTurns)
	}

	// Wall-clock budget.
	elapsed := time.Since(start)
	if elapsed > 60*time.Second {
		t.Errorf("wall-clock %s > 60s", elapsed)
	}
	t.Logf("integration round-trip: %d turns / %d byte bundle / %s", numTurns, bundleSize, elapsed)
}

// liveLedgerHydrator wires a real ledger.Store into the importer's
// LedgerHydrator interface — used only by this integration test.
//
// Why the byte-equal hash works without restoring MissionID:
// ChainRootHashForSession's algorithm (see store_session.go) hashes
// over (prev_hash || node_id || content_commitment) — MissionID is
// NOT part of the hash input. So the importer's normal MissionID
// rewrite (source-id → dest-id) is harmless for the hash equality
// assertion; we let the importer do its real job and query the
// destination's allocated session id directly.
type liveLedgerHydrator struct {
	store *ledger.Store
}

// HydrateNode implements LedgerHydrator. Passes the node through
// verbatim — including the importer's MissionID rewrite. This is
// the production hydration path; the integration test exercises it
// unchanged.
func (h *liveLedgerHydrator) HydrateNode(n ledger.Node) error {
	return h.store.WriteNode(n)
}

// HydrateEdge implements LedgerHydrator.
func (h *liveLedgerHydrator) HydrateEdge(e ledger.Edge) error {
	return h.store.WriteEdge(e)
}

// HydrateContent implements LedgerHydrator.
func (h *liveLedgerHydrator) HydrateContent(nodeID string, blob []byte) error {
	return h.store.WriteContentBlob(nodeID, blob)
}

// ChainRootHashForSession implements LedgerHydrator. Delegates to
// the real store — no override. The test queries via the destination
// session id the allocator minted, which is what the importer also
// passes in the final-verify call.
func (h *liveLedgerHydrator) ChainRootHashForSession(sessionID string) (string, error) {
	return h.store.ChainRootHashForSession(sessionID)
}
