package agentserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/executor"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeExecutor satisfies executor.Executor for tests. Configurable
// success / error / delay so we can exercise every handler path.
type fakeExecutor struct {
	t       executor.TaskType
	fn      func(ctx context.Context, p executor.Plan) (executor.Deliverable, error)
}

func (f *fakeExecutor) TaskType() executor.TaskType { return f.t }
func (f *fakeExecutor) Execute(ctx context.Context, p executor.Plan, _ executor.EffortLevel) (executor.Deliverable, error) {
	return f.fn(ctx, p)
}
func (f *fakeExecutor) BuildCriteria(_ executor.Task, _ executor.Deliverable) []plan.AcceptanceCriterion {
	return nil
}
func (f *fakeExecutor) BuildRepairFunc(_ executor.Plan) executor.RepairFunc { return nil }
func (f *fakeExecutor) BuildEnvFixFunc() executor.EnvFixFunc {
	return func(context.Context, string, string) bool { return false }
}

type fakeDeliverable struct {
	summary string
	size    int
}

func (f fakeDeliverable) Summary() string { return f.summary }
func (f fakeDeliverable) Size() int       { return f.size }

func newTestServer(t *testing.T, cfg Config) (*Server, *httptest.Server) {
	t.Helper()
	if cfg.Version == "" {
		cfg.Version = "test"
	}
	s := NewServer(cfg)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func okExecutor(tt executor.TaskType) *fakeExecutor {
	return &fakeExecutor{
		t: tt,
		fn: func(_ context.Context, p executor.Plan) (executor.Deliverable, error) {
			return fakeDeliverable{summary: "done " + p.Query, size: 42}, nil
		},
	}
}

func TestCapabilities(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: okExecutor(executor.TaskResearch),
		},
	})
	resp, err := http.Get(ts.URL + "/api/capabilities")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var caps Capabilities
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if caps.Version != "test" {
		t.Errorf("version=%q", caps.Version)
	}
	found := false
	for _, tt := range caps.TaskTypes {
		if tt == "research" {
			found = true
		}
	}
	if !found {
		t.Errorf("research not advertised: %+v", caps.TaskTypes)
	}
	if caps.RequiresAuth {
		t.Error("no bearer configured, requires_auth should be false")
	}
}

func TestCreateTaskSuccess(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: okExecutor(executor.TaskResearch),
		},
	})
	body, _ := json.Marshal(TaskRequest{
		TaskType:    "research",
		Description: "find FINTRAC MSB thresholds",
		Query:       "FINTRAC MSB",
	})
	resp, err := http.Post(ts.URL+"/api/task", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var st TaskState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Status != "completed" {
		t.Errorf("status=%q", st.Status)
	}
	if !strings.HasPrefix(st.ID, "t-") {
		t.Errorf("id=%q", st.ID)
	}
	if st.Summary != "done FINTRAC MSB" {
		t.Errorf("summary=%q", st.Summary)
	}

	// GET /api/task/{id} returns the same state.
	resp2, err := http.Get(ts.URL + "/api/task/" + st.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp2.Body.Close()
	var got TaskState
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != st.ID || got.Summary != st.Summary {
		t.Errorf("round-trip mismatch: %+v vs %+v", got, st)
	}
}

func TestCreateTaskExecutorError(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: &fakeExecutor{
				t: executor.TaskResearch,
				fn: func(context.Context, executor.Plan) (executor.Deliverable, error) {
					return nil, errors.New("boom")
				},
			},
		},
	})
	body, _ := json.Marshal(TaskRequest{TaskType: "research", Query: "x"})
	resp, err := http.Post(ts.URL+"/api/task", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var st TaskState
	json.NewDecoder(resp.Body).Decode(&st)
	if st.Status != "failed" {
		t.Errorf("status=%q", st.Status)
	}
	if !strings.Contains(st.Error, "boom") {
		t.Errorf("error missing marker: %q", st.Error)
	}
}

func TestCreateTaskUnknownType(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: okExecutor(executor.TaskResearch),
		},
	})
	body, _ := json.Marshal(map[string]any{"task_type": "martian", "description": "x"})
	resp, err := http.Post(ts.URL+"/api/task", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

func TestCreateTaskNoExecutorRegistered(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: okExecutor(executor.TaskResearch),
		},
	})
	body, _ := json.Marshal(TaskRequest{TaskType: "deploy", Description: "x"})
	resp, err := http.Post(ts.URL+"/api/task", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("status=%d, want 400 for missing executor", resp.StatusCode)
	}
}

func TestCreateTaskEmptyBody(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: okExecutor(executor.TaskResearch),
		},
	})
	resp, err := http.Post(ts.URL+"/api/task", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

func TestGetTaskUnknown(t *testing.T) {
	_, ts := newTestServer(t, Config{})
	resp, err := http.Get(ts.URL + "/api/task/t-none")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

func TestAuthMissingHeader(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Bearer: []string{"t1"},
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: okExecutor(executor.TaskResearch),
		},
	})
	body, _ := json.Marshal(TaskRequest{TaskType: "research", Query: "x"})
	resp, err := http.Post(ts.URL+"/api/task", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("status=%d", resp.StatusCode)
	}
}

func TestAuthWrongHeader(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Bearer: []string{"t1"},
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: okExecutor(executor.TaskResearch),
		},
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/task",
		strings.NewReader(`{"task_type":"research","query":"x"}`))
	req.Header.Set("X-Stoke-Bearer", "NOT-THE-TOKEN")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("status=%d", resp.StatusCode)
	}
}

func TestAuthCorrectHeader(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Bearer: []string{"t1", "t2"},
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: okExecutor(executor.TaskResearch),
		},
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/task",
		strings.NewReader(`{"task_type":"research","query":"x"}`))
	req.Header.Set("X-Stoke-Bearer", "t2")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d", resp.StatusCode)
	}
}

func TestAuthCapabilitiesOpen(t *testing.T) {
	// Capabilities is public even when bearer is set — discovery
	// needs to work before you have a token.
	_, ts := newTestServer(t, Config{Bearer: []string{"t1"}})
	resp, err := http.Get(ts.URL + "/api/capabilities")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d", resp.StatusCode)
	}
	var caps Capabilities
	json.NewDecoder(resp.Body).Decode(&caps)
	if !caps.RequiresAuth {
		t.Error("requires_auth should be true when bearer list non-empty")
	}
}

func TestTaskTimeout(t *testing.T) {
	// Executor that waits longer than the task timeout — the ctx
	// cancel should surface as a failure.
	ex := &fakeExecutor{
		t: executor.TaskResearch,
		fn: func(ctx context.Context, _ executor.Plan) (executor.Deliverable, error) {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("timed out: %w", ctx.Err())
			case <-time.After(2 * time.Second):
				return fakeDeliverable{summary: "never"}, nil
			}
		},
	}
	_, ts := newTestServer(t, Config{
		TaskTimeout: 100 * time.Millisecond,
		Executors: map[executor.TaskType]executor.Executor{
			executor.TaskResearch: ex,
		},
	})
	body, _ := json.Marshal(TaskRequest{TaskType: "research", Query: "slow"})
	resp, err := http.Post(ts.URL+"/api/task", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var st TaskState
	json.NewDecoder(resp.Body).Decode(&st)
	if st.Status != "failed" {
		t.Errorf("status=%q, want failed", st.Status)
	}
	if !strings.Contains(st.Error, "timed out") {
		t.Errorf("error missing timeout marker: %q", st.Error)
	}
}

// fakeProvider satisfies provider.Provider for unit tests. Returns a
// fixed text response without hitting any external API.
type fakeProvider struct {
	reply string
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Chat(_ provider.ChatRequest) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{
		ID:         "fake-resp",
		Model:      "fake",
		Content:    []provider.ResponseContent{{Type: "text", Text: f.reply}},
		StopReason: "end_turn",
		Usage:      stream.TokenUsage{Input: 10, Output: 5},
	}, nil
}

func (f *fakeProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	resp, err := f.Chat(req)
	if err != nil {
		return nil, err
	}
	if onEvent != nil {
		onEvent(stream.Event{DeltaText: f.reply})
	}
	return resp, nil
}

func TestChatCompletions_ReturnsOpenAIFormat(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Provider: &fakeProvider{reply: "hello from R1"},
	})

	body, _ := json.Marshal(chatCompletionRequest{
		Model: "r1",
		Messages: []chatMessage{
			{Role: "user", Content: "say hello"},
		},
	})
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	var cr chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cr.Object != "chat.completion" {
		t.Errorf("object=%q want chat.completion", cr.Object)
	}
	if len(cr.Choices) != 1 {
		t.Fatalf("choices len=%d", len(cr.Choices))
	}
	if cr.Choices[0].Message.Role != "assistant" {
		t.Errorf("role=%q", cr.Choices[0].Message.Role)
	}
	if cr.Choices[0].Message.Content != "hello from R1" {
		t.Errorf("content=%q", cr.Choices[0].Message.Content)
	}
	if cr.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason=%q", cr.Choices[0].FinishReason)
	}
}

func TestChatCompletions_StreamReturnsOpenAISSE(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Provider: &fakeProvider{reply: "hello from R1"},
	})

	body, _ := json.Marshal(chatCompletionRequest{
		Model:  "r1",
		Stream: true,
		Messages: []chatMessage{
			{Role: "user", Content: "say hello"},
		},
	})
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type=%q", ct)
	}

	streamBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyText := string(streamBody)
	if !strings.Contains(bodyText, "\"object\":\"chat.completion.chunk\"") {
		t.Fatalf("missing chunk envelope: %s", bodyText)
	}
	if !strings.Contains(bodyText, "\"role\":\"assistant\"") {
		t.Fatalf("missing assistant role: %s", bodyText)
	}
	if !strings.Contains(bodyText, "\"content\":\"hello from R1\"") {
		t.Fatalf("missing streamed content: %s", bodyText)
	}
	if !strings.Contains(bodyText, "\"finish_reason\":\"stop\"") {
		t.Fatalf("missing stop chunk: %s", bodyText)
	}
	if !strings.Contains(bodyText, "data: [DONE]") {
		t.Fatalf("missing done marker: %s", bodyText)
	}
}

func TestChatCompletions_EmptyMessagesRejects(t *testing.T) {
	_, ts := newTestServer(t, Config{
		Provider: &fakeProvider{reply: "x"},
	})

	body, _ := json.Marshal(chatCompletionRequest{Model: "r1"})
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400", resp.StatusCode)
	}
}
