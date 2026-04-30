package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

type fakeAgentProvider struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeAgentProvider) Name() string { return "fake-agent" }

func (f *fakeAgentProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return f.chat(req, nil)
}

func (f *fakeAgentProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	return f.chat(req, onEvent)
}

func (f *fakeAgentProvider) chat(_ provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return &provider.ChatResponse{
			ID: "resp-1",
			Content: []provider.ResponseContent{{
				Type:  "tool_use",
				ID:    "tool-1",
				Name:  "dispatch_build",
				Input: map[string]interface{}{"description": "implement agent interaction mode"},
			}},
			StopReason: "tool_use",
		}, nil
	}
	if onEvent != nil {
		onEvent(stream.Event{DeltaText: "Queued it for execution."})
	}
	return &provider.ChatResponse{
		ID: "resp-2",
		Content: []provider.ResponseContent{{
			Type: "text",
			Text: "Queued it for execution.",
		}},
		StopReason: "end_turn",
	}, nil
}

func newAgentDaemon(t *testing.T, exec Executor, prov provider.Provider, workers int) (*Daemon, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	d, err := New(Config{
		StateDir:     dir,
		Addr:         "127.0.0.1:0",
		MaxParallel:  0,
		PollGap:      10,
		ChatProvider: prov,
	}, exec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if workers > 0 {
		d.Resize(workers)
	}
	ts := httptest.NewServer(d.Handler())
	t.Cleanup(func() {
		ts.Close()
		d.Stop()
	})
	return d, ts
}

func createSession(t *testing.T, baseURL string) agentSessionCreateResponse {
	t.Helper()
	body := strings.NewReader(`{"agent_id":"claude-opus","capabilities":["enqueue","query","redirect"]}`)
	resp, err := http.Post(baseURL+"/agent/session", "application/json", body)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create session status=%d", resp.StatusCode)
	}
	var out agentSessionCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create session: %v", err)
	}
	if !strings.HasPrefix(out.SessionID, "agent-") || out.Token == "" {
		t.Fatalf("bad session create response: %+v", out)
	}
	return out
}

func postAgentChat(t *testing.T, baseURL string, sess agentSessionCreateResponse, messageType, message string) agentChatResponse {
	t.Helper()
	body, _ := json.Marshal(agentChatRequest{
		SessionID:   sess.SessionID,
		Message:     message,
		MessageType: messageType,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/agent/chat", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chat request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("chat status=%d body=%s", resp.StatusCode, raw)
	}
	var out agentChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode chat: %v", err)
	}
	return out
}

func TestAgentSessionCreateAndFallbackChat(t *testing.T) {
	d, ts := newAgentDaemon(t, NoopExecutor{OutBase: filepath.Join(t.TempDir(), "proofs")}, nil, 0)
	sess := createSession(t, ts.URL)

	resp := postAgentChat(t, ts.URL, sess, "task", "Implement the HTTP agent interface.")
	if resp.CurrentState != "running" {
		t.Fatalf("current_state=%q want running", resp.CurrentState)
	}
	if len(resp.TaskIDsAffected) != 1 {
		t.Fatalf("task_ids=%v want 1", resp.TaskIDsAffected)
	}
	task := d.queue.Get(resp.TaskIDsAffected[0])
	if task == nil {
		t.Fatalf("queued task missing")
	}
	if task.Meta["agent_session_id"] != sess.SessionID {
		t.Fatalf("task meta missing session id: %+v", task.Meta)
	}
}

func TestAgentChatUsesProviderDispatcher(t *testing.T) {
	d, ts := newAgentDaemon(t, NoopExecutor{OutBase: filepath.Join(t.TempDir(), "proofs")}, &fakeAgentProvider{}, 0)
	sess := createSession(t, ts.URL)

	resp := postAgentChat(t, ts.URL, sess, "task", "Please build the new interface.")
	if !strings.Contains(resp.Reply, "Queued it for execution") {
		t.Fatalf("reply=%q", resp.Reply)
	}
	if len(resp.TaskIDsAffected) != 1 {
		t.Fatalf("task_ids=%v want 1", resp.TaskIDsAffected)
	}
	task := d.queue.Get(resp.TaskIDsAffected[0])
	if task == nil || task.Meta["agent_action"] != "build" {
		t.Fatalf("expected build task, got %+v", task)
	}
}

func TestAgentRedirectAndAbort(t *testing.T) {
	d, ts := newAgentDaemon(t, NoopExecutor{OutBase: filepath.Join(t.TempDir(), "proofs")}, nil, 0)
	sess := createSession(t, ts.URL)

	taskResp := postAgentChat(t, ts.URL, sess, "task", "Queue a baseline task.")
	redirectResp := postAgentChat(t, ts.URL, sess, "redirect", "Redirect the work toward the daemon endpoints.")
	if len(redirectResp.TaskIDsAffected) != 1 {
		t.Fatalf("redirect task_ids=%v", redirectResp.TaskIDsAffected)
	}
	redirectTask := d.queue.Get(redirectResp.TaskIDsAffected[0])
	if redirectTask == nil || redirectTask.Meta["agent_action"] != "redirect" {
		t.Fatalf("redirect task malformed: %+v", redirectTask)
	}

	abortResp := postAgentChat(t, ts.URL, sess, "abort", "stop queued work")
	if len(abortResp.TaskIDsAffected) != 2 {
		t.Fatalf("abort task_ids=%v want 2", abortResp.TaskIDsAffected)
	}
	for _, taskID := range append([]string(nil), taskResp.TaskIDsAffected...) {
		got := d.queue.Get(taskID)
		if got == nil || got.State != StateCancelled {
			t.Fatalf("task %s not cancelled: %+v", taskID, got)
		}
	}
	gotRedirect := d.queue.Get(redirectResp.TaskIDsAffected[0])
	if gotRedirect == nil || gotRedirect.State != StateCancelled {
		t.Fatalf("redirect task not cancelled: %+v", gotRedirect)
	}
}

func TestAgentFollowUpQueuesDerivedTask(t *testing.T) {
	d, ts := newAgentDaemon(t, NoopExecutor{OutBase: filepath.Join(t.TempDir(), "proofs")}, nil, 0)
	sess := createSession(t, ts.URL)
	taskResp := postAgentChat(t, ts.URL, sess, "task", "Build the baseline daemon interface.")

	body, _ := json.Marshal(agentFollowUpRequest{
		SessionID:    sess.SessionID,
		ParentTaskID: taskResp.TaskIDsAffected[0],
		NewContext:   "Also add a session events stream.",
	})
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/agent/follow-up", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new follow-up request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("follow-up request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("follow-up status=%d body=%s", resp.StatusCode, raw)
	}
	var out agentFollowUpResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode follow-up: %v", err)
	}
	if out.NewTaskID == "" || out.WillReplayFrom != "state" {
		t.Fatalf("bad follow-up response: %+v", out)
	}
	task := d.queue.Get(out.NewTaskID)
	if task == nil {
		t.Fatalf("follow-up task missing")
	}
	if !strings.Contains(task.Prompt, "Follow-up context") || !strings.Contains(task.Prompt, "session events stream") {
		t.Fatalf("follow-up prompt=%q", task.Prompt)
	}
}

func TestAgentEventsStreamReplaysBacklog(t *testing.T) {
	d, ts := newAgentDaemon(t, NoopExecutor{OutBase: filepath.Join(t.TempDir(), "proofs")}, nil, 1)
	sess := createSession(t, ts.URL)
	taskResp := postAgentChat(t, ts.URL, sess, "task", "Run through the worker pool.")
	if err := pollUntilDone(d, taskResp.TaskIDsAffected, 3*time.Second); err != nil {
		t.Fatalf("pollUntilDone: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/agent/events?session_id="+sess.SessionID+"&since=0", nil)
	if err != nil {
		t.Fatalf("new events request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status=%d", resp.StatusCode)
	}

	events := readAgentEvents(t, resp.Body, 4, 2*time.Second)
	var sawEnqueue, sawStarted, sawCompleted bool
	for _, ev := range events {
		switch ev.Type {
		case "task.enqueued":
			sawEnqueue = true
		case "task.started":
			sawStarted = true
		case "task.completed":
			sawCompleted = true
		}
	}
	if !sawEnqueue || !sawStarted || !sawCompleted {
		t.Fatalf("missing expected events: %+v", events)
	}
}

func readAgentEvents(t *testing.T, r io.Reader, want int, timeout time.Duration) []AgentEvent {
	t.Helper()
	eventsCh := make(chan []AgentEvent, 1)
	go func() {
		scan := bufio.NewScanner(r)
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		events := make([]AgentEvent, 0, want)
		for scan.Scan() {
			line := scan.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev AgentEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				continue
			}
			events = append(events, ev)
			if len(events) >= want {
				eventsCh <- events
				return
			}
		}
		eventsCh <- events
	}()
	select {
	case events := <-eventsCh:
		return events
	case <-time.After(timeout):
		return nil
	}
}
