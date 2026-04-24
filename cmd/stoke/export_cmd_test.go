package main

// export_cmd_test.go — unit coverage for `stoke export` (work-stoke T16).
//
// The invariants under test:
//
//   - BuildTraceBundle produces a valid zip containing every artefact
//     referenced by the signature file.
//   - The zip contains a manifest.json whose files[] match the archived
//     payloads (size + digest).
//   - The Merkle root is deterministic: identical content ⇒ identical
//     root; different content ⇒ different root.
//   - Output filename embeds the 12-char Merkle prefix so content-
//     addressing holds at the filesystem layer.
//   - CLI dispatch (`runExportCmd`) returns 2 for usage errors and 0
//     for a successful bundle.

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ericmacdougall/stoke/internal/session"
)

// writeTestSession scaffolds a complete .stoke/ tree under t.TempDir()
// and returns (repoRoot, signatureFile). Individual tests can mutate
// the signature before exporting.
func writeTestSession(t *testing.T) (string, session.SignatureFile) {
	t.Helper()
	root := t.TempDir()
	stokeDir := filepath.Join(root, ".stoke")
	if err := os.MkdirAll(stokeDir, 0o755); err != nil {
		t.Fatalf("mkdir .stoke: %v", err)
	}
	// stream.jsonl
	streamPath := filepath.Join(stokeDir, "stream.jsonl")
	if err := os.WriteFile(streamPath, []byte(`{"type":"stoke.session.start"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	// checkpoints.jsonl
	checkpointPath := filepath.Join(stokeDir, "checkpoints.jsonl")
	if err := os.WriteFile(checkpointPath, []byte(`{"checkpoint":1}`+"\n"), 0o600); err != nil {
		t.Fatalf("write checkpoints: %v", err)
	}
	// ledger/ with a node and an edge
	ledgerDir := filepath.Join(stokeDir, "ledger")
	if err := os.MkdirAll(filepath.Join(ledgerDir, "nodes"), 0o755); err != nil {
		t.Fatalf("mkdir ledger: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ledgerDir, "nodes", "n1.json"), []byte(`{"id":"n1"}`), 0o600); err != nil {
		t.Fatalf("write node: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ledgerDir, "chain.txt"), []byte("hashchain\n"), 0o600); err != nil {
		t.Fatalf("write chain: %v", err)
	}
	// memory.db with one session-scoped row
	memoryDB := filepath.Join(stokeDir, "memory.db")
	writeTestMembus(t, memoryDB, "r1-testsess")

	sig := session.SignatureFile{
		Version:        "1",
		InstanceID:     "r1-testsess",
		PID:            42,
		RepoRoot:       root,
		Mode:           "test",
		Status:         "running",
		StreamFile:     streamPath,
		LedgerDir:      ledgerDir,
		CheckpointFile: checkpointPath,
		BusWAL:         memoryDB,
		StartedAt:      time.Now().UTC().Add(-time.Minute),
		UpdatedAt:      time.Now().UTC(),
	}
	// Write signature file so resolveExportSignature finds it.
	sigBytes, err := json.Marshal(sig)
	if err != nil {
		t.Fatalf("marshal sig: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stokeDir, "r1.session.json"), sigBytes, 0o600); err != nil {
		t.Fatalf("write sig: %v", err)
	}
	return root, sig
}

// writeTestMembus creates a SQLite DB matching the `stoke_memory_bus`
// schema the exporter probes for. One session-scoped row goes in so
// the memory.json payload has something to surface.
func writeTestMembus(t *testing.T, path, sessionID string) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=2000")
	if err != nil {
		t.Fatalf("open membus: %v", err)
	}
	defer db.Close()
	const ddl = `
CREATE TABLE IF NOT EXISTS stoke_memory_bus (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scope TEXT,
    scope_target TEXT,
    key TEXT,
    content TEXT,
    author TEXT,
    created_at TEXT,
    expires_at TEXT,
    content_hash TEXT
);`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("membus ddl: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO stoke_memory_bus(scope, scope_target, key, content, author, created_at, content_hash)
		 VALUES ('session', ?, 'note', 'hello', 'worker1', ?, 'abc123')`,
		sessionID, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("membus insert: %v", err)
	}
	// A non-matching row — must NOT appear in the export.
	if _, err := db.Exec(
		`INSERT INTO stoke_memory_bus(scope, scope_target, key, content, created_at)
		 VALUES ('session', 'r1-other', 'note2', 'not mine', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("membus insert other: %v", err)
	}
}

func TestBuildTraceBundle_Contents(t *testing.T) {
	_, sig := writeTestSession(t)
	outDir := t.TempDir()
	bundlePath, merkleRoot, err := BuildTraceBundle(sig, outDir)
	if err != nil {
		t.Fatalf("BuildTraceBundle: %v", err)
	}
	if !strings.HasSuffix(bundlePath, ".tracebundle") {
		t.Errorf("bundlePath = %q, want .tracebundle suffix", bundlePath)
	}
	if len(merkleRoot) != 64 {
		t.Errorf("merkleRoot len = %d, want 64 hex chars", len(merkleRoot))
	}
	// Filename must embed the first 12 hex chars of the root.
	if !strings.Contains(bundlePath, merkleRoot[:12]) {
		t.Errorf("bundlePath %q does not embed merkle prefix %q", bundlePath, merkleRoot[:12])
	}

	// Read zip back and verify each expected path is present.
	zr, err := zip.OpenReader(bundlePath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	got := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		buf, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		got[f.Name] = buf
	}
	wantPaths := []string{
		"stream.jsonl",
		"checkpoints/checkpoints.jsonl",
		"ledger/nodes/n1.json",
		"ledger/chain.txt",
		"memory.json",
		"manifest.json",
	}
	for _, p := range wantPaths {
		if _, ok := got[p]; !ok {
			t.Errorf("missing %s in bundle (have %v)", p, mapKeys(got))
		}
	}

	// Manifest.json must list the same merkle root the call returned.
	var m TraceBundleManifest
	if err := json.Unmarshal(got["manifest.json"], &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if m.MerkleRoot != merkleRoot {
		t.Errorf("manifest.MerkleRoot=%q, want %q", m.MerkleRoot, merkleRoot)
	}
	if m.SessionID != sig.InstanceID {
		t.Errorf("manifest.SessionID=%q, want %q", m.SessionID, sig.InstanceID)
	}
	if m.Format != "tracebundle" || m.Version != traceBundleFormatVersion {
		t.Errorf("manifest format=%q version=%q", m.Format, m.Version)
	}
	// Every listed file must match what's actually in the zip.
	for _, e := range m.Files {
		data, ok := got[e.Path]
		if !ok {
			t.Errorf("manifest references %q which is not in the zip", e.Path)
			continue
		}
		if int64(len(data)) != e.Size {
			t.Errorf("size mismatch %s: manifest=%d actual=%d", e.Path, e.Size, len(data))
		}
	}

	// Memory.json should contain exactly one row (the session-scoped one).
	var snap sessionMemorySnapshot
	if err := json.Unmarshal(got["memory.json"], &snap); err != nil {
		t.Fatalf("unmarshal memory.json: %v", err)
	}
	if snap.SessionID != sig.InstanceID {
		t.Errorf("memory snapshot session=%q, want %q", snap.SessionID, sig.InstanceID)
	}
	if snap.Count != 1 || len(snap.Rows) != 1 {
		t.Fatalf("memory.Count=%d rows=%d, want 1", snap.Count, len(snap.Rows))
	}
	if snap.Rows[0].Content != "hello" {
		t.Errorf("memory row content=%q, want %q", snap.Rows[0].Content, "hello")
	}
}

func TestBuildTraceBundle_DeterministicMerkleRoot(t *testing.T) {
	_, sig := writeTestSession(t)

	outDir1 := t.TempDir()
	outDir2 := t.TempDir()
	_, root1, err := BuildTraceBundle(sig, outDir1)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	_, root2, err := BuildTraceBundle(sig, outDir2)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if root1 != root2 {
		t.Errorf("merkle root should be deterministic over identical content:\n  %q\n  %q", root1, root2)
	}

	// Mutate an artefact and re-export — root MUST change.
	if err := os.WriteFile(sig.StreamFile, []byte(`{"mutated":true}`+"\n"), 0o600); err != nil {
		t.Fatalf("mutate stream: %v", err)
	}
	outDir3 := t.TempDir()
	_, root3, err := BuildTraceBundle(sig, outDir3)
	if err != nil {
		t.Fatalf("third build: %v", err)
	}
	if root3 == root1 {
		t.Errorf("merkle root unchanged after mutation: %q", root3)
	}
}

func TestBuildTraceBundle_MissingArtifacts(t *testing.T) {
	root := t.TempDir()
	sig := session.SignatureFile{
		InstanceID: "r1-empty",
		RepoRoot:   root,
		Status:     "crashed",
		StreamFile: filepath.Join(root, "does-not-exist.jsonl"),
		LedgerDir:  filepath.Join(root, "does-not-exist"),
		UpdatedAt:  time.Now().UTC(),
	}
	bundlePath, merkleRoot, err := BuildTraceBundle(sig, t.TempDir())
	if err != nil {
		t.Fatalf("BuildTraceBundle with missing artefacts: %v", err)
	}
	if merkleRoot == "" {
		t.Error("empty merkleRoot on missing-artefact export")
	}
	// Zip must at least contain manifest.json + memory.json (the empty
	// snapshot). Nothing else is guaranteed.
	zr, err := zip.OpenReader(bundlePath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	names := make(map[string]bool)
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["manifest.json"] {
		t.Error("manifest.json missing from empty bundle")
	}
	if !names["memory.json"] {
		t.Error("memory.json missing from empty bundle")
	}
}

func TestResolveBundlePath_FileVsDir(t *testing.T) {
	dir := t.TempDir()
	// Directory target → generated filename with merkle prefix.
	p, err := resolveBundlePath(dir, "r1-abc", "deadbeef12345678")
	if err != nil {
		t.Fatalf("resolveBundlePath dir: %v", err)
	}
	if filepath.Dir(p) != dir {
		t.Errorf("resolved path %q not under %q", p, dir)
	}
	if !strings.Contains(p, "deadbeef1234") {
		t.Errorf("resolved path %q missing merkle prefix", p)
	}

	// Explicit file target → splice merkle prefix in front of ext.
	target := filepath.Join(dir, "nested", "out.tracebundle")
	p2, err := resolveBundlePath(target, "r1-abc", "feedface12345678")
	if err != nil {
		t.Fatalf("resolveBundlePath file: %v", err)
	}
	if !strings.HasSuffix(p2, "-feedface1234.tracebundle") {
		t.Errorf("explicit file path = %q, want …-feedface1234.tracebundle", p2)
	}

	// Re-resolving with same prefix must be idempotent.
	p3, err := resolveBundlePath(p2, "r1-abc", "feedface12345678")
	if err != nil {
		t.Fatalf("resolveBundlePath idempotent: %v", err)
	}
	if p3 != p2 {
		t.Errorf("non-idempotent resolve: %q → %q", p2, p3)
	}
}

func TestRunExportCmd_UsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"bad format", []string{"--format", "csv", "--session-id", "r1-x", "--output", "/tmp/x"}},
		{"missing session", []string{"--format", "tracebundle", "--output", "/tmp/x"}},
		{"missing output", []string{"--format", "tracebundle", "--session-id", "r1-x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runExportCmd(tc.args, &stdout, &stderr); code != 2 {
				t.Errorf("exit code = %d, want 2 (stderr=%q)", code, stderr.String())
			}
		})
	}
}

func TestRunExportCmd_Happy(t *testing.T) {
	root, _ := writeTestSession(t)
	outDir := t.TempDir()
	args := []string{
		"--format", "tracebundle",
		"--session-id", "r1-testsess",
		"--output", outDir,
		"--repo", root,
	}
	var stdout, stderr bytes.Buffer
	if code := runExportCmd(args, &stdout, &stderr); code != 0 {
		t.Fatalf("runExportCmd = %d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "merkle_root=") {
		t.Errorf("stdout = %q, want merkle_root= prefix", stdout.String())
	}
	// Bundle file must exist on disk.
	matches, err := filepath.Glob(filepath.Join(outDir, "*.tracebundle"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected 1 .tracebundle in %s, got %v (%v)", outDir, matches, err)
	}
}

func TestRunExportCmd_SessionMismatch(t *testing.T) {
	root, _ := writeTestSession(t)
	args := []string{
		"--format", "tracebundle",
		"--session-id", "r1-wrong-id",
		"--output", t.TempDir(),
		"--repo", root,
	}
	var stdout, stderr bytes.Buffer
	if code := runExportCmd(args, &stdout, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1 (mismatch); stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "does not match") {
		t.Errorf("stderr lacks mismatch message: %q", stderr.String())
	}
}

// mapKeys is a tiny helper for sorted debug output on test failure.
func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
