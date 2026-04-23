package agentserve

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/executor"
)

// TestCancelTask_Running drives a long-running executor, hits
// POST /api/task/{id}/cancel mid-flight, and asserts the state
// transitions to "cancelled" and the executor context observes
// ctx.Done() within a reasonable window.
func TestCancelTask_Running(t *testing.T) {
	started := make(chan struct{})
	ctxObserved := make(chan error, 1)
	ex := &fakeExecutor{
		t: executor.TaskResearch,
		fn: func(ctx context.Context, _ executor.Plan) (executor.Deliverable, error) {
			close(started)
			<-ctx.Done()
			ctxObserved <- ctx.Err()
			return nil, ctx.Err()
		},
	}
	srv, ts := newTestServer(t, Config{
		TaskTimeout: 5 * time.Second,
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: ex,
		},
	})

	// Kick off the task from a goroutine — the POST blocks until the
	// executor returns, which only happens after cancel fires.
	postDone := make(chan *http.Response, 1)
	postErr := make(chan error, 1)
	go func() {
		body := strings.NewReader(`{"task_type":"research","query":"slow"}`)
		resp, err := http.Post(ts.URL+"/api/task", "application/json", body)
		if err != nil {
			postErr <- err
			return
		}
		postDone <- resp
	}()

	select {
	case <-started:
	case err := <-postErr:
		t.Fatalf("post failed before executor started: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("executor never started")
	}

	id := pollFirstTaskID(t, srv)

	// Fire cancel.
	cancelReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/task/"+id+"/cancel", nil)
	cancelResp, err := http.DefaultClient.Do(cancelReq)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	defer cancelResp.Body.Close()
	if cancelResp.StatusCode != 200 {
		t.Fatalf("cancel status=%d", cancelResp.StatusCode)
	}
	var cancelled TaskState
	if err := json.NewDecoder(cancelResp.Body).Decode(&cancelled); err != nil {
		t.Fatalf("cancel decode: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("status=%q, want cancelled", cancelled.Status)
	}

	// Executor's ctx must observe cancellation promptly.
	select {
	case err := <-ctxObserved:
		if err == nil {
			t.Error("ctx err was nil inside executor")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executor ctx never observed cancel")
	}

	// Wait for the original POST to return before the test exits.
	select {
	case resp := <-postDone:
		resp.Body.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("original POST never returned")
	}
}

// TestCancelTask_NotFound verifies 404 on unknown ID.
func TestCancelTask_NotFound(t *testing.T) {
	_, ts := newTestServer(t, Config{})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/task/t-nope/cancel", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

// TestCancelTask_Terminal verifies 409 when the task has already
// completed (cancel is a no-op).
func TestCancelTask_Terminal(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: okExecutor(executor.TaskResearch),
		},
	})
	body := strings.NewReader(`{"task_type":"research","query":"fast"}`)
	resp, err := http.Post(ts.URL+"/api/task", "application/json", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var st TaskState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Status != "completed" {
		t.Fatalf("status=%q, want completed", st.Status)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/task/"+st.ID+"/cancel", nil)
	cancelResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cancelResp.Body.Close()
	if cancelResp.StatusCode != 409 {
		t.Errorf("status=%d, want 409", cancelResp.StatusCode)
	}
}

// TestSSEEventStream subscribes to /events while a task is running
// and asserts the frame sequence contains started and completed.
// The stream must close with `event: end` after the terminal frame.
func TestSSEEventStream(t *testing.T) {
	release := make(chan struct{})
	ex := &fakeExecutor{
		t: executor.TaskResearch,
		fn: func(ctx context.Context, _ executor.Plan) (executor.Deliverable, error) {
			<-release
			return fakeDeliverable{summary: "sse-done", size: 1}, nil
		},
	}
	srv, ts := newTestServer(t, Config{
		TaskTimeout: 5 * time.Second,
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: ex,
		},
	})

	// Create the task asynchronously.
	postDone := make(chan struct{})
	go func() {
		defer close(postDone)
		resp, err := http.Post(ts.URL+"/api/task", "application/json",
			strings.NewReader(`{"task_type":"research","query":"sse"}`))
		if err == nil {
			resp.Body.Close()
		}
	}()

	// Wait for the task to register (running state).
	id := pollFirstTaskID(t, srv)

	// Open SSE stream.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/task/"+id+"/events", nil)
	sseResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse open: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != 200 {
		t.Fatalf("sse status=%d", sseResp.StatusCode)
	}
	if ct := sseResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type=%q", ct)
	}

	// Release the executor so it finishes.
	close(release)

	frames := readSSEFrames(t, sseResp.Body, 3*time.Second)

	// Expect priming frame (current status == "running" typically) +
	// completed frame + end sentinel. Primer kind can be "queued",
	// "running", or "started" depending on timing.
	if len(frames) < 2 {
		t.Fatalf("got %d frames, want >= 2: %+v", len(frames), frames)
	}

	// Last data frame must be the terminal completed event.
	var last taskEvent
	for _, f := range frames {
		if f.data != "" {
			if err := json.Unmarshal([]byte(f.data), &last); err != nil {
				t.Fatalf("decode frame: %v — raw=%q", err, f.data)
			}
		}
	}
	if last.Kind != "completed" {
		t.Errorf("final kind=%q, want completed (frames=%+v)", last.Kind, frames)
	}
	if !last.Terminal {
		t.Errorf("final frame Terminal=false, want true")
	}
	if last.State.Status != "completed" {
		t.Errorf("state.status=%q, want completed", last.State.Status)
	}

	// End sentinel must be present.
	sawEnd := false
	for _, f := range frames {
		if f.event == "end" {
			sawEnd = true
			break
		}
	}
	if !sawEnd {
		t.Errorf("no `event: end` sentinel seen: %+v", frames)
	}

	<-postDone
}

// TestSSEEvents_NotFound verifies 404 on SSE for an unknown task.
func TestSSEEvents_NotFound(t *testing.T) {
	_, ts := newTestServer(t, Config{})
	resp, err := http.Get(ts.URL + "/api/task/t-nope/events")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

// -- helpers ---------------------------------------------------------

// pollFirstTaskID waits until exactly one task exists on the server
// and returns its ID. Peeks into the server's tasks map directly —
// the same process holds both the server and the test, so no RPC
// round-trip is needed. Fails the test after 2s if none appears.
func pollFirstTaskID(t *testing.T, srv *Server) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		srv.mu.Lock()
		for id := range srv.tasks {
			srv.mu.Unlock()
			return id
		}
		srv.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no task ID registered within 2s")
	return ""
}

// sseFrame is one parsed Server-Sent Events frame.
type sseFrame struct {
	event string
	data  string
}

// readSSEFrames reads frames from r until the stream closes or the
// deadline is hit. It terminates early if an `event: end` frame is
// seen. Non-fatal so the caller can assert on the accumulated slice.
func readSSEFrames(t *testing.T, r io.Reader, deadline time.Duration) []sseFrame {
	t.Helper()
	done := make(chan struct{})
	var frames []sseFrame
	go func() {
		defer close(done)
		scan := bufio.NewScanner(r)
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var cur sseFrame
		for scan.Scan() {
			line := scan.Text()
			switch {
			case line == "":
				if cur.event != "" || cur.data != "" {
					frames = append(frames, cur)
					if cur.event == "end" {
						return
					}
					cur = sseFrame{}
				}
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			case strings.HasPrefix(line, "event: "):
				cur.event = strings.TrimPrefix(line, "event: ")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(deadline):
	}
	return frames
}

// Static assertions that the executor stub in server_test.go still
// satisfies the Executor interface. Keeps this file compilable if
// server_test.go evolves.
var _ executor.Executor = (*fakeExecutor)(nil)
