// Package agentserve implements Task 24 — the hireable-agent HTTP
// facade. Third-party agents (or TrustPlane) POST a task and get a
// verified result back.
//
// Distinct from internal/server, which runs the mission-orchestrator
// API consumed by stoke-server / dashboards. agentserve focuses
// narrowly on "hire this Stoke to do a task and verify it":
//
//   GET  /api/capabilities          — what this Stoke advertises
//   POST /api/task                  — submit a task, returns task state
//   GET  /api/task/{id}             — poll status + deliverable
//
// MVP is synchronous: POST /api/task blocks until the executor
// returns (or the per-task timeout fires). A future commit moves to
// async + webhook callbacks without breaking the response shape.
//
// Auth: optional X-Stoke-Bearer header; accepted tokens come from
// Config.Bearer (typically loaded from STOKE_SERVE_TOKENS env).
// Empty list = no auth (localhost dev default). Do NOT expose the
// open listener on the public internet.
package agentserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/eventlog"
	"github.com/RelayOne/r1/internal/executor"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/google/uuid"
)

// Capabilities is the JSON returned by GET /api/capabilities.
type Capabilities struct {
	Version      string   `json:"version"`
	TaskTypes    []string `json:"task_types"`
	BudgetUSD    float64  `json:"budget_usd"`
	RequiresAuth bool     `json:"requires_auth"`
}

// TaskRequest is the POST /api/task body.
type TaskRequest struct {
	TaskType    string         `json:"task_type"`
	Description string         `json:"description"`
	Query       string         `json:"query,omitempty"`
	Budget      float64        `json:"budget,omitempty"`
	Effort      string         `json:"effort,omitempty"`
	Spec        string         `json:"spec,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// TaskState is the JSON returned by POST /api/task and
// GET /api/task/{id}.
type TaskState struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	TaskType    string     `json:"task_type"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Size        int        `json:"size,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// Config plumbs dependencies into a Server.
type Config struct {
	Version      string
	Capabilities Capabilities
	Executors    map[executor.TaskType]executor.Executor
	Bearer       []string
	TaskTimeout  time.Duration

	// EventLog, if non-nil, receives an agentserve.task.<state> event
	// for every task lifecycle transition via eventlog.EmitBus. Bus
	// must also be non-nil when EventLog is set. Both nil = in-memory
	// only (SSE still works via the internal broadcaster).
	EventLog *eventlog.Log
	Bus      *bus.Bus

	// OnTaskComplete, when non-nil, fires after every terminal task
	// transition (completed or failed) with the task ID, a pass/fail
	// flag, and any evidence payloads the executor emitted. Wired by
	// `stoke agent-serve --trustplane-register` to drive Settle /
	// Dispute calls against the TrustPlane gateway (TASK-T20); keep
	// nil when running standalone.
	//
	// The callback runs synchronously on the task goroutine after the
	// terminal SSE/eventlog frame has been flushed. It must not block
	// for long; spawn a goroutine inside the callback if you need
	// durable async work. Use Server.TaskMetadata(id) from the
	// callback to recover the original request's Extra map (e.g.
	// contract_id) without widening this signature.
	OnTaskComplete func(taskID string, passed bool, evidence [][]byte)

	// Provider, if non-nil, is used by the /v1/chat/completions endpoint
	// to run requests through R1's agentloop. When nil, a provider is
	// constructed from ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL env vars.
	Provider provider.Provider
}

// Server is an agentserve instance. Tasks + deliverables are kept
// in memory for the MVP; a future cycle swaps this for persistence.
type Server struct {
	cfg     Config
	mu      sync.Mutex
	tasks   map[string]*TaskState
	results map[string]executor.Deliverable

	// cancels holds the CancelFunc for every live task so
	// POST /api/task/{id}/cancel can abort in-flight work. Entries
	// are cleared on terminal transition. Guarded by mu.
	cancels map[string]context.CancelFunc

	// subs maps task ID -> active SSE subscriber channels. Each
	// subscriber gets a buffered channel; the handler drains it and
	// writes SSE frames. Channel is closed on terminal transition so
	// the HTTP handler knows to send `event: end` + return. Guarded
	// by mu.
	subs map[string][]chan taskEvent

	// meta maps task ID -> the TaskRequest.Extra the caller submitted
	// (TrustPlane contract_id, delegation id, etc.). Read by the
	// OnTaskComplete callback via TaskMetadata so settlement wiring
	// recovers the contract without the agentserve package importing
	// hire/trustplane concerns. Guarded by mu.
	meta map[string]map[string]any
}

// taskEvent is the internal SSE payload. Kind mirrors the event type
// (queued/started/completed/failed/cancelled) and State is a snapshot
// of the task's current TaskState. Terminal == true signals the SSE
// writer to close the stream after flushing.
type taskEvent struct {
	Kind     string    `json:"kind"`
	State    TaskState `json:"state"`
	Terminal bool      `json:"terminal"`
}

// NewServer returns a fresh Server.
func NewServer(cfg Config) *Server {
	if cfg.TaskTimeout <= 0 {
		cfg.TaskTimeout = 10 * time.Minute
	}
	if cfg.Executors == nil {
		cfg.Executors = map[executor.TaskType]executor.Executor{}
	}
	cfg.Capabilities.RequiresAuth = len(cfg.Bearer) > 0
	return &Server{
		cfg:     cfg,
		tasks:   map[string]*TaskState{},
		results: map[string]executor.Deliverable{},
		cancels: map[string]context.CancelFunc{},
		subs:    map[string][]chan taskEvent{},
		meta:    map[string]map[string]any{},
	}
}

// TaskMetadata returns a shallow copy of the TaskRequest.Extra map
// submitted for id, or nil when the task is unknown or the caller
// submitted no metadata. Used by TASK-T20 settlement callbacks to
// extract `contract_id` and related routing keys from inside an
// OnTaskComplete callback without broadening its signature.
func (s *Server) TaskMetadata(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.meta[id]
	if !ok || len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// SetOnTaskComplete swaps the terminal-transition callback after
// construction. Wired by cmd/stoke/agent_serve_cmd.go when
// --trustplane-register is set: the callback needs the *Server so it
// can look up TaskMetadata, which is only available post-NewServer.
// Safe to call before the server receives its first task.
func (s *Server) SetOnTaskComplete(fn func(taskID string, passed bool, evidence [][]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.OnTaskComplete = fn
}

// Handler returns the wired mux with auth middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/capabilities", s.handleCapabilities)
	mux.HandleFunc("POST /api/task", s.handleCreateTask)
	mux.HandleFunc("GET /api/task/{id}", s.handleGetTask)
	mux.HandleFunc("POST /api/task/{id}/cancel", s.handleCancelTask)
	mux.HandleFunc("GET /api/task/{id}/events", s.handleTaskEvents)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	return s.withAuth(mux)
}

// Config returns the server's effective configuration. Safe
// post-construction read; exposes values the CLI and tests use.
func (s *Server) Config() Config { return s.cfg }

// withAuth validates X-Stoke-Bearer when Config.Bearer is non-empty.
// Capabilities endpoint is public so discovery works without a
// token; task endpoints require auth when any token is registered.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.Bearer) == 0 || r.URL.Path == "/api/capabilities" {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("X-Stoke-Bearer")
		if got == "" {
			writeErr(w, http.StatusUnauthorized, "missing X-Stoke-Bearer")
			return
		}
		for _, t := range s.cfg.Bearer {
			if t == got {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeErr(w, http.StatusUnauthorized, "invalid X-Stoke-Bearer")
	})
}

func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	caps := s.cfg.Capabilities
	caps.Version = s.cfg.Version
	if len(caps.TaskTypes) == 0 {
		for t := range s.cfg.Executors {
			caps.TaskTypes = append(caps.TaskTypes, t.String())
		}
	}
	caps.RequiresAuth = len(s.cfg.Bearer) > 0
	writeJSON(w, http.StatusOK, caps)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "decode body: %v", err)
		return
	}
	if strings.TrimSpace(req.TaskType) == "" {
		writeErr(w, http.StatusBadRequest, "task_type required")
		return
	}
	if strings.TrimSpace(req.Description) == "" && strings.TrimSpace(req.Query) == "" {
		writeErr(w, http.StatusBadRequest, "description or query required")
		return
	}

	tt := parseTaskType(req.TaskType)
	if tt == executor.TaskUnknown {
		writeErr(w, http.StatusBadRequest, "unknown task_type %q", req.TaskType)
		return
	}
	ex, ok := s.cfg.Executors[tt]
	if !ok {
		writeErr(w, http.StatusBadRequest, "no executor registered for %s", req.TaskType)
		return
	}

	now := time.Now().UTC()
	state := &TaskState{
		ID:        "t-" + uuid.NewString(),
		Status:    "queued",
		TaskType:  req.TaskType,
		CreatedAt: now,
	}
	// Derive from context.Background so a client-initiated disconnect
	// does not abort an in-flight task; POST /cancel is the only
	// abort path. TaskTimeout still bounds the run.
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.TaskTimeout)
	defer cancel()

	s.mu.Lock()
	s.tasks[state.ID] = state
	s.cancels[state.ID] = cancel
	if len(req.Extra) > 0 {
		metaCopy := make(map[string]any, len(req.Extra))
		for k, v := range req.Extra {
			metaCopy[k] = v
		}
		s.meta[state.ID] = metaCopy
	}
	s.mu.Unlock()
	s.emitTaskEvent(state, "queued", false)

	// Synchronous MVP: run inline. Future: spawn into a worker pool
	// and return 202 immediately.
	s.runTask(ctx, state, req, ex)

	s.mu.Lock()
	snapshot := *state
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	state, ok := s.tasks[id]
	var snapshot TaskState
	if ok {
		snapshot = *state
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "task %q not found", id)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// runTask blocks until the executor returns. Results + state
// transitions are updated under s.mu so concurrent GETs see a
// consistent view.
func (s *Server) runTask(ctx context.Context, state *TaskState, req TaskRequest, ex executor.Executor) {
	s.mu.Lock()
	// If /cancel already flipped the state to cancelled before the
	// worker reached here, honor it and bail. Emission already
	// happened in handleCancelTask; clear the cancel func so the
	// handler's deferred call is a no-op.
	if state.Status == taskStatusCancelled {
		s.mu.Unlock()
		s.clearLive(state.ID)
		return
	}
	startedAt := time.Now().UTC()
	state.Status = "running"
	state.StartedAt = &startedAt
	s.mu.Unlock()
	s.emitTaskEvent(state, "started", false)

	plan := executor.Plan{
		ID: state.ID,
		Task: executor.Task{
			ID:          state.ID,
			Description: req.Description,
			Spec:        req.Spec,
			Budget:      req.Budget,
		},
		Query: req.Query,
		Extra: req.Extra,
	}
	if plan.Query == "" {
		plan.Query = req.Description
	}

	deliverable, err := ex.Execute(ctx, plan, executor.EffortLevelFromString(req.Effort))
	completedAt := time.Now().UTC()

	s.mu.Lock()
	state.CompletedAt = &completedAt
	// If /cancel fired during the Execute call, prefer the cancelled
	// state even if the executor returned a wrapped ctx error.
	if state.Status == taskStatusCancelled {
		s.mu.Unlock()
		s.clearLive(state.ID)
		s.emitTaskEvent(state, taskStatusCancelled, true)
		return
	}
	kind := taskStatusCompleted
	if err != nil {
		state.Status = taskStatusFailed
		state.Error = err.Error()
		kind = taskStatusFailed
	} else {
		state.Status = taskStatusCompleted
		if deliverable != nil {
			state.Summary = deliverable.Summary()
			state.Size = deliverable.Size()
			s.results[state.ID] = deliverable
		}
	}
	cb := s.cfg.OnTaskComplete
	taskID := state.ID
	passed := err == nil
	s.mu.Unlock()
	s.clearLive(state.ID)
	s.emitTaskEvent(state, kind, true)

	// TASK-T20 settlement hook. Fires after the terminal SSE/eventlog
	// frame has been flushed so observers see the completion before
	// the external Settle/Dispute round-trip begins. Evidence is
	// sourced from the Deliverable's optional EvidenceSamples method
	// (see evidenceSampler); nil when the deliverable doesn't opt in.
	if cb != nil {
		cb(taskID, passed, collectEvidence(deliverable, err))
	}
}

// evidenceSampler is the optional contract an executor.Deliverable
// can satisfy to contribute Dispute evidence. Non-deliverable
// surfaces (failures, nil deliverable) fall back to nil so the
// callback still fires with a usable signature.
type evidenceSampler interface {
	EvidenceSamples() [][]byte
}

// collectEvidence returns the evidence payloads associated with a
// terminal task. On success it queries the deliverable; on failure it
// surfaces the error message as a single evidence byte slice so the
// far side has something to anchor a dispute on.
func collectEvidence(d executor.Deliverable, err error) [][]byte {
	if err != nil {
		if err.Error() == "" {
			return nil
		}
		return [][]byte{[]byte(err.Error())}
	}
	if d == nil {
		return nil
	}
	if s, ok := d.(evidenceSampler); ok {
		return s.EvidenceSamples()
	}
	return nil
}

// clearLive removes the cancel func for id and invokes it to release
// any context resources. Safe to call multiple times.
func (s *Server) clearLive(id string) {
	s.mu.Lock()
	cancel, ok := s.cancels[id]
	if ok {
		delete(s.cancels, id)
	}
	s.mu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

// parseTaskType maps a wire string onto a TaskType enum value.
// Unknown inputs return TaskUnknown so the handler can 400.
func parseTaskType(s string) executor.TaskType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "code":
		return executor.TaskCode
	case "research":
		return executor.TaskResearch
	case "browser", "browse":
		return executor.TaskBrowser
	case "deploy":
		return executor.TaskDeploy
	case "delegate", "delegation":
		return executor.TaskDelegate
	case "chat":
		return executor.TaskChat
	default:
		return executor.TaskUnknown
	}
}

// chatMessage mirrors the OpenAI ChatCompletion message shape.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatCompletionRequest is the subset of the OpenAI ChatCompletion
// request body that this handler consumes.
type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream,omitempty"`
}

// chatCompletionResponse is an OpenAI-compatible non-streaming response.
type chatCompletionResponse struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []chatChoice        `json:"choices"`
	Usage   chatCompletionUsage `json:"usage"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatCompletionChunkResponse struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []chatChunkChoice `json:"choices"`
}

type chatChunkChoice struct {
	Index        int               `json:"index"`
	Delta        chatChunkDelta    `json:"delta"`
	FinishReason *string           `json:"finish_reason"`
}

type chatChunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// handleChatCompletions implements POST /v1/chat/completions.
//
// The handler accepts an OpenAI-format ChatCompletion request and routes it
// through R1's agentloop. The last user message (or all messages concatenated
// into a single turn) is used as the task prompt. The agentloop runs
// tool-use turns until the model emits end_turn, then the final text is
// returned in an OpenAI-compatible response envelope.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "decode body: %v", err)
		return
	}
	if len(req.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "messages required")
		return
	}

	// Build prompt: concatenate all messages in order so the agentloop
	// has the full conversation context as a single user turn.
	var sb strings.Builder
	for _, m := range req.Messages {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		role := strings.ToUpper(m.Role[:1]) + m.Role[1:]
		sb.WriteString(role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
	}
	prompt := sb.String()

	// Resolve provider: use injected provider from Config when available,
	// otherwise build one from env vars (ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL).
	prov := s.cfg.Provider
	if prov == nil {
		prov = provider.NewAnthropicProvider("", "")
	}

	model := req.Model
	if model == "" || model == "r1" {
		model = "claude-sonnet-4-5-20250929"
	}

	cfg := agentloop.Config{
		Model:        model,
		MaxTurns:     25,
		SystemPrompt: "You are R1, an AI assistant powered by the Stoke agent loop.",
	}

	timeout := s.cfg.TaskTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	loop := agentloop.New(prov, cfg, nil, nil)
	responseID := "r1-" + uuid.NewString()
	createdAt := time.Now().Unix()

	if req.Stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")

		assistantRole := "assistant"
		loop.SetOnText(func(delta string) {
			chunk := chatCompletionChunkResponse{
				ID:      responseID,
				Object:  "chat.completion.chunk",
				Created: createdAt,
				Model:   model,
				Choices: []chatChunkChoice{
					{
						Index: 0,
						Delta: chatChunkDelta{
							Role:    assistantRole,
							Content: delta,
						},
						FinishReason: nil,
					},
				},
			}
			assistantRole = ""
			writeSSEData(w, chunk)
			flusher.Flush()
		})

		result, err := loop.Run(ctx, prompt)
		if err != nil {
			writeSSEData(w, map[string]string{"error": fmt.Sprintf("agent loop error: %v", err)})
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		finishReason := "stop"
		writeSSEData(w, chatCompletionChunkResponse{
			ID:      responseID,
			Object:  "chat.completion.chunk",
			Created: createdAt,
			Model:   model,
			Choices: []chatChunkChoice{
				{
					Index:        0,
					Delta:        chatChunkDelta{},
					FinishReason: &finishReason,
				},
			},
		})
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		_ = result
		return
	}

	result, err := loop.Run(ctx, prompt)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "agent loop error: %v", err)
		return
	}

	resp := chatCompletionResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: createdAt,
		Model:   model,
		Choices: []chatChoice{
			{
				Index:        0,
				Message:      chatMessage{Role: "assistant", Content: result.FinalText},
				FinishReason: "stop",
			},
		},
		Usage: chatCompletionUsage{
			PromptTokens:     result.TotalCost.InputTokens,
			CompletionTokens: result.TotalCost.OutputTokens,
			TotalTokens:      result.TotalCost.InputTokens + result.TotalCost.OutputTokens,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "marshal error: %v\n", err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(body)
	w.Write([]byte("\n"))
}

func writeErr(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]any{"error": fmt.Sprintf(format, args...)})
}

func writeSSEData(w http.ResponseWriter, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(w, "data: %q\n\n", fmt.Sprintf("marshal error: %v", err))
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", body)
}

// ErrNoExecutor is returned by Dispatch when no executor is
// registered for the requested task type. Retained for callers
// that want a typed check instead of the 400 body.
var ErrNoExecutor = errors.New("agentserve: no executor registered for task type")
