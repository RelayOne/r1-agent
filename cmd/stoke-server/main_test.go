package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/runtrack"
)

// helper: boot a full mux + runtrack dir, return the test server.
func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("STOKE_RUNTRACK_DIR", dir)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/api/runs", handleAPIRuns)
	mux.HandleFunc("/api/run/", handleAPIRun)
	mux.HandleFunc("/tail/", handleTail)
	mux.HandleFunc("/run/", handleRunHTML)
	mux.HandleFunc("/", handleRoot)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, dir
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIRunsEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var runs []runView
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}
}

func TestAPIRunsWithRegisteredInstance(t *testing.T) {
	srv, dir := newTestServer(t)

	reg, err := runtrack.Register(runtrack.Manifest{
		RunID:      "run-e2e-test",
		PID:        os.Getpid(), // our own PID — guaranteed alive
		Command:    "stoke sow",
		RepoRoot:   "/tmp/repo-e2e",
		Model:      "claude-sonnet-4-6",
		StokeBuild: "abcdef123",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	resp, err := http.Get(srv.URL + "/api/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var runs []runView
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run in %s, got %d", dir, len(runs))
	}
	r := runs[0]
	if r.Manifest.RunID != "run-e2e-test" {
		t.Errorf("RunID = %q", r.Manifest.RunID)
	}
	if !r.Alive {
		t.Errorf("Alive should be true (PID %d is us)", r.Manifest.PID)
	}
	if r.Manifest.StokeBuild != "abcdef123" {
		t.Errorf("StokeBuild = %q", r.Manifest.StokeBuild)
	}
}

func TestAPIRunDetailFound(t *testing.T) {
	srv, _ := newTestServer(t)

	reg, _ := runtrack.Register(runtrack.Manifest{
		RunID:    "run-detail",
		PID:      os.Getpid(),
		RepoRoot: "/tmp/repo",
	})
	defer reg.Close()

	resp, err := http.Get(srv.URL + "/api/run/run-detail")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["manifest"]; !ok {
		t.Error("response missing manifest")
	}
	if _, ok := out["alive"]; !ok {
		t.Error("response missing alive")
	}
}

func TestAPIRunDetailNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/api/run/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestIndexHTMLRendersRegisteredInstance(t *testing.T) {
	srv, _ := newTestServer(t)

	reg, _ := runtrack.Register(runtrack.Manifest{
		RunID:      "run-html-test",
		PID:        os.Getpid(),
		RepoRoot:   "/tmp/sample",
		Model:      "claude-sonnet-4-6",
		StokeBuild: "deadbeef",
		Command:    "stoke sow",
	})
	defer reg.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64*1024)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	for _, want := range []string{"run-html-test", "claude-sonnet-4-6", "deadbeef", "/tmp/sample", "ACTIVE"} {
		if !strings.Contains(body, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}

func TestTailReadsJSONL(t *testing.T) {
	srv, _ := newTestServer(t)

	// Create a mock JSONL file.
	logsDir := t.TempDir()
	jsonl := filepath.Join(logsDir, "T1-abc.jsonl")
	content := `{"type":"dispatch_start","dispatch_id":"d-123"}
{"type":"tool_call","tool":"bash","result":"hello"}
`
	if err := os.WriteFile(jsonl, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	reg, _ := runtrack.Register(runtrack.Manifest{
		RunID:         "run-tail",
		PID:           os.Getpid(),
		WorkerLogsDir: logsDir,
	})
	defer reg.Close()

	resp, err := http.Get(srv.URL + "/tail/run-tail")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	buf := make([]byte, 16*1024)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	if !strings.Contains(body, "dispatch_start") {
		t.Error("tail missing dispatch_start line")
	}
	if !strings.Contains(body, "tool_call") {
		t.Error("tail missing tool_call line")
	}
	if !strings.Contains(body, "=== T1-abc.jsonl ===") {
		t.Error("tail missing file-delimiter marker")
	}
}

func TestTailNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/tail/missing-run")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestRunHTMLViewerRenders(t *testing.T) {
	srv, _ := newTestServer(t)

	reg, _ := runtrack.Register(runtrack.Manifest{
		RunID:    "run-viewer",
		PID:      os.Getpid(),
		RepoRoot: "/tmp/viewer-repo",
		SOWName:  "test-sow.md",
		Model:    "claude-sonnet-4-6",
	})
	defer reg.Close()

	resp, err := http.Get(srv.URL + "/run/run-viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	buf := make([]byte, 64*1024)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	for _, want := range []string{"run-viewer", "test-sow.md", "/tmp/viewer-repo"} {
		if !strings.Contains(body, want) {
			t.Errorf("viewer HTML missing %q", want)
		}
	}
}

func TestSortingAlivenessThenNewest(t *testing.T) {
	srv, _ := newTestServer(t)

	// Register three in specific order:
	//  - old dead (bogus PID, old StartedAt)
	//  - new dead (bogus PID, recent StartedAt)
	//  - alive (our PID)
	// Expect alive first, then new-dead, then old-dead.
	base := time.Now().UTC()
	old, _ := runtrack.Register(runtrack.Manifest{
		RunID: "r-old-dead", PID: 99999998,
		StartedAt: base.Add(-2 * time.Hour).Format(time.RFC3339Nano),
	})
	defer old.Close()
	new1, _ := runtrack.Register(runtrack.Manifest{
		RunID: "r-new-dead", PID: 99999999,
		StartedAt: base.Add(-1 * time.Minute).Format(time.RFC3339Nano),
	})
	defer new1.Close()
	alive, _ := runtrack.Register(runtrack.Manifest{
		RunID: "r-alive", PID: os.Getpid(),
		StartedAt: base.Add(-30 * time.Minute).Format(time.RFC3339Nano),
	})
	defer alive.Close()

	resp, err := http.Get(srv.URL + "/api/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var runs []runView
	_ = json.NewDecoder(resp.Body).Decode(&runs)

	if len(runs) != 3 {
		t.Fatalf("expected 3, got %d", len(runs))
	}
	if runs[0].Manifest.RunID != "r-alive" {
		t.Errorf("position 0 = %q, want r-alive", runs[0].Manifest.RunID)
	}
	// Second + third are both dead — sorted by StartedAt desc.
	if runs[1].Manifest.RunID != "r-new-dead" {
		t.Errorf("position 1 = %q, want r-new-dead", runs[1].Manifest.RunID)
	}
	if runs[2].Manifest.RunID != "r-old-dead" {
		t.Errorf("position 2 = %q, want r-old-dead", runs[2].Manifest.RunID)
	}
}

func TestIsAddrInUse(t *testing.T) {
	// Construct an OpError with a message containing "address already in use"
	// and confirm the detector trips. This protects the singleton behavior.
	cases := []struct {
		err  error
		want bool
	}{
		{&net.OpError{Err: errStr("listen tcp :3948: address already in use")}, true},
		{&net.OpError{Err: errStr("something else")}, false},
		{errStr("address already in use wrapped"), true},
		{errStr("unrelated error"), false},
	}
	for i, c := range cases {
		if got := isAddrInUse(c.err); got != c.want {
			t.Errorf("case %d: isAddrInUse(%v) = %v, want %v", i, c.err, got, c.want)
		}
	}
}

// errStr is an error type for tests.
type errStr string

func (e errStr) Error() string { return string(e) }
