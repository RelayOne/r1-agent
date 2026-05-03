package main

// ctl_daemon_cmd_test.go — TASK-36 tests for `r1 ctl <verb>`.
//
// Each test installs a fake r1 home dir, writes a daemon.json
// discovery file pointing at an httptest.Server, and asserts the verb
// dispatcher reaches the right route + token.
//
// Why httptest rather than a real daemon: the verb dispatcher's job is
// "translate verb to HTTP request"; the daemon's job is "honor that
// HTTP request". Decoupling lets us assert the wire shape without
// pulling daemon.New + worker pool into every CLI test.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/daemondisco"
)

// ctlTestEnv pairs an httptest.Server with a temp R1_HOME containing
// a matching daemon.json. Tests Set R1_HOME via t.Setenv so the
// daemondisco.ReadDiscovery() probes the temp dir.
type ctlTestEnv struct {
	srv          *httptest.Server
	token        string
	discoveryDir string
	requests     []recordedReq
}

type recordedReq struct {
	Method string
	Path   string
	Auth   string
	Body   []byte
}

// newCtlTestEnv stands up an httptest server that records every
// incoming request and returns a stub JSON body. token is the bearer
// the test daemon expects (matching what we write into daemon.json).
func newCtlTestEnv(t *testing.T, token string, response any) *ctlTestEnv {
	t.Helper()
	env := &ctlTestEnv{token: token}

	env.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		env.requests = append(env.requests, recordedReq{
			Method: r.Method,
			Path:   r.URL.RequestURI(),
			Auth:   r.Header.Get("Authorization"),
			Body:   body,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(env.srv.Close)

	// Resolve port from URL.
	u, err := url.Parse(env.srv.URL)
	if err != nil {
		t.Fatalf("parse httptest URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	env.discoveryDir = t.TempDir()
	if _, err := daemondisco.WriteDiscoveryTo(env.discoveryDir, 4242, "/tmp/r1-test.sock", port, token, "r1-test"); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	t.Setenv("R1_HOME", env.discoveryDir)
	return env
}

// runCtl is a helper that captures stdout+stderr from runCtlDaemonCmd.
func runCtl(args ...string) (stdout, stderr string, code int) {
	var so, se bytes.Buffer
	code = runCtlDaemonCmd(args, &so, &se)
	return so.String(), se.String(), code
}

func TestCtl_Discover(t *testing.T) {
	newCtlTestEnv(t, "tk-discover", map[string]any{"ok": true})
	stdout, stderr, code := runCtl("discover")
	if code != 0 {
		t.Fatalf("discover: exit code %d (stderr=%q)", code, stderr)
	}
	var disc daemondisco.Discovery
	if err := json.Unmarshal([]byte(stdout), &disc); err != nil {
		t.Fatalf("parse discover output: %v\nbody: %s", err, stdout)
	}
	if disc.Token != "tk-discover" {
		t.Errorf("discover token: got %q, want tk-discover", disc.Token)
	}
	if disc.PID != 4242 {
		t.Errorf("discover pid: got %d, want 4242", disc.PID)
	}
}

func TestCtl_SessionsList(t *testing.T) {
	env := newCtlTestEnv(t, "tk-list", []map[string]any{{"id": "t1", "title": "x"}})
	stdout, stderr, code := runCtl("sessions", "list")
	if code != 0 {
		t.Fatalf("sessions list: exit %d (stderr=%q)", code, stderr)
	}
	if len(env.requests) != 1 {
		t.Fatalf("sessions list: want 1 request, got %d", len(env.requests))
	}
	r := env.requests[0]
	if r.Method != "GET" {
		t.Errorf("method: got %s, want GET", r.Method)
	}
	if r.Path != "/v1/queue/tasks" {
		t.Errorf("path: got %q, want /v1/queue/tasks", r.Path)
	}
	if r.Auth != "Bearer tk-list" {
		t.Errorf("auth: got %q, want Bearer tk-list", r.Auth)
	}
	if !strings.Contains(stdout, "t1") {
		t.Errorf("stdout: missing task id; got %q", stdout)
	}
}

func TestCtl_SessionsListWithStateFilter(t *testing.T) {
	env := newCtlTestEnv(t, "tk-list-st", []map[string]any{})
	_, stderr, code := runCtl("sessions", "list", "--state", "queued")
	if code != 0 {
		t.Fatalf("sessions list --state: exit %d (stderr=%q)", code, stderr)
	}
	if len(env.requests) != 1 {
		t.Fatalf("want 1 request, got %d", len(env.requests))
	}
	if !strings.Contains(env.requests[0].Path, "state=queued") {
		t.Errorf("path missing state filter: %q", env.requests[0].Path)
	}
}

func TestCtl_Enqueue(t *testing.T) {
	env := newCtlTestEnv(t, "tk-enq", map[string]any{"id": "t-new"})
	_, stderr, code := runCtl("enqueue",
		"--title", "demo task",
		"--prompt", "do the thing",
		"--priority", "5",
	)
	if code != 0 {
		t.Fatalf("enqueue: exit %d (stderr=%q)", code, stderr)
	}
	if len(env.requests) != 1 {
		t.Fatalf("want 1 request, got %d", len(env.requests))
	}
	r := env.requests[0]
	if r.Method != "POST" {
		t.Errorf("method: got %s, want POST", r.Method)
	}
	if r.Path != "/v1/queue/enqueue" {
		t.Errorf("path: got %q, want /v1/queue/enqueue", r.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if body["title"] != "demo task" {
		t.Errorf("title: got %v, want demo task", body["title"])
	}
	if body["prompt"] != "do the thing" {
		t.Errorf("prompt: got %v, want do the thing", body["prompt"])
	}
	if v, _ := body["priority"].(float64); int(v) != 5 {
		t.Errorf("priority: got %v, want 5", body["priority"])
	}
}

func TestCtl_EnqueueRequiresTitleAndPrompt(t *testing.T) {
	newCtlTestEnv(t, "tk-enq-2", map[string]any{})
	_, stderr, code := runCtl("enqueue", "--title", "only-title")
	if code != 2 {
		t.Errorf("missing prompt: exit %d, want 2 (usage); stderr=%q", code, stderr)
	}
}

func TestCtl_Status(t *testing.T) {
	env := newCtlTestEnv(t, "tk-status", map[string]any{"workers": 4, "active": 1})
	_, _, code := runCtl("status")
	if code != 0 {
		t.Fatalf("status: exit %d", code)
	}
	if env.requests[0].Path != "/v1/queue/status" {
		t.Errorf("path: got %q", env.requests[0].Path)
	}
}

func TestCtl_Workers(t *testing.T) {
	env := newCtlTestEnv(t, "tk-workers", map[string]any{"workers": 8})
	_, stderr, code := runCtl("workers", "--count", "8")
	if code != 0 {
		t.Fatalf("workers: exit %d (stderr=%q)", code, stderr)
	}
	r := env.requests[0]
	if r.Method != "POST" || r.Path != "/v1/queue/workers" {
		t.Errorf("workers wire: %s %s", r.Method, r.Path)
	}
	var body map[string]any
	json.Unmarshal(r.Body, &body)
	if v, _ := body["count"].(float64); int(v) != 8 {
		t.Errorf("count: got %v, want 8", body["count"])
	}
}

func TestCtl_WorkersRequiresCount(t *testing.T) {
	newCtlTestEnv(t, "tk-workers-2", map[string]any{})
	_, stderr, code := runCtl("workers")
	if code != 2 {
		t.Errorf("workers no count: exit %d (want 2); stderr=%q", code, stderr)
	}
}

func TestCtl_WAL(t *testing.T) {
	env := newCtlTestEnv(t, "tk-wal", []map[string]any{{"type": "enqueue"}})
	_, _, code := runCtl("wal", "--n", "25")
	if code != 0 {
		t.Fatalf("wal: exit %d", code)
	}
	if env.requests[0].Path != "/v1/queue/wal?n=25" {
		t.Errorf("wal path: got %q", env.requests[0].Path)
	}
}

func TestCtl_Tasks(t *testing.T) {
	env := newCtlTestEnv(t, "tk-tasks", []map[string]any{})
	_, _, code := runCtl("tasks", "--state", "running")
	if code != 0 {
		t.Fatalf("tasks: exit %d", code)
	}
	if !strings.Contains(env.requests[0].Path, "state=running") {
		t.Errorf("tasks path missing state: %q", env.requests[0].Path)
	}
}

func TestCtl_PauseResume(t *testing.T) {
	env := newCtlTestEnv(t, "tk-pr", map[string]any{"ok": true})
	_, _, code := runCtl("pause")
	if code != 0 {
		t.Fatalf("pause: exit %d", code)
	}
	if env.requests[0].Method != "POST" || env.requests[0].Path != "/v1/queue/pause" {
		t.Errorf("pause wire: %s %s", env.requests[0].Method, env.requests[0].Path)
	}
	_, _, code = runCtl("resume")
	if code != 0 {
		t.Fatalf("resume: exit %d", code)
	}
	if env.requests[1].Path != "/v1/queue/resume" {
		t.Errorf("resume path: got %q", env.requests[1].Path)
	}
}

func TestCtl_Shutdown(t *testing.T) {
	env := newCtlTestEnv(t, "tk-shut", map[string]any{"ok": true})
	_, _, code := runCtl("shutdown")
	if code != 0 {
		t.Fatalf("shutdown: exit %d", code)
	}
	if env.requests[0].Path != "/v1/queue/pause" {
		t.Errorf("shutdown maps to pause; got %q", env.requests[0].Path)
	}
}

func TestCtl_SessionsKill(t *testing.T) {
	env := newCtlTestEnv(t, "tk-kill", map[string]any{"ok": true})
	_, _, code := runCtl("sessions", "kill", "t-42")
	if code != 0 {
		t.Fatalf("sessions kill: exit %d", code)
	}
	r := env.requests[0]
	if r.Method != "POST" || r.Path != "/v1/queue/tasks/cancel" {
		t.Errorf("kill wire: %s %s", r.Method, r.Path)
	}
	var body map[string]any
	json.Unmarshal(r.Body, &body)
	if body["id"] != "t-42" {
		t.Errorf("kill id: got %v, want t-42", body["id"])
	}
}

func TestCtl_UnknownVerbRejected(t *testing.T) {
	newCtlTestEnv(t, "tk-unk", map[string]any{})
	_, stderr, code := runCtl("nonsense-verb")
	if code != 2 {
		t.Errorf("unknown verb: exit %d (want 2); stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "unknown verb") {
		t.Errorf("unknown verb: stderr should mention it; got %q", stderr)
	}
}

func TestCtl_DiscoveryMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("R1_HOME", dir)
	// daemon.json absent — discover must fail with a clear error and
	// non-zero exit, not panic.
	_, stderr, code := runCtl("status")
	if code == 0 {
		t.Errorf("missing discovery: want non-zero exit, got 0")
	}
	if !strings.Contains(stderr, "discovery") {
		t.Errorf("missing discovery: stderr should mention discovery; got %q", stderr)
	}
	_ = filepath.Join // keep import
}
