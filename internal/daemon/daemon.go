package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/failure"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/rules"
)

// Config configures a Daemon.
//
// All paths default to under $HOME/.stoke/ if left empty. The HTTP server
// listens on Addr (default "127.0.0.1:9090"). MaxParallel is the initial
// worker pool size (default 10) and can be changed at runtime via the
// /workers endpoint or Daemon.Resize.
type Config struct {
	StateDir     string            // base dir for queue, wal, proofs (default $HOME/.stoke)
	Addr         string            // http listen addr (default 127.0.0.1:9090)
	Token        string            // bearer token for HTTP (empty = no auth)
	MaxParallel  int               // initial worker pool size (default 10)
	PollGap      int               // worker poll interval in milliseconds (default 250)
	Hooks        []string          // shell commands to run on every task lifecycle event
	ChatProvider provider.Provider // optional provider backing /agent/chat; nil falls back to env or deterministic queueing
	ChatModel    string            // default model for restored agent chat sessions
}

// Daemon is the long-running R1 process: queue + WAL + worker pool + HTTP.
//
// Lifecycle:
//
//	d, _ := New(cfg, exec)
//	d.Start(ctx)        // begin pool + http
//	defer d.Stop()      // graceful shutdown
//	<-ctx.Done()        // wait for signal
//
// The HTTP API:
//
//	POST /enqueue              { id?, title, prompt, repo?, runner?, estimate_bytes?, priority?, tags?, meta? }
//	GET  /status               -> { workers, active, queue_counts }
//	POST /workers              { count: <int> }
//	POST /pause                pause workers (set max=0 without forgetting old size)
//	POST /resume               resume workers (restore previous size)
//	GET  /tasks                ?state=queued|running|done|failed|cancelled
//	GET  /tasks/get?id=...
//	POST /tasks/cancel         { id }
//	GET  /wal?n=100
//	POST /hooks/install        { command, event } (runtime hook injection)
//	GET  /health
//
// All endpoints return JSON. Auth is a bearer token if cfg.Token is set.
type Daemon struct {
	cfg     Config
	queue   *Queue
	wal     *WAL
	pool    *WorkerPool
	exec    Executor
	Rules   *rules.Registry
	srv     *http.Server
	mux     *http.ServeMux
	cancel  context.CancelFunc
	started atomic.Bool

	agentSessions *AgentSessionStore

	mu          sync.Mutex
	pausedSize  int // remembered size during /pause
	hooksMu     sync.RWMutex
	customHooks []customHook
}

type customHook struct {
	Event   string `json:"event"` // intent | done | blocked | enqueue | complete | fail
	Command string `json:"command"`
}

// New constructs a Daemon. exec may be nil; if so, NoopExecutor is used.
func New(cfg Config, exec Executor) (*Daemon, error) {
	if cfg.StateDir == "" {
		home, _ := homeDir()
		cfg.StateDir = filepath.Join(home, ".stoke")
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:9090"
	}
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 10
	}
	if cfg.PollGap <= 0 {
		cfg.PollGap = 250
	}
	q, err := NewQueue(filepath.Join(cfg.StateDir, "queue.json"))
	if err != nil {
		return nil, fmt.Errorf("queue: %w", err)
	}
	w, err := OpenWAL(filepath.Join(cfg.StateDir, "daemon.wal"))
	if err != nil {
		return nil, fmt.Errorf("wal: %w", err)
	}
	if exec == nil {
		exec = NoopExecutor{OutBase: filepath.Join(cfg.StateDir, "proofs")}
	}
	ruleRegistry := rules.NewFSRegistry(cfg.StateDir, nil)
	d := &Daemon{
		cfg:   cfg,
		queue: q,
		wal:   w,
		exec:  WrapExecutorWithRules(exec, ruleRegistry),
		Rules: ruleRegistry,
	}
	d.mux = http.NewServeMux()
	d.agentSessions, err = NewAgentSessionStore(filepath.Join(cfg.StateDir, "agent-sessions.json"), d, cfg.ChatProvider, cfg.ChatModel)
	if err != nil {
		return nil, fmt.Errorf("agent sessions: %w", err)
	}
	d.registerRoutes()
	return d, nil
}

// Queue exposes the underlying queue (read-only operations only — mutate
// via the HTTP API or Enqueue).
func (d *Daemon) Queue() *Queue { return d.queue }

// WAL exposes the underlying WAL.
func (d *Daemon) WAL() *WAL { return d.wal }

// Enqueue adds a task to the queue and writes an enqueue WAL event.
func (d *Daemon) Enqueue(t *Task) error {
	_, _, err := d.EnqueueTask(t)
	return err
}

// EnqueueTask persists a task, deduplicating by idempotency key when present.
func (d *Daemon) EnqueueTask(t *Task) (*Task, bool, error) {
	if t == nil {
		return nil, false, errors.New("nil task")
	}
	prepareTaskForEnqueue(t)
	stored, deduplicated, err := d.queue.EnqueueOrGet(t)
	if err != nil {
		return nil, false, err
	}
	if deduplicated {
		_ = d.wal.Append(WALEvent{
			Type:    "enqueue",
			TaskID:  stored.ID,
			Message: fmt.Sprintf("deduplicated enqueue for %q", stored.Title),
			Evidence: map[string]string{
				"idempotency_key": stored.IdempotencyKey,
			},
		})
		return stored, true, nil
	}
	_ = d.wal.Append(WALEvent{
		Type:    "enqueue",
		TaskID:  stored.ID,
		Message: fmt.Sprintf("enqueued %q (estimate=%d)", stored.Title, stored.EstimateBytes),
		Evidence: map[string]string{
			"idempotency_key": stored.IdempotencyKey,
		},
	})
	return stored, false, nil
}

func prepareTaskForEnqueue(t *Task) {
	if t.Runner == "" {
		t.Runner = "hybrid"
	}
	if t.MaxAttempts <= 0 {
		t.MaxAttempts = 3
	}
	if t.IdempotencyKey == "" {
		namespace := "daemon"
		action := t.Title
		if t.Meta != nil && t.Meta["agent_session_id"] != "" {
			namespace = t.Meta["agent_session_id"]
			action = t.Meta["agent_action"]
		}
		t.IdempotencyKey = failure.DeriveIdempotencyKey(namespace, action, t.Prompt, t.Repo, t.Runner, t.Meta)
	}
}

// Resize changes worker pool size at runtime.
func (d *Daemon) Resize(n int) {
	d.pool.Resize(n)
	_ = d.wal.Append(WALEvent{
		Type:    "parallelism_change",
		Message: fmt.Sprintf("worker pool resized to %d", n),
	})
}

// Start begins the worker pool and HTTP server. Returns immediately.
func (d *Daemon) Start(ctx context.Context) error {
	if !d.started.CompareAndSwap(false, true) {
		return errors.New("daemon already started")
	}

	ctx, d.cancel = context.WithCancel(ctx)

	// Resume any tasks left in StateRunning from a prior run.
	checkpoints := d.recoverFromWAL()
	if n, err := d.queue.ResumeRunning(); err == nil && n > 0 {
		_ = d.wal.Append(WALEvent{Type: "resume", Message: fmt.Sprintf("requeued %d in-flight task(s) from prior run", n)})
	}
	if len(checkpoints) > 0 {
		_ = d.queue.ApplyRecoveryCheckpoints(checkpoints)
		_ = d.wal.Append(WALEvent{Type: "recovery", Message: fmt.Sprintf("restored %d WAL checkpoint(s)", len(checkpoints))})
	}

	d.pool = NewWorkerPool(ctx, d.queue, d.wal, d.exec, time.Duration(d.cfg.PollGap)*time.Millisecond, d.onTaskLifecycle)
	d.pool.Resize(d.cfg.MaxParallel)

	d.srv = &http.Server{
		Addr:              d.cfg.Addr,
		Handler:           d.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", d.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	go func() {
		_ = d.srv.Serve(ln)
	}()
	_ = d.wal.Append(WALEvent{Type: "resume", Message: fmt.Sprintf("daemon started on %s with %d workers", d.cfg.Addr, d.cfg.MaxParallel)})
	return nil
}

// Stop gracefully stops the worker pool and HTTP server.
func (d *Daemon) Stop() {
	if !d.started.Load() {
		return
	}
	if d.pool != nil {
		d.pool.StopAll()
	}
	if d.srv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.srv.Shutdown(shutdownCtx)
	}
	if d.cancel != nil {
		d.cancel()
	}
	_ = d.wal.Append(WALEvent{Type: "pause", Message: "daemon stopped"})
	_ = d.wal.Close()
}

// ----- HTTP routes -----

func (d *Daemon) registerRoutes() {
	d.mux.HandleFunc("/health", d.handleHealth)
	d.mux.HandleFunc("/enqueue", d.auth(d.handleEnqueue))
	d.mux.HandleFunc("/status", d.auth(d.handleStatus))
	d.mux.HandleFunc("/rules", d.auth(d.handleRules))
	d.mux.HandleFunc("/rules/", d.auth(d.handleRuleByID))
	d.mux.HandleFunc("/workers", d.auth(d.handleWorkers))
	d.mux.HandleFunc("/pause", d.auth(d.handlePause))
	d.mux.HandleFunc("/resume", d.auth(d.handleResume))
	d.mux.HandleFunc("/tasks", d.auth(d.handleTasksList))
	d.mux.HandleFunc("/tasks/get", d.auth(d.handleTaskGet))
	d.mux.HandleFunc("/tasks/cancel", d.auth(d.handleTaskCancel))
	d.mux.HandleFunc("/wal", d.auth(d.handleWAL))
	d.mux.HandleFunc("/hooks/install", d.auth(d.handleHookInstall))
	d.registerAgentRoutes()
}

// Handler exposes the daemon's mux for tests / embedding.
func (d *Daemon) Handler() http.Handler { return d.mux }

func (d *Daemon) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.cfg.Token != "" {
			got := r.Header.Get("Authorization")
			if got != "Bearer "+d.cfg.Token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		h(w, r)
	}
}

func (d *Daemon) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"status": "ok", "started": d.started.Load()})
}

func (d *Daemon) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var t Task
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if t.Title == "" || t.Prompt == "" {
		http.Error(w, "title and prompt required", http.StatusBadRequest)
		return
	}
	stored, deduplicated, err := d.EnqueueTask(&t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if deduplicated {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
	writeJSON(w, stored)
}

func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"workers":      d.pool.Size(),
		"active":       d.pool.ActiveCount(),
		"queue_counts": d.queue.Counts(),
		"addr":         d.cfg.Addr,
	})
}

func (d *Daemon) handleRules(w http.ResponseWriter, r *http.Request) {
	if d.Rules == nil {
		http.Error(w, "rules registry unavailable", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		list, err := d.Rules.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, list)
	case http.MethodPost:
		var body struct {
			Text       string `json:"text"`
			Scope      string `json:"scope"`
			ToolFilter string `json:"tool_filter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		rule, err := d.Rules.AddWithOptions(r.Context(), rules.AddRequest{
			Text:       body.Text,
			Scope:      body.Scope,
			ToolFilter: body.ToolFilter,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		writeJSON(w, rule)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *Daemon) handleRuleByID(w http.ResponseWriter, r *http.Request) {
	if d.Rules == nil {
		http.Error(w, "rules registry unavailable", http.StatusServiceUnavailable)
		return
	}

	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/rules/"), "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) > 2 {
		http.NotFound(w, r)
		return
	}

	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch {
	case r.Method == http.MethodGet && action == "":
		rule, err := d.Rules.Get(id)
		if err != nil {
			writeRuleError(w, err)
			return
		}
		writeJSON(w, rule)
	case r.Method == http.MethodDelete && action == "":
		if err := d.Rules.Delete(id); err != nil {
			writeRuleError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && action == "pause":
		if err := d.Rules.Pause(id); err != nil {
			writeRuleError(w, err)
			return
		}
		writeJSON(w, map[string]string{"id": id, "status": rules.StatusPaused})
	case r.Method == http.MethodPost && action == "resume":
		if err := d.Rules.Resume(id); err != nil {
			writeRuleError(w, err)
			return
		}
		writeJSON(w, map[string]string{"id": id, "status": rules.StatusActive})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *Daemon) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Count < 0 || body.Count > 200 {
		http.Error(w, "count out of range [0,200]", http.StatusBadRequest)
		return
	}
	d.Resize(body.Count)
	writeJSON(w, map[string]any{"workers": d.pool.Size()})
}

func (d *Daemon) handlePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	d.pausedSize = d.pool.Size()
	d.mu.Unlock()
	d.Resize(0)
	writeJSON(w, map[string]any{"paused": true, "remembered_size": d.pausedSize})
}

func (d *Daemon) handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	target := d.pausedSize
	d.pausedSize = 0
	d.mu.Unlock()
	if target == 0 {
		target = d.cfg.MaxParallel
	}
	d.Resize(target)
	writeJSON(w, map[string]any{"resumed": true, "workers": d.pool.Size()})
}

func (d *Daemon) handleTasksList(w http.ResponseWriter, r *http.Request) {
	state := TaskState(r.URL.Query().Get("state"))
	tasks := d.queue.List(state)
	writeJSON(w, map[string]any{"tasks": tasks, "count": len(tasks)})
}

func (d *Daemon) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	t := d.queue.Get(id)
	if t == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, t)
}

func (d *Daemon) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := d.queue.Cancel(body.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if task := d.queue.Get(body.ID); task != nil && task.Meta != nil {
		d.onTaskLifecycle(TaskLifecycleEvent{
			TS:        time.Now().UTC(),
			Type:      "task.cancelled",
			TaskID:    body.ID,
			SessionID: task.Meta["agent_session_id"],
			Message:   "task cancelled",
			State:     StateCancelled,
		})
	}
	writeJSON(w, map[string]string{"cancelled": body.ID})
}

func (d *Daemon) handleWAL(w http.ResponseWriter, r *http.Request) {
	n := 100
	if v := r.URL.Query().Get("n"); v != "" {
		fmt.Sscanf(v, "%d", &n)
	}
	events, err := d.wal.Tail(n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"events": events, "count": len(events)})
}

func (d *Daemon) handleHookInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var h customHook
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if h.Event == "" || h.Command == "" {
		http.Error(w, "event and command required", http.StatusBadRequest)
		return
	}
	switch h.Event {
	case "intent", "done", "blocked", "enqueue", "complete", "fail":
	default:
		http.Error(w, "unsupported event "+h.Event, http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(h.Command, ";&|`$") {
		http.Error(w, "command contains forbidden shell metachars", http.StatusBadRequest)
		return
	}
	d.hooksMu.Lock()
	d.customHooks = append(d.customHooks, h)
	d.hooksMu.Unlock()
	_ = d.wal.Append(WALEvent{Type: "hook_install", Message: fmt.Sprintf("hook on %s: %s", h.Event, h.Command)})
	writeJSON(w, map[string]any{"installed": h, "total_hooks": len(d.customHooks)})
}

// Hooks returns the currently installed runtime hooks.
func (d *Daemon) Hooks() []customHook {
	d.hooksMu.RLock()
	defer d.hooksMu.RUnlock()
	out := make([]customHook, len(d.customHooks))
	copy(out, d.customHooks)
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeRuleError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

func (d *Daemon) onTaskLifecycle(ev TaskLifecycleEvent) {
	if d.agentSessions == nil {
		return
	}
	d.agentSessions.RecordTaskLifecycle(ev)
}

func (d *Daemon) recoverFromWAL() map[string]string {
	if d.wal == nil {
		return nil
	}
	events, err := d.wal.ReadAll()
	if err != nil {
		return nil
	}
	recoveryEvents := make([]failure.RecoveryEvent, 0, len(events))
	for _, ev := range events {
		recoveryEvents = append(recoveryEvents, failure.RecoveryEvent{
			TS:       ev.TS,
			Type:     ev.Type,
			TaskID:   ev.TaskID,
			WorkerID: ev.WorkerID,
			Message:  ev.Message,
			Evidence: ev.Evidence,
		})
	}
	checkpoints := failure.DetectPartialState(recoveryEvents)
	out := make(map[string]string, len(checkpoints))
	for id, cp := range checkpoints {
		if cp.ResumeFrom != "" {
			out[id] = cp.ResumeFrom
		}
	}
	return out
}
