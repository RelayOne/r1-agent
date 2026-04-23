package session

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriteSignatureCreatesFileWithCorrectShape(t *testing.T) {
	dir := t.TempDir()
	cfg := SignatureConfig{
		Mode:           "sow",
		SowName:        "scope-suite",
		Model:          "claude-4.5-sonnet",
		StreamFile:     filepath.Join(dir, ".stoke", "stream.jsonl"),
		LedgerDir:      filepath.Join(dir, ".stoke", "ledger"),
		CheckpointFile: filepath.Join(dir, ".stoke", "checkpoints", "timeline.jsonl"),
		BusWAL:         filepath.Join(dir, ".stoke", "bus", "events.log"),
		RegisterURL:    "http://127.0.0.1:1/nowhere", // invalid port, registration will fail silently
	}

	sig, err := WriteSignature(dir, cfg)
	if err != nil {
		t.Fatalf("WriteSignature: %v", err)
	}
	t.Cleanup(func() { _ = sig.Close("completed") })

	path := filepath.Join(dir, ".stoke", "r1.session.json")
	if sig.Path() != path {
		t.Errorf("Path = %q, want %q", sig.Path(), path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read signature: %v", err)
	}
	var got SignatureFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal signature: %v", err)
	}

	if got.Version != SignatureVersion {
		t.Errorf("Version = %q, want %q", got.Version, SignatureVersion)
	}
	if got.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", got.PID, os.Getpid())
	}
	if !strings.HasPrefix(got.InstanceID, "r1-") || len(got.InstanceID) != 11 {
		t.Errorf("InstanceID = %q, want r1-<8hex>", got.InstanceID)
	}
	if got.RepoRoot != dir {
		t.Errorf("RepoRoot = %q, want %q", got.RepoRoot, dir)
	}
	if got.Mode != "sow" {
		t.Errorf("Mode = %q, want sow", got.Mode)
	}
	if got.SowName != "scope-suite" {
		t.Errorf("SowName = %q, want scope-suite", got.SowName)
	}
	if got.Status != "running" {
		t.Errorf("Status = %q, want running", got.Status)
	}
	if got.StreamFile != cfg.StreamFile {
		t.Errorf("StreamFile = %q, want %q", got.StreamFile, cfg.StreamFile)
	}
	if got.LedgerDir != cfg.LedgerDir {
		t.Errorf("LedgerDir = %q, want %q", got.LedgerDir, cfg.LedgerDir)
	}
	if got.StartedAt.IsZero() {
		t.Error("StartedAt is zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestWriteSignatureEmptyRepoErrors(t *testing.T) {
	if _, err := WriteSignature("", SignatureConfig{}); err == nil {
		t.Fatal("WriteSignature(\"\") returned nil error")
	}
}

func TestCloseSetsStatusCompletedAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	sig, err := WriteSignature(dir, SignatureConfig{
		Mode:        "chat",
		RegisterURL: "http://127.0.0.1:1/nowhere",
	})
	if err != nil {
		t.Fatalf("WriteSignature: %v", err)
	}

	if err := sig.Close("completed"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second close must be a no-op, not a panic.
	if err := sig.Close("completed"); err != nil {
		t.Fatalf("Close (2nd): %v", err)
	}

	got, err := LoadSignature(sig.Path())
	if err != nil {
		t.Fatalf("LoadSignature: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("Status = %q, want completed", got.Status)
	}
}

func TestCloseFailedStatusPropagates(t *testing.T) {
	dir := t.TempDir()
	sig, err := WriteSignature(dir, SignatureConfig{RegisterURL: "http://127.0.0.1:1/x"})
	if err != nil {
		t.Fatalf("WriteSignature: %v", err)
	}
	if err := sig.Close("failed"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := LoadSignature(sig.Path())
	if err != nil {
		t.Fatalf("LoadSignature: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("Status = %q, want failed", got.Status)
	}
}

func TestHeartbeatUpdatesUpdatedAt(t *testing.T) {
	// Override interval for a fast test — we need a dedicated helper
	// since HeartbeatInterval is const. Use the fact that WriteSignature
	// starts a goroutine and manually invoke the writeAtomic path.
	dir := t.TempDir()
	sig, err := WriteSignature(dir, SignatureConfig{RegisterURL: "http://127.0.0.1:1/x"})
	if err != nil {
		t.Fatalf("WriteSignature: %v", err)
	}
	t.Cleanup(func() { _ = sig.Close("completed") })

	first, err := LoadSignature(sig.Path())
	if err != nil {
		t.Fatalf("LoadSignature: %v", err)
	}

	// Simulate a heartbeat tick by calling the refresh path directly.
	sig.mu.Lock()
	later := first.UpdatedAt.Add(HeartbeatInterval + time.Second)
	sig.data.UpdatedAt = later
	if err := sig.writeAtomic(); err != nil {
		sig.mu.Unlock()
		t.Fatalf("writeAtomic: %v", err)
	}
	sig.mu.Unlock()

	got, err := LoadSignature(sig.Path())
	if err != nil {
		t.Fatalf("LoadSignature (2): %v", err)
	}
	if !got.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("UpdatedAt did not advance: first=%v, after=%v", first.UpdatedAt, got.UpdatedAt)
	}
}

func TestRegisterWithServerSilentOnNoServer(t *testing.T) {
	// Bind a free port and close it to guarantee refused connection.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close() // close so nothing accepts

	dir := t.TempDir()
	sig, err := WriteSignature(dir, SignatureConfig{
		Mode:        "sow",
		RegisterURL: "http://" + addr + "/api/register",
	})
	if err != nil {
		t.Fatalf("WriteSignature: %v", err)
	}
	t.Cleanup(func() { _ = sig.Close("completed") })

	// Allow registerWithServer goroutine to try and fail.
	time.Sleep(RegisterTimeout + 200*time.Millisecond)

	// Signature file still exists and is readable — startup did not block.
	if _, err := LoadSignature(sig.Path()); err != nil {
		t.Fatalf("LoadSignature: %v", err)
	}
}

func TestRegisterWithServerPostsWhenServerUp(t *testing.T) {
	var hits atomic.Int32
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/register" {
			t.Errorf("Path = %s, want /api/register", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	sig, err := WriteSignature(dir, SignatureConfig{
		Mode:        "sow",
		RegisterURL: srv.URL + "/api/register",
	})
	if err != nil {
		t.Fatalf("WriteSignature: %v", err)
	}
	t.Cleanup(func() { _ = sig.Close("completed") })

	// Wait for registration goroutine.
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}

	if hits.Load() == 0 {
		t.Fatal("register handler was never called")
	}
	var payload SignatureFile
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("register body not valid SignatureFile JSON: %v\nbody: %s", err, string(gotBody))
	}
	if payload.InstanceID != sig.InstanceID() {
		t.Errorf("register body instance_id = %q, want %q", payload.InstanceID, sig.InstanceID())
	}
}

func TestNewInstanceIDFormat(t *testing.T) {
	for i := 0; i < 16; i++ {
		id := NewInstanceID()
		if !strings.HasPrefix(id, "r1-") {
			t.Errorf("id %q does not start with r1-", id)
		}
		if len(id) != 11 {
			t.Errorf("id %q length %d, want 11", id, len(id))
		}
	}
}

func TestLoadSignatureMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSignature(filepath.Join(dir, "does-not-exist.json"))
	if err == nil {
		t.Fatal("LoadSignature nonexistent: expected error")
	}
}
