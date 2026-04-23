package main

// import_test.go — coverage for `r1-server import` (work-stoke T16).
//
// We build a fresh .tracebundle from primitives (no dependency on the
// exporter package) so this test lives entirely inside cmd/r1-server,
// then exercise ImportTraceBundle / runImportCmd end-to-end against a
// throwaway SQLite DB.
//
// Invariants covered:
//   - Valid bundle → session row + events + ledger nodes/edges land.
//   - Merkle-root mismatch → hard error, nothing ingested beyond the
//     session upsert (which happens after verification).
//   - Re-importing the same bundle is idempotent (counts stable).
//   - Manifest missing / format != tracebundle → error.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// bundleFileTest is the test's local mirror of the exporter's
// bundleFile. Decoupled on purpose so this file compiles without
// importing cmd/stoke.
type bundleFileTest struct {
	Path string
	Data []byte
}

// buildTestBundle writes a valid .tracebundle to a tmp path and
// returns (bundlePath, merkleRoot). Callers can override sessionID
// and tamper with the manifest before writing.
func buildTestBundle(t *testing.T, sessionID string) (string, string) {
	t.Helper()

	files := []bundleFileTest{
		{Path: "stream.jsonl", Data: []byte(`{"type":"stoke.session.start","ts":"2026-04-22T00:00:00Z"}` + "\n" +
			`{"type":"stoke.task.start","ts":"2026-04-22T00:00:01Z"}` + "\n")},
		{Path: "checkpoints/checkpoints.jsonl", Data: []byte(`{"checkpoint":1}` + "\n")},
		{Path: "ledger/nodes/node1.json", Data: []byte(`{"id":"node1","type":"decision","created_at":"2026-04-22T00:00:00Z"}`)},
		{Path: "ledger/nodes/node2.json", Data: []byte(`{"id":"node2","type":"action","created_at":"2026-04-22T00:00:01Z"}`)},
		{Path: "ledger/edges/edge1.json", Data: []byte(`{"id":"edge1","from":"node1","to":"node2","type":"causes"}`)},
		{Path: "ledger/chain.txt", Data: []byte("hashchain-line-1\n")},
		{Path: "memory.json", Data: []byte(`{"session_id":"` + sessionID + `","rows":[{"key":"k","content":"v"}],"count":1}`)},
	}

	// Compute digests + merkle root exactly like the exporter.
	type entry struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
	}
	entries := make([]entry, 0, len(files))
	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		entries = append(entries, entry{
			Path:   f.Path,
			Size:   int64(len(f.Data)),
			Digest: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	h := sha256.New()
	for _, e := range entries {
		h.Write([]byte(e.Path))
		h.Write([]byte{'\t'})
		h.Write([]byte(e.Digest))
		h.Write([]byte{'\n'})
	}
	root := hex.EncodeToString(h.Sum(nil))

	manifest := map[string]any{
		"format":      "tracebundle",
		"version":     "1",
		"session_id":  sessionID,
		"repo_root":   "/tmp/test-repo",
		"mode":        "test",
		"status":      "completed",
		"created_at":  time.Now().UTC().Format(time.RFC3339Nano),
		"files":       entries,
		"merkle_root": root,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	bundlePath := filepath.Join(t.TempDir(), sessionID+"-"+root[:12]+".tracebundle")
	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, bf := range files {
		w, err := zw.Create(bf.Path)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(bf.Data); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	mw, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatalf("zip create manifest: %v", err)
	}
	if _, err := mw.Write(manifestBytes); err != nil {
		t.Fatalf("zip write manifest: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return bundlePath, root
}

func TestImportTraceBundle_Happy(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	bundlePath, wantRoot := buildTestBundle(t, "r1-imported")
	summary, err := ImportTraceBundle(db, bundlePath)
	if err != nil {
		t.Fatalf("ImportTraceBundle: %v", err)
	}
	if summary.SessionID != "r1-imported" {
		t.Errorf("session_id = %q", summary.SessionID)
	}
	if summary.MerkleRoot != wantRoot {
		t.Errorf("merkle root = %q, want %q", summary.MerkleRoot, wantRoot)
	}
	if summary.Events != 2 {
		t.Errorf("events = %d, want 2", summary.Events)
	}
	if summary.Nodes != 2 {
		t.Errorf("nodes = %d, want 2", summary.Nodes)
	}
	if summary.Edges != 1 {
		t.Errorf("edges = %d, want 1", summary.Edges)
	}
	if summary.Memories != 1 {
		t.Errorf("memories = %d, want 1", summary.Memories)
	}

	// Session row landed.
	row, err := db.GetSession("r1-imported")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.Status != "completed" {
		t.Errorf("status = %q, want completed", row.Status)
	}
	if row.Mode != "test" {
		t.Errorf("mode = %q, want test", row.Mode)
	}

	// Events are queryable.
	events, err := db.ListEvents("r1-imported", 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("ListEvents = %d, want 2", len(events))
	}

	// Ledger graph is queryable.
	snap, err := db.GetLedger("r1-imported")
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if len(snap.Nodes) != 2 {
		t.Errorf("ledger nodes = %d, want 2", len(snap.Nodes))
	}
	if len(snap.Edges) != 1 {
		t.Errorf("ledger edges = %d, want 1", len(snap.Edges))
	}

	// Memory rows landed in stoke_memory_bus — the ingester writes
	// through to the same table the /memories handler queries, so
	// imported rows are visible alongside live ones.
	var memRows int
	var gotKey, gotContent string
	if err := db.sql.QueryRow(
		`SELECT COUNT(1) FROM stoke_memory_bus WHERE session_id = ?`,
		"r1-imported",
	).Scan(&memRows); err != nil {
		t.Fatalf("count membus rows: %v", err)
	}
	if memRows != 1 {
		t.Errorf("stoke_memory_bus rows = %d, want 1", memRows)
	}
	if err := db.sql.QueryRow(
		`SELECT key, content FROM stoke_memory_bus WHERE session_id = ?`,
		"r1-imported",
	).Scan(&gotKey, &gotContent); err != nil {
		t.Fatalf("select membus row: %v", err)
	}
	if gotKey != "k" || gotContent != "v" {
		t.Errorf("membus row = (%q,%q), want (\"k\",\"v\")", gotKey, gotContent)
	}
}

func TestImportTraceBundle_Idempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	bundlePath, _ := buildTestBundle(t, "r1-idemp")

	for i := 0; i < 3; i++ {
		s, err := ImportTraceBundle(db, bundlePath)
		if err != nil {
			t.Fatalf("import round %d: %v", i, err)
		}
		if s.Nodes != 2 || s.Edges != 1 {
			t.Errorf("round %d: nodes=%d edges=%d", i, s.Nodes, s.Edges)
		}
	}
	snap, err := db.GetLedger("r1-idemp")
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if len(snap.Nodes) != 2 || len(snap.Edges) != 1 {
		t.Errorf("after 3 imports: nodes=%d edges=%d, want 2 and 1",
			len(snap.Nodes), len(snap.Edges))
	}

	// Memory rows must also be idempotent — the ON CONFLICT clause
	// on (scope, scope_target, key) upserts in place rather than
	// duplicating. One row in after three rounds.
	var memRows int
	if err := db.sql.QueryRow(
		`SELECT COUNT(1) FROM stoke_memory_bus WHERE session_id = ?`,
		"r1-idemp",
	).Scan(&memRows); err != nil {
		t.Fatalf("count membus rows: %v", err)
	}
	if memRows != 1 {
		t.Errorf("stoke_memory_bus rows after 3 imports = %d, want 1", memRows)
	}
}

func TestImportTraceBundle_MerkleTamper(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	bundlePath, _ := buildTestBundle(t, "r1-tamper")

	// Rewrite the zip with one file's content mutated but the
	// manifest left untouched. The verifier should reject this.
	zr, err := zip.OpenReader(bundlePath)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	tampered := filepath.Join(filepath.Dir(bundlePath), "tampered.tracebundle")
	out, err := os.Create(tampered)
	if err != nil {
		t.Fatalf("create tampered: %v", err)
	}
	zw := zip.NewWriter(out)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry: %v", err)
		}
		buf, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read entry: %v", err)
		}
		if f.Name == "stream.jsonl" {
			buf = append(buf, []byte(`{"type":"sneaky.injected"}`+"\n")...)
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(buf); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	zw.Close()
	out.Close()
	zr.Close()

	if _, err := ImportTraceBundle(db, tampered); err == nil {
		t.Fatal("expected error on tampered bundle, got nil")
	}
}

func TestImportTraceBundle_MissingManifest(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	bogus := filepath.Join(t.TempDir(), "no-manifest.tracebundle")
	f, err := os.Create(bogus)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("stream.jsonl")
	w.Write([]byte("not a manifest\n"))
	zw.Close()
	f.Close()

	if _, err := ImportTraceBundle(db, bogus); err == nil {
		t.Fatal("expected error on manifest-less bundle, got nil")
	}
}

func TestRunImportCmd_CLI(t *testing.T) {
	bundlePath, _ := buildTestBundle(t, "r1-cli")
	dataDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := runImportCmd([]string{"--data-dir", dataDir, bundlePath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runImportCmd = %d, stderr=%q", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("session_id=r1-cli")) {
		t.Errorf("stdout missing session_id: %q", stdout.String())
	}
}

func TestRunImportCmd_Usage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runImportCmd(nil, &stdout, &stderr); code != 2 {
		t.Errorf("no-args exit = %d, want 2 (stderr=%q)", code, stderr.String())
	}
}

func TestRunImportCmd_MissingBundle(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runImportCmd([]string{"--data-dir", t.TempDir(), "/nonexistent/bundle.tracebundle"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("missing bundle exit = %d, want 1 (stderr=%q)", code, stderr.String())
	}
}
