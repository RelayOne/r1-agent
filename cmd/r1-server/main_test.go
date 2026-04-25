package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/session"
)

// newTestDB opens a fresh SQLite DB under t.TempDir() so each test
// runs against a clean schema.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestServer(t *testing.T, db *DB) *httptest.Server {
	t.Helper()
	mux := buildMux(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestAPIHealth(t *testing.T) {
	s := newTestServer(t, newTestDB(t))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/api/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status=%v, want ok", body["status"])
	}
	if body["version"] == "" || body["version"] == nil {
		t.Error("version missing")
	}
}

func TestAPIRegisterThenList(t *testing.T) {
	db := newTestDB(t)
	s := newTestServer(t, db)

	now := time.Now().UTC()
	sig := session.SignatureFile{
		Version:    "1",
		PID:        42,
		InstanceID: "r1-abc12345",
		StartedAt:  now,
		UpdatedAt:  now,
		RepoRoot:   "/tmp/fake-repo",
		Mode:       "ship",
		Status:     "running",
		StreamFile: "/tmp/fake-repo/.stoke/stream.jsonl",
	}
	body, _ := json.Marshal(sig)
	postReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.URL+"/api/register", bytes.NewReader(body))
	postReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status=%d", resp.StatusCode)
	}

	// List sessions — should contain the one we just registered.
	listReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/api/sessions", nil)
	resp, err = http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer resp.Body.Close()
	var listBody struct {
		Sessions []SessionRow `json:"sessions"`
		Count    int          `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listBody.Count != 1 || len(listBody.Sessions) != 1 {
		t.Fatalf("want 1 session, got count=%d len=%d", listBody.Count, len(listBody.Sessions))
	}
	if listBody.Sessions[0].InstanceID != sig.InstanceID {
		t.Errorf("instance_id=%q, want %q", listBody.Sessions[0].InstanceID, sig.InstanceID)
	}

	// Session detail by ID.
	detailReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/api/session/"+sig.InstanceID, nil)
	resp, err = http.DefaultClient.Do(detailReq)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail status=%d", resp.StatusCode)
	}
	var row SessionRow
	if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if row.RepoRoot != sig.RepoRoot {
		t.Errorf("repo_root=%q, want %q", row.RepoRoot, sig.RepoRoot)
	}
}

func TestAPIRegisterRejectsEmptyInstance(t *testing.T) {
	s := newTestServer(t, newTestDB(t))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.URL+"/api/register", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAPIEventsEmpty(t *testing.T) {
	db := newTestDB(t)
	s := newTestServer(t, db)

	// Prime the DB with a session so the endpoint doesn't 404 on the detail.
	sig := session.SignatureFile{InstanceID: "r1-empty", Status: "running", StartedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.UpsertSession(sig); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/api/session/r1-empty/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		InstanceID string     `json:"instance_id"`
		Events     []EventRow `json:"events"`
		Count      int        `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.InstanceID != "r1-empty" {
		t.Errorf("instance_id=%q", body.InstanceID)
	}
	if body.Count != 0 {
		t.Errorf("count=%d, want 0", body.Count)
	}
}

func TestAPICheckpointsMissingFile(t *testing.T) {
	db := newTestDB(t)
	s := newTestServer(t, db)
	sig := session.SignatureFile{
		InstanceID:     "r1-ckpt",
		Status:         "running",
		CheckpointFile: filepath.Join(t.TempDir(), "does-not-exist.jsonl"),
		StartedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := db.UpsertSession(sig); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/api/session/r1-ckpt/checkpoints", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("checkpoints: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	count, ok := body["count"].(float64)
	if !ok {
		t.Fatalf("count field: unexpected type: %T", body["count"])
	}
	if count != 0 {
		t.Errorf("count=%v, want 0 for missing file", body["count"])
	}
}

func TestResolvePort(t *testing.T) {
	t.Setenv("R1_SERVER_PORT", "")
	if p, err := resolvePort(); err != nil || p != defaultPort {
		t.Errorf("default: got %d %v, want %d", p, err, defaultPort)
	}
	t.Setenv("R1_SERVER_PORT", "8080")
	if p, err := resolvePort(); err != nil || p != 8080 {
		t.Errorf("valid: got %d %v", p, err)
	}
	t.Setenv("R1_SERVER_PORT", "banana")
	if _, err := resolvePort(); err == nil {
		t.Error("non-numeric should error")
	}
	t.Setenv("R1_SERVER_PORT", "0")
	if _, err := resolvePort(); err == nil {
		t.Error("port 0 should error")
	}
	t.Setenv("R1_SERVER_PORT", "70000")
	if _, err := resolvePort(); err == nil {
		t.Error("port > 65535 should error")
	}
}
