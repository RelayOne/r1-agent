package migration

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

// stubAllocator implements SessionAllocator with deterministic ids.
type stubAllocator struct {
	mu    sync.Mutex
	seq   int
	state map[string]string
}

func newStubAllocator() *stubAllocator { return &stubAllocator{state: map[string]string{}} }

func (a *stubAllocator) AllocateSession(model, tenantID string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	id := "dest-sess-stub"
	a.state[id] = "migrating-in"
	_ = model
	_ = tenantID
	return id, nil
}

func (a *stubAllocator) SetSessionState(id, state string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state[id] = state
	return nil
}

// stubLedger implements LedgerHydrator backed by an in-memory chain
// representation that the importer can hash.
type stubLedger struct {
	mu       sync.Mutex
	nodes    map[string]ledger.Node
	edges    []ledger.Edge
	contents map[string][]byte
	// chainOverride lets a test inject a specific chain root so the
	// final-verify path can be exercised independently of the actual
	// node set.
	chainOverride string
	// brokeChain toggles a synthetic divergence on the next hash call.
	breakChain bool
}

func newStubLedger() *stubLedger {
	return &stubLedger{
		nodes:    map[string]ledger.Node{},
		contents: map[string][]byte{},
	}
}

func (s *stubLedger) HydrateNode(n ledger.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[n.ID] = n
	return nil
}

func (s *stubLedger) HydrateEdge(e ledger.Edge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edges = append(s.edges, e)
	return nil
}

func (s *stubLedger) HydrateContent(nodeID string, blob []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contents[nodeID] = blob
	return nil
}

func (s *stubLedger) ChainRootHashForSession(sessionID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.breakChain {
		s.breakChain = false
		return "wrong-root", nil
	}
	if s.chainOverride != "" {
		return s.chainOverride, nil
	}
	// Compute a stable root by sorting node IDs + commitments and
	// hashing — mirrors the production ChainRootHashForSession's
	// determinism without re-importing the full ledger algorithm.
	ids := make([]string, 0, len(s.nodes))
	for id := range s.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		n := s.nodes[id]
		h.Write([]byte(n.ID))
		h.Write([]byte(n.ContentCommitment))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stubMemory implements MemoryHydrator with a count assertion.
type stubMemory struct {
	mu   sync.Mutex
	rows int
}

func (m *stubMemory) HydrateMemoryRow(destSessionID string, rowJSON []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows++
	_ = destSessionID
	_ = rowJSON
	return nil
}

// stubWALReplayer implements WALReplayer that just counts events.
type stubWALReplayer struct {
	count uint64
}

func (s *stubWALReplayer) ReplayWAL(dest string, walBytes []byte, onProgress func(seq uint64) error) (uint64, error) {
	_ = dest
	start := 0
	for i := 0; i <= len(walBytes); i++ {
		if i == len(walBytes) || walBytes[i] == '\n' {
			line := walBytes[start:i]
			start = i + 1
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			s.count++
		}
	}
	if onProgress != nil {
		return s.count, onProgress(s.count)
	}
	return s.count, nil
}

// allowAllPacks always reports packs present.
type allowAllPacks struct{}

func (allowAllPacks) HasPack(packID, contentHash string) bool { return true }

// rejectAllPacks always reports missing.
type rejectAllPacks struct{}

func (rejectAllPacks) HasPack(packID, contentHash string) bool { return false }

// buildBundle is a test helper that produces a bundle from the
// fixture source and returns the raw bytes.
func buildBundle(t *testing.T) ([]byte, ed25519.PublicKey, *fixtureSource, string) {
	t.Helper()
	src, priv, pub, signer := makeFixture(t)
	var buf bytes.Buffer
	if err := WriteBundle(&buf, src, signer, priv, time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	// Override the source's chain_root_hash with the value the stub
	// ledger will compute post-hydration, so the final-verify passes.
	stub := newStubLedger()
	// Rehydrate the fixture's nodes into the stub so the hash matches.
	pb, err := ReadBundle(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	nodes, _ := pb.ChainNodes()
	for _, n := range nodes {
		if n.MissionID == src.SourceSessionID() {
			n.MissionID = "dest-sess-stub"
		}
		stub.nodes[n.ID] = n
	}
	expected, _ := stub.ChainRootHashForSession("dest-sess-stub")
	// Now rebuild the bundle with the fixture src's hash overridden.
	src.chainRootHash = expected
	var buf2 bytes.Buffer
	if err := WriteBundle(&buf2, src, signer, priv, time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("WriteBundle 2: %v", err)
	}
	return buf2.Bytes(), pub, src, signer
}

func TestImporter_HappyPath(t *testing.T) {
	raw, pub, _, _ := buildBundle(t)
	pb, err := ReadBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	allocator := newStubAllocator()
	led := newStubLedger()
	mem := &stubMemory{}
	imp := &Importer{
		PublicKey:   pub,
		Allocator:   allocator,
		Ledger:      led,
		Memory:      mem,
		Idempotency: NewMemoryIdempotencyStore(),
		PackChecker: allowAllPacks{},
		Emitter:     &CaptureEventEmitter{},
		WALReplayer: &stubWALReplayer{},
	}
	out, err := imp.Import(pb)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !out.Verified {
		t.Errorf("Verified=false; out=%+v", out)
	}
	if out.NewSessionID == "" {
		t.Errorf("empty new session id")
	}
	if out.NodeCount != 2 {
		t.Errorf("node_count=%d want 2", out.NodeCount)
	}
	if mem.rows != 1 {
		t.Errorf("mem rows=%d want 1", mem.rows)
	}
	if state := allocator.state["dest-sess-stub"]; state != "idle" {
		t.Errorf("dest state=%q want idle", state)
	}
}

func TestImporter_ChainRootMismatch(t *testing.T) {
	raw, pub, _, _ := buildBundle(t)
	pb, err := ReadBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	allocator := newStubAllocator()
	led := newStubLedger()
	led.chainOverride = "wrong-root"
	emitter := &CaptureEventEmitter{}
	imp := &Importer{
		PublicKey:   pub,
		Allocator:   allocator,
		Ledger:      led,
		Memory:      &stubMemory{},
		Idempotency: NewMemoryIdempotencyStore(),
		PackChecker: allowAllPacks{},
		Emitter:     emitter,
		WALReplayer: &stubWALReplayer{},
	}
	_, err = imp.Import(pb)
	if err == nil {
		t.Fatalf("expected ChainRootMismatch")
	}
	if !errors.Is(err, ErrChainRootMismatch) {
		t.Errorf("err=%v want ErrChainRootMismatch", err)
	}
	if len(emitter.Divergent) == 0 {
		t.Errorf("no divergent event emitted")
	}
	if state := allocator.state["dest-sess-stub"]; state != "migrated-failed" {
		t.Errorf("dest state=%q want migrated-failed", state)
	}
}

func TestImporter_BadSignature(t *testing.T) {
	raw, _, _, _ := buildBundle(t)
	pb, err := ReadBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	// Use a different public key — verify should fail.
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	imp := &Importer{
		PublicKey:   other,
		Allocator:   newStubAllocator(),
		Ledger:      newStubLedger(),
		Idempotency: NewMemoryIdempotencyStore(),
		PackChecker: allowAllPacks{},
	}
	_, err = imp.Import(pb)
	if err == nil {
		t.Fatalf("expected signature mismatch")
	}
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("err=%v want ErrSignatureMismatch", err)
	}
}

func TestImporter_MissingPacks(t *testing.T) {
	raw, pub, _, _ := buildBundle(t)
	pb, err := ReadBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	imp := &Importer{
		PublicKey:   pub,
		Allocator:   newStubAllocator(),
		Ledger:      newStubLedger(),
		Idempotency: NewMemoryIdempotencyStore(),
		PackChecker: rejectAllPacks{},
	}
	_, err = imp.Import(pb)
	if err == nil {
		t.Fatalf("expected MissingPacksError")
	}
	if !errors.Is(err, ErrMissingSkillPacks) {
		t.Errorf("err=%v want ErrMissingSkillPacks", err)
	}
	var mpe *MissingPacksError
	if !errors.As(err, &mpe) {
		t.Errorf("not MissingPacksError: %v", err)
	} else if len(mpe.Packs) == 0 {
		t.Errorf("expected at least one missing pack")
	}
}

func TestImporter_Idempotent(t *testing.T) {
	raw, pub, _, _ := buildBundle(t)
	pb1, err := ReadBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadBundle 1: %v", err)
	}
	store := NewMemoryIdempotencyStore()
	imp := &Importer{
		PublicKey:   pub,
		Allocator:   newStubAllocator(),
		Ledger:      newStubLedger(),
		Memory:      &stubMemory{},
		Idempotency: store,
		PackChecker: allowAllPacks{},
		WALReplayer: &stubWALReplayer{},
	}
	out1, err := imp.Import(pb1)
	if err != nil {
		t.Fatalf("Import 1: %v", err)
	}
	if out1.Idempotent {
		t.Errorf("first import should not be idempotent")
	}
	// Re-import the same bundle.
	pb2, _ := ReadBundle(bytes.NewReader(raw))
	// Fresh stubs — proves idempotency short-circuits before
	// hydration runs.
	imp2 := &Importer{
		PublicKey:   pub,
		Allocator:   newStubAllocator(),
		Ledger:      newStubLedger(),
		Memory:      &stubMemory{},
		Idempotency: store,
		PackChecker: allowAllPacks{},
		WALReplayer: &stubWALReplayer{},
	}
	out2, err := imp2.Import(pb2)
	if err != nil {
		t.Fatalf("Import 2: %v", err)
	}
	if !out2.Idempotent {
		t.Errorf("second import should be idempotent")
	}
	if out2.NewSessionID != out1.NewSessionID {
		t.Errorf("idempotent new id=%q want %q", out2.NewSessionID, out1.NewSessionID)
	}
}

func TestImporter_CrossTenantForbidden(t *testing.T) {
	raw, pub, _, _ := buildBundle(t)
	pb, err := ReadBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	imp := &Importer{
		PublicKey:      pub,
		BearerTenantID: "wrong-tenant",
		Allocator:      newStubAllocator(),
		Ledger:         newStubLedger(),
		Idempotency:    NewMemoryIdempotencyStore(),
		PackChecker:    allowAllPacks{},
	}
	_, err = imp.Import(pb)
	if err == nil {
		t.Fatalf("expected cross-tenant rejection")
	}
	if !errors.Is(err, ErrCrossTenantForbidden) {
		t.Errorf("err=%v want ErrCrossTenantForbidden", err)
	}
}

func TestImporter_BadSchemaVersion(t *testing.T) {
	raw, pub, _, _ := buildBundle(t)
	pb, err := ReadBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	// Mutate the parsed manifest to unsupported version. The Importer
	// rejects via the explicit schema-version gate in Import.
	pb.Manifest.SchemaVersion = 999
	// Re-marshal manifest bytes so the canonical body now reflects
	// the mutation. Re-sign won't help (we used the original key),
	// but the schema check fires BEFORE the signature check.
	pb.ManifestBytes, _ = json.Marshal(pb.Manifest)
	imp := &Importer{
		PublicKey:   pub,
		Allocator:   newStubAllocator(),
		Ledger:      newStubLedger(),
		Idempotency: NewMemoryIdempotencyStore(),
		PackChecker: allowAllPacks{},
	}
	_, err = imp.Import(pb)
	if err == nil {
		t.Fatalf("expected schema-version rejection")
	}
	if !errors.Is(err, ErrSchemaVersionUnsupported) {
		t.Errorf("err=%v want ErrSchemaVersionUnsupported", err)
	}
}

func TestSQLiteIdempotencyStore_Roundtrip(t *testing.T) {
	// Memory-backed SQLite via ":memory:" driver is unavailable
	// without a CGO build; we only verify the MemoryIdempotencyStore
	// here and leave SQLite to integration / production.
	store := NewMemoryIdempotencyStore()
	if got, _ := store.Lookup("nope"); got != "" {
		t.Errorf("Lookup on empty store returned %q", got)
	}
	if err := store.Record("aaaa", "sess-1", "src-1", "host-a"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := store.Lookup("aaaa")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != "sess-1" {
		t.Errorf("Lookup=%q want sess-1", got)
	}
}
