package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/chat"
	"github.com/RelayOne/r1/internal/failure"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/google/uuid"
)

const (
	defaultAgentChatModel = "claude-sonnet-4-6"
	maxSessionEvents      = 512
)

// AgentTurn is one conversational exchange inside an agent session.
type AgentTurn struct {
	TS              time.Time `json:"ts"`
	MessageType     string    `json:"message_type"`
	Message         string    `json:"message"`
	Reply           string    `json:"reply"`
	TaskIDsAffected []string  `json:"task_ids_affected,omitempty"`
}

// AgentEvent is the session-scoped event frame replayed by /agent/events.
type AgentEvent struct {
	TS           time.Time         `json:"ts"`
	Sequence     int64             `json:"sequence"`
	SessionID    string            `json:"session_id"`
	Type         string            `json:"type"`
	TaskID       string            `json:"task_id,omitempty"`
	CurrentState string            `json:"current_state,omitempty"`
	Message      string            `json:"message,omitempty"`
	Data         map[string]string `json:"data,omitempty"`
}

// AgentSession is the persisted state for one agent-interaction-mode session.
type AgentSession struct {
	ID            string                 `json:"id"`
	AgentID       string                 `json:"agent_id"`
	Token         string                 `json:"token"`
	Model         string                 `json:"model"`
	Capabilities  []string               `json:"capabilities,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
	CurrentState  string                 `json:"current_state"`
	TaskIDs       []string               `json:"task_ids,omitempty"`
	ActiveTaskIDs []string               `json:"active_task_ids,omitempty"`
	Turns         []AgentTurn            `json:"turns,omitempty"`
	History       []provider.ChatMessage `json:"history,omitempty"`
	Events        []AgentEvent           `json:"events,omitempty"`
	NextEventSeq  int64                  `json:"next_event_seq"`
	NextTaskSeq   int                    `json:"next_task_seq"`

	runtime *agentSessionRuntime `json:"-"`
}

type agentSessionRuntime struct {
	mu   sync.Mutex
	chat *chat.Session
}

type agentSessionFile struct {
	Sessions []*AgentSession `json:"sessions"`
}

// TaskLifecycleEvent is the worker-to-session bridge payload for session-owned
// queue tasks.
type TaskLifecycleEvent struct {
	TS          time.Time
	Type        string
	TaskID      string
	SessionID   string
	WorkerID    string
	Message     string
	State       TaskState
	ProofsPath  string
	ActualBytes int64
	Error       string
}

// AgentSessionStore persists agent sessions and rebuilds live chat state on
// demand from the saved provider history.
type AgentSessionStore struct {
	path     string
	daemon   *Daemon
	provider provider.Provider
	model    string

	mu       sync.Mutex
	sessions map[string]*AgentSession
	subs     map[string]map[chan AgentEvent]struct{}
}

func NewAgentSessionStore(path string, daemon *Daemon, prov provider.Provider, model string) (*AgentSessionStore, error) {
	if path == "" {
		return nil, errors.New("agent session path required")
	}
	if model == "" {
		model = defaultAgentChatModel
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir agent session dir: %w", err)
	}
	s := &AgentSessionStore{
		path:     path,
		daemon:   daemon,
		provider: prov,
		model:    model,
		sessions: map[string]*AgentSession{},
		subs:     map[string]map[chan AgentEvent]struct{}{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *AgentSessionStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.persistLocked()
		}
		return fmt.Errorf("read agent sessions: %w", err)
	}
	if len(data) == 0 {
		return nil
	}

	var file agentSessionFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse agent sessions: %w", err)
	}
	for _, sess := range file.Sessions {
		if sess == nil || sess.ID == "" {
			continue
		}
		s.ensureRuntimeLocked(sess)
		s.sessions[sess.ID] = sess
	}
	return nil
}

func (s *AgentSessionStore) persistLocked() error {
	sessions := make([]*AgentSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, cloneSessionForPersist(sess))
	}
	data, err := json.MarshalIndent(agentSessionFile{Sessions: sessions}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agent sessions: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write agent sessions tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename agent sessions: %w", err)
	}
	return nil
}

func (s *AgentSessionStore) ensureRuntimeLocked(sess *AgentSession) {
	if sess.runtime == nil {
		sess.runtime = &agentSessionRuntime{}
	}
	if sess.Model == "" {
		sess.Model = s.model
	}
	if sess.CurrentState == "" {
		sess.CurrentState = "ready"
	}
	if sess.NextTaskSeq <= 0 && len(sess.TaskIDs) > 0 {
		sess.NextTaskSeq = failure.NextStableSequence(sess.ID, sess.TaskIDs) - 1
	}
}

func (s *AgentSessionStore) sessionByID(id string) (*AgentSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil {
		return nil, fmt.Errorf("session %q not found", id)
	}
	s.ensureRuntimeLocked(sess)
	return sess, nil
}

func (s *AgentSessionStore) Create(agentID string, capabilities []string) (*AgentSession, error) {
	now := time.Now().UTC()
	sess := &AgentSession{
		ID:           "agent-" + uuid.NewString(),
		AgentID:      strings.TrimSpace(agentID),
		Token:        "bearer-" + uuid.NewString(),
		Model:        s.model,
		Capabilities: normalizeCapabilities(capabilities),
		CreatedAt:    now,
		UpdatedAt:    now,
		CurrentState: "ready",
		runtime:      &agentSessionRuntime{},
	}
	if sess.AgentID == "" {
		sess.AgentID = "agent"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	if err := s.publishEventLocked(sess, AgentEvent{
		Type:         "session.created",
		Message:      fmt.Sprintf("session created for %s", sess.AgentID),
		CurrentState: sess.CurrentState,
	}); err != nil {
		return nil, err
	}
	return cloneSessionForPersist(sess), nil
}

func (s *AgentSessionStore) Authorize(sessionID, authHeader, daemonToken string) (*AgentSession, error) {
	sess, err := s.sessionByID(sessionID)
	if err != nil {
		return nil, err
	}
	got := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	switch {
	case daemonToken != "" && got == daemonToken:
		return sess, nil
	case got == "" || got != sess.Token:
		return nil, errors.New("unauthorized")
	default:
		return sess, nil
	}
}

func (s *AgentSessionStore) Chat(ctx context.Context, sessionID, message, messageType string) (string, string, []string, error) {
	sess, err := s.sessionByID(sessionID)
	if err != nil {
		return "", "", nil, err
	}
	sess.runtime.mu.Lock()
	defer sess.runtime.mu.Unlock()

	if err := requireCapability(sess, capabilityForMessageType(messageType)); err != nil {
		return "", sess.CurrentState, nil, err
	}

	var reply string
	var taskIDs []string
	switch messageType {
	case "task", "query":
		reply, taskIDs, err = s.chatOrFallback(ctx, sess, message, messageType)
	case "redirect":
		reply, taskIDs, err = s.redirectLocked(sess, message)
	case "abort":
		reply, taskIDs, err = s.abortLocked(sess, message)
	default:
		err = fmt.Errorf("unsupported message_type %q", messageType)
	}
	if err != nil {
		return "", sess.CurrentState, nil, err
	}

	sess.UpdatedAt = time.Now().UTC()
	sess.Turns = append(sess.Turns, AgentTurn{
		TS:              sess.UpdatedAt,
		MessageType:     messageType,
		Message:         message,
		Reply:           reply,
		TaskIDsAffected: append([]string(nil), taskIDs...),
	})
	if err := s.persistSessionLocked(sess); err != nil {
		return "", sess.CurrentState, nil, err
	}
	return reply, sess.CurrentState, taskIDs, nil
}

func (s *AgentSessionStore) FollowUp(sessionID, parentTaskID, newContext string) (string, string, error) {
	sess, err := s.sessionByID(sessionID)
	if err != nil {
		return "", "", err
	}
	sess.runtime.mu.Lock()
	defer sess.runtime.mu.Unlock()

	if err := requireCapability(sess, "enqueue"); err != nil {
		return "", "", err
	}
	if !containsString(sess.TaskIDs, parentTaskID) {
		return "", "", fmt.Errorf("task %q does not belong to session %s", parentTaskID, sessionID)
	}
	parent := s.daemon.queue.Get(parentTaskID)
	if parent == nil {
		return "", "", fmt.Errorf("task %q not found", parentTaskID)
	}

	prompt := strings.TrimSpace(parent.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(parent.Title)
	}
	prompt = strings.TrimSpace(prompt + "\n\nFollow-up context:\n" + strings.TrimSpace(newContext))

	taskID, err := s.enqueueSessionTaskLocked(sess, "follow_up", prompt, map[string]string{
		"parent_task_id": parentTaskID,
	})
	if err != nil {
		return "", "", err
	}
	sess.UpdatedAt = time.Now().UTC()
	sess.Turns = append(sess.Turns, AgentTurn{
		TS:              sess.UpdatedAt,
		MessageType:     "follow_up",
		Message:         newContext,
		Reply:           fmt.Sprintf("Queued follow-up task %s for parent %s.", taskID, parentTaskID),
		TaskIDsAffected: []string{taskID},
	})
	if err := s.persistSessionLocked(sess); err != nil {
		return "", "", err
	}
	return taskID, "state", nil
}

func (s *AgentSessionStore) EventsSince(sessionID string, since time.Time) ([]AgentEvent, error) {
	sess, err := s.sessionByID(sessionID)
	if err != nil {
		return nil, err
	}
	sess.runtime.mu.Lock()
	defer sess.runtime.mu.Unlock()
	events := make([]AgentEvent, 0, len(sess.Events))
	for _, ev := range sess.Events {
		if since.IsZero() || !ev.TS.Before(since) {
			events = append(events, ev)
		}
	}
	return events, nil
}

func (s *AgentSessionStore) Subscribe(sessionID string) (chan AgentEvent, func(), error) {
	if _, err := s.sessionByID(sessionID); err != nil {
		return nil, nil, err
	}
	ch := make(chan AgentEvent, 32)
	s.mu.Lock()
	if s.subs[sessionID] == nil {
		s.subs[sessionID] = map[chan AgentEvent]struct{}{}
	}
	s.subs[sessionID][ch] = struct{}{}
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		subs := s.subs[sessionID]
		if subs != nil {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(s.subs, sessionID)
			}
		}
		s.mu.Unlock()
	}
	return ch, cancel, nil
}

func (s *AgentSessionStore) RecordTaskLifecycle(ev TaskLifecycleEvent) {
	if ev.SessionID == "" {
		return
	}
	sess, err := s.sessionByID(ev.SessionID)
	if err != nil {
		return
	}
	sess.runtime.mu.Lock()
	defer sess.runtime.mu.Unlock()

	switch ev.Type {
	case "task.started":
		sess.CurrentState = "running"
	case "task.completed", "task.failed", "task.cancelled":
		sess.ActiveTaskIDs = removeString(sess.ActiveTaskIDs, ev.TaskID)
		refreshCurrentState(sess)
	}

	data := map[string]string{}
	if ev.WorkerID != "" {
		data["worker_id"] = ev.WorkerID
	}
	if ev.ProofsPath != "" {
		data["proofs_path"] = ev.ProofsPath
	}
	if ev.ActualBytes > 0 {
		data["actual_bytes"] = fmt.Sprintf("%d", ev.ActualBytes)
	}
	if ev.Error != "" {
		data["error"] = ev.Error
	}

	_ = s.persistLifecycleLocked(sess, AgentEvent{
		TS:           ev.TS,
		Type:         ev.Type,
		TaskID:       ev.TaskID,
		CurrentState: sess.CurrentState,
		Message:      ev.Message,
		Data:         data,
	})
}

func (s *AgentSessionStore) chatOrFallback(ctx context.Context, sess *AgentSession, message, messageType string) (string, []string, error) {
	prov, err := s.resolveProvider()
	if err == nil && prov != nil {
		return s.chatWithProviderLocked(ctx, sess, prov, message)
	}
	return s.chatFallbackLocked(sess, message, messageType)
}

func (s *AgentSessionStore) resolveProvider() (provider.Provider, error) {
	if s.provider != nil {
		return s.provider, nil
	}
	return chat.NewProviderFromOptions(chat.ProviderOptions{Model: s.model})
}

func (s *AgentSessionStore) chatWithProviderLocked(ctx context.Context, sess *AgentSession, prov provider.Provider, message string) (string, []string, error) {
	if sess.runtime.chat == nil {
		restored, err := chat.NewSessionFromHistory(prov, chat.Config{
			Model:     sess.Model,
			MaxTokens: 6000,
			Tools:     chat.DispatcherTools(),
		}, sess.History)
		if err != nil {
			return "", nil, err
		}
		sess.runtime.chat = restored
	}

	dispatcher := &daemonChatDispatcher{store: s, session: sess}
	onDispatch := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return chat.RunToolCall(dispatcher, name, input)
	}
	result, err := sess.runtime.chat.Send(ctx, message, nil, onDispatch)
	if err != nil {
		return "", nil, err
	}
	sess.History = sess.runtime.chat.History()
	sess.UpdatedAt = time.Now().UTC()

	reply := strings.TrimSpace(result.Text)
	if reply == "" && len(dispatcher.taskIDs) > 0 {
		reply = fmt.Sprintf("Queued %d task(s).", len(dispatcher.taskIDs))
	}
	if reply == "" {
		reply = "No reply generated."
	}
	if len(dispatcher.taskIDs) == 0 {
		refreshCurrentState(sess)
	}
	return reply, append([]string(nil), dispatcher.taskIDs...), nil
}

func (s *AgentSessionStore) chatFallbackLocked(sess *AgentSession, message, messageType string) (string, []string, error) {
	switch messageType {
	case "query":
		refreshCurrentState(sess)
		reply := fmt.Sprintf("Session %s is %s. Active tasks: %d. Total tasks: %d.",
			sess.ID, sess.CurrentState, len(sess.ActiveTaskIDs), len(sess.TaskIDs))
		return reply, nil, nil
	case "task":
		taskID, err := s.enqueueSessionTaskLocked(sess, "task", message, nil)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("Queued task %s.", taskID), []string{taskID}, nil
	default:
		return "", nil, fmt.Errorf("fallback unsupported for %s", messageType)
	}
}

func (s *AgentSessionStore) redirectLocked(sess *AgentSession, message string) (string, []string, error) {
	meta := map[string]string{}
	if len(sess.ActiveTaskIDs) > 0 {
		meta["redirected_from"] = strings.Join(sess.ActiveTaskIDs, ",")
	}
	taskID, err := s.enqueueSessionTaskLocked(sess, "redirect", message, meta)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Queued redirect task %s.", taskID), []string{taskID}, nil
}

func (s *AgentSessionStore) abortLocked(sess *AgentSession, message string) (string, []string, error) {
	cancelled := make([]string, 0, len(sess.TaskIDs))
	for _, taskID := range append([]string(nil), sess.TaskIDs...) {
		task := s.daemon.queue.Get(taskID)
		if task == nil {
			continue
		}
		if task.State == StateDone || task.State == StateFailed || task.State == StateCancelled {
			continue
		}
		if err := s.daemon.queue.Cancel(taskID); err != nil {
			continue
		}
		cancelled = append(cancelled, taskID)
		sess.ActiveTaskIDs = removeString(sess.ActiveTaskIDs, taskID)
		_ = s.persistLifecycleLocked(sess, AgentEvent{
			Type:         "task.cancelled",
			TaskID:       taskID,
			CurrentState: "aborted",
			Message:      strings.TrimSpace(message),
		})
	}
	sess.CurrentState = "aborted"
	if len(cancelled) == 0 {
		return "No active tasks to abort.", nil, nil
	}
	return fmt.Sprintf("Cancelled %d task(s).", len(cancelled)), cancelled, nil
}

func (s *AgentSessionStore) enqueueSessionTaskLocked(sess *AgentSession, action, prompt string, meta map[string]string) (string, error) {
	nextSeq := sess.NextTaskSeq + 1
	id := failure.StableTaskID(sess.ID, nextSeq)
	title := fmt.Sprintf("%s: %s", strings.ReplaceAll(action, "_", " "), oneLine(prompt, 72))
	if title == "" {
		title = action
	}
	taskMeta := map[string]string{
		"agent_session_id":    sess.ID,
		"agent_action":        action,
		"agent_id":            sess.AgentID,
		"agent_task_sequence": fmt.Sprintf("%d", nextSeq),
	}
	for k, v := range meta {
		if strings.TrimSpace(k) != "" && v != "" {
			taskMeta[k] = v
		}
	}
	task := &Task{
		ID:             id,
		IdempotencyKey: failure.DeriveIdempotencyKey(sess.ID, action, prompt, "", "hybrid", taskMeta),
		Title:          title,
		Prompt:         strings.TrimSpace(prompt),
		Runner:         "hybrid",
		Priority:       50,
		State:          StateQueued,
		Meta:           taskMeta,
		Tags:           []string{"agent-session", action},
	}
	if task.Prompt == "" {
		task.Prompt = title
	}
	stored, deduplicated, err := s.daemon.EnqueueTask(task)
	if err != nil {
		return "", err
	}
	id = stored.ID
	if !deduplicated {
		sess.NextTaskSeq = nextSeq
	}
	if !containsString(sess.TaskIDs, id) {
		sess.TaskIDs = append(sess.TaskIDs, id)
	}
	if stored.State != StateDone && stored.State != StateFailed && stored.State != StateCancelled && !containsString(sess.ActiveTaskIDs, id) {
		sess.ActiveTaskIDs = append(sess.ActiveTaskIDs, id)
	}
	sess.CurrentState = "running"
	eventType := "task.enqueued"
	if deduplicated {
		eventType = "task.deduplicated"
	}
	if err := s.publishEventLocked(sess, AgentEvent{
		Type:         eventType,
		TaskID:       id,
		CurrentState: sess.CurrentState,
		Message:      title,
		Data:         map[string]string{"action": action},
	}); err != nil {
		return "", err
	}
	return id, nil
}

func (s *AgentSessionStore) persistSessionLocked(sess *AgentSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	return s.persistLocked()
}

func (s *AgentSessionStore) persistLifecycleLocked(sess *AgentSession, ev AgentEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishEventLocked(sess, ev)
}

func (s *AgentSessionStore) publishEventLocked(sess *AgentSession, ev AgentEvent) error {
	now := time.Now().UTC()
	if ev.TS.IsZero() {
		ev.TS = now
	}
	s.ensureRuntimeLocked(sess)
	sess.UpdatedAt = now
	sess.NextEventSeq++
	ev.Sequence = sess.NextEventSeq
	ev.SessionID = sess.ID
	if ev.CurrentState == "" {
		ev.CurrentState = sess.CurrentState
	}
	sess.Events = append(sess.Events, ev)
	if len(sess.Events) > maxSessionEvents {
		sess.Events = append([]AgentEvent(nil), sess.Events[len(sess.Events)-maxSessionEvents:]...)
	}
	s.sessions[sess.ID] = sess
	if err := s.persistLocked(); err != nil {
		return err
	}

	subs := make([]chan AgentEvent, 0, len(s.subs[sess.ID]))
	for ch := range s.subs[sess.ID] {
		subs = append(subs, ch)
	}
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
	return nil
}

type daemonChatDispatcher struct {
	store   *AgentSessionStore
	session *AgentSession
	taskIDs []string
}

func (d *daemonChatDispatcher) Scope(description string) (string, error) {
	return d.enqueue("scope", description)
}

func (d *daemonChatDispatcher) Build(description string) (string, error) {
	return d.enqueue("build", description)
}

func (d *daemonChatDispatcher) Ship(description string) (string, error) {
	return d.enqueue("ship", description)
}

func (d *daemonChatDispatcher) Plan(description string) (string, error) {
	return d.enqueue("plan", description)
}

func (d *daemonChatDispatcher) Audit() (string, error) {
	return d.enqueue("audit", "Run the multi-persona audit on the current repo.")
}

func (d *daemonChatDispatcher) Scan(securityOnly bool) (string, error) {
	msg := "Run the deterministic scan."
	if securityOnly {
		msg = "Run the deterministic security scan."
	}
	return d.enqueue("scan", msg)
}

func (d *daemonChatDispatcher) Status() (string, error) {
	refreshCurrentState(d.session)
	return fmt.Sprintf("Current state: %s. Active tasks: %d. Total tasks: %d.",
		d.session.CurrentState, len(d.session.ActiveTaskIDs), len(d.session.TaskIDs)), nil
}

func (d *daemonChatDispatcher) SOW(filePath string) (string, error) {
	if _, err := os.Stat(filePath); err != nil {
		return "", fmt.Errorf("sow file not readable: %w", err)
	}
	return d.enqueue("sow", "Execute the statement of work at "+filePath)
}

func (d *daemonChatDispatcher) enqueue(action, prompt string) (string, error) {
	taskID, err := d.store.enqueueSessionTaskLocked(d.session, action, prompt, nil)
	if err != nil {
		return "", err
	}
	d.taskIDs = append(d.taskIDs, taskID)
	return fmt.Sprintf("Queued %s task %s.", action, taskID), nil
}

func normalizeCapabilities(in []string) []string {
	if len(in) == 0 {
		return []string{"enqueue", "query", "redirect"}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, cap := range in {
		cap = strings.ToLower(strings.TrimSpace(cap))
		if cap == "" {
			continue
		}
		if _, ok := seen[cap]; ok {
			continue
		}
		seen[cap] = struct{}{}
		out = append(out, cap)
	}
	return out
}

func capabilityForMessageType(messageType string) string {
	switch messageType {
	case "query":
		return "query"
	case "redirect":
		return "redirect"
	case "task", "follow_up", "abort":
		return "enqueue"
	default:
		return ""
	}
}

func requireCapability(sess *AgentSession, cap string) error {
	if cap == "" {
		return nil
	}
	if len(sess.Capabilities) == 0 {
		return nil
	}
	for _, have := range sess.Capabilities {
		if have == cap {
			return nil
		}
	}
	return fmt.Errorf("session %s lacks capability %q", sess.ID, cap)
}

func refreshCurrentState(sess *AgentSession) {
	if len(sess.ActiveTaskIDs) > 0 {
		sess.CurrentState = "running"
		return
	}
	if sess.CurrentState == "" || sess.CurrentState == "running" {
		sess.CurrentState = "ready"
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func removeString(xs []string, drop string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != drop {
			out = append(out, x)
		}
	}
	return append([]string(nil), out...)
}

func oneLine(in string, max int) string {
	in = strings.TrimSpace(strings.ReplaceAll(in, "\n", " "))
	if len([]rune(in)) <= max {
		return in
	}
	r := []rune(in)
	return string(r[:max]) + "..."
}

func cloneSessionForPersist(in *AgentSession) *AgentSession {
	if in == nil {
		return nil
	}
	out := *in
	out.runtime = nil
	out.Capabilities = append([]string(nil), in.Capabilities...)
	out.TaskIDs = append([]string(nil), in.TaskIDs...)
	out.ActiveTaskIDs = append([]string(nil), in.ActiveTaskIDs...)
	out.Turns = append([]AgentTurn(nil), in.Turns...)
	out.History = append([]provider.ChatMessage(nil), in.History...)
	out.Events = append([]AgentEvent(nil), in.Events...)
	return &out
}
