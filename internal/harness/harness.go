// Package harness is the runtime layer that creates worker stances when the
// supervisor's hooks call for spawning. It handles model selection, system
// prompt construction, session initialization, pause/resume, tool
// authorization, and stance lifecycle.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/concern"
	"github.com/ericmacdougall/stoke/internal/harness/prompts"
	htools "github.com/ericmacdougall/stoke/internal/harness/tools"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

// Config holds mission-level harness configuration.
type Config struct {
	MissionID      string
	DefaultModel   string            // e.g. "claude-opus-4-6"
	ModelOverrides map[string]string // role -> model
	OperatingMode  string            // "interactive" or "full_auto"
	BudgetUSD      float64
}

// Harness manages the lifecycle of stance workers for a mission.
type Harness struct {
	config  Config
	ledger  *ledger.Ledger
	bus     *bus.Bus
	concern *concern.Builder
	stances map[string]*StanceSession
	mu      sync.RWMutex
	seq     uint64 // monotonic stance counter
}

// New creates a Harness wired to the given ledger, bus, and concern builder.
func New(cfg Config, l *ledger.Ledger, b *bus.Bus, cb *concern.Builder) *Harness {
	return &Harness{
		config:  cfg,
		ledger:  l,
		bus:     b,
		concern: cb,
		stances: make(map[string]*StanceSession),
	}
}

// SpawnStance creates and initializes a new worker stance.
func (h *Harness) SpawnStance(ctx context.Context, req SpawnRequest) (*StanceHandle, error) {
	// 1. Validate request.
	if req.Role == "" {
		return nil, fmt.Errorf("harness: spawn: role is required")
	}
	if !prompts.KnownRole(req.Role) {
		return nil, fmt.Errorf("harness: spawn: unknown role %q", req.Role)
	}
	face := req.Face
	if face == "" {
		face = "proposing"
	}
	if face != "proposing" && face != "reviewing" {
		return nil, fmt.Errorf("harness: spawn: invalid face %q", face)
	}

	// 2. Build concern field.
	scope := concern.Scope{
		MissionID: h.config.MissionID,
		TaskID:    req.TaskDAGScope,
		LoopID:    req.LoopRef,
	}
	cf, err := h.concern.BuildConcernField(ctx, concern.StanceRole(req.Role), concern.Face(face), scope)
	if err != nil {
		return nil, fmt.Errorf("harness: spawn: build concern field: %w", err)
	}
	renderedCF := concern.Render(cf)

	// 3. Construct system prompt.
	tmpl, err := prompts.Template(req.Role)
	if err != nil {
		return nil, fmt.Errorf("harness: spawn: %w", err)
	}

	// Build tools list string.
	authorizedTools := h.resolveTools(req)
	toolsList := strings.Join(authorizedTools, ", ")

	systemPrompt := strings.ReplaceAll(tmpl, "{{TOOLS}}", toolsList)
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{CONCERN_FIELD}}", renderedCF)
	if req.AdditionalCtx != "" {
		systemPrompt += "\n\n" + req.AdditionalCtx
	}

	// 4. Select model.
	model := h.resolveModel(req)

	// 5. Create StanceSession.
	h.mu.Lock()
	h.seq++
	stanceID := fmt.Sprintf("stance-%s-%d", req.Role, h.seq)
	sess := &StanceSession{
		ID:              stanceID,
		Role:            req.Role,
		Status:          StatusRunning,
		Model:           model,
		SystemPrompt:    systemPrompt,
		ConcernField:    renderedCF,
		AuthorizedTools: authorizedTools,
		SpawnRequest:    req,
		CreatedAt:       time.Now().UTC(),
		AdditionalCtx:   req.AdditionalCtx,
		pauseCh:         make(chan struct{}),
		resumeCh:        make(chan struct{}),
		pauseAckCh:      make(chan struct{}),
	}
	h.stances[stanceID] = sess
	h.mu.Unlock()

	// 6. Emit stance.spawned event on bus.
	payload, _ := json.Marshal(map[string]string{
		"role":  req.Role,
		"face":  face,
		"model": model,
	})
	if err := h.bus.Publish(bus.Event{
		Type:      bus.EvtWorkerSpawned,
		EmitterID: stanceID,
		Scope: bus.Scope{
			MissionID: h.config.MissionID,
			TaskID:    req.TaskDAGScope,
			StanceID:  stanceID,
			LoopID:    req.LoopRef,
		},
		Payload:   payload,
		CausalRef: req.CausalityRef,
	}); err != nil {
		return nil, fmt.Errorf("harness: spawn: publish event: %w", err)
	}

	// 7. Return handle.
	return &StanceHandle{
		ID:    stanceID,
		Role:  req.Role,
		State: StatusRunning,
	}, nil
}

// PauseStance pauses a running stance. It signals the stance to halt via the
// pause channel and waits for the stance to acknowledge at a safe checkpoint
// before publishing the paused event.
func (h *Harness) PauseStance(ctx context.Context, stanceID string, reason string) error {
	h.mu.Lock()

	sess, ok := h.stances[stanceID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("harness: pause: stance %q not found", stanceID)
	}
	if sess.Status != StatusRunning {
		h.mu.Unlock()
		return fmt.Errorf("harness: pause: stance %q is %s, not running", stanceID, sess.Status)
	}

	// Signal the stance to pause via the cooperative channel.
	close(sess.pauseCh)
	sess.Status = StatusPaused
	sess.PauseReason = reason

	// Release the lock before blocking on acknowledgment.
	h.mu.Unlock()

	// Wait for the stance to reach a safe checkpoint and acknowledge.
	select {
	case <-sess.pauseAckCh:
		// Stance has stopped at a safe checkpoint.
	case <-time.After(30 * time.Second):
		return fmt.Errorf("harness: pause: stance %q did not acknowledge within 30s", stanceID)
	case <-ctx.Done():
		return ctx.Err()
	}

	// Publish the paused event only after the stance has actually stopped.
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	if err := h.bus.Publish(bus.Event{
		Type:      bus.EvtWorkerPaused,
		EmitterID: stanceID,
		Scope: bus.Scope{
			MissionID: h.config.MissionID,
			StanceID:  stanceID,
		},
		Payload: payload,
	}); err != nil {
		return fmt.Errorf("harness: pause: publish event: %w", err)
	}

	return nil
}

// ResumeStance resumes a paused stance with optional additional context.
// It closes the resume channel to unblock the stance's CheckpointCheck.
func (h *Harness) ResumeStance(ctx context.Context, stanceID string, additional string) error {
	h.mu.Lock()

	sess, ok := h.stances[stanceID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("harness: resume: stance %q not found", stanceID)
	}
	if sess.Status != StatusPaused && sess.Status != StatusWaitingResearch && sess.Status != StatusWaitingConsensus {
		h.mu.Unlock()
		return fmt.Errorf("harness: resume: stance %q is %s, not paused", stanceID, sess.Status)
	}

	sess.Status = StatusRunning
	sess.PauseReason = ""
	if additional != "" {
		sess.AdditionalCtx = additional
	}

	// Unblock the stance runner waiting in CheckpointCheck.
	close(sess.resumeCh)
	// Allocate a fresh resume channel for the next pause cycle.
	sess.resumeCh = make(chan struct{})

	h.mu.Unlock()

	if err := h.bus.Publish(bus.Event{
		Type:      bus.EvtWorkerResumed,
		EmitterID: stanceID,
		Scope: bus.Scope{
			MissionID: h.config.MissionID,
			StanceID:  stanceID,
		},
	}); err != nil {
		return fmt.Errorf("harness: resume: publish event: %w", err)
	}

	return nil
}

// TerminateStance ends a stance permanently.
func (h *Harness) TerminateStance(ctx context.Context, stanceID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	sess, ok := h.stances[stanceID]
	if !ok {
		return fmt.Errorf("harness: terminate: stance %q not found", stanceID)
	}
	if sess.Status == StatusTerminated {
		return nil // idempotent
	}

	sess.Status = StatusTerminated

	if err := h.bus.Publish(bus.Event{
		Type:      bus.EvtWorkerTerminated,
		EmitterID: stanceID,
		Scope: bus.Scope{
			MissionID: h.config.MissionID,
			StanceID:  stanceID,
		},
	}); err != nil {
		return fmt.Errorf("harness: terminate: publish event: %w", err)
	}

	return nil
}

// InspectStance returns current state of a stance.
func (h *Harness) InspectStance(_ context.Context, stanceID string) (*StanceState, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sess, ok := h.stances[stanceID]
	if !ok {
		return nil, fmt.Errorf("harness: inspect: stance %q not found", stanceID)
	}

	return &StanceState{
		StanceHandle: StanceHandle{
			ID:    sess.ID,
			Role:  sess.Role,
			State: sess.Status,
		},
		Model:       sess.Model,
		TokensUsed:  sess.TokensUsed,
		CostUSD:     sess.CostUSD,
		CreatedAt:   sess.CreatedAt,
		PauseReason: sess.PauseReason,
	}, nil
}

// ListStances returns all tracked stances.
func (h *Harness) ListStances(_ context.Context) []StanceHandle {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]StanceHandle, 0, len(h.stances))
	for _, sess := range h.stances {
		out = append(out, StanceHandle{
			ID:    sess.ID,
			Role:  sess.Role,
			State: sess.Status,
		})
	}
	return out
}

// Recover rebuilds harness state from the ledger and bus after a crash.
// It replays worker lifecycle events to reconstruct the stances map.
func (h *Harness) Recover(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Clear existing state.
	h.stances = make(map[string]*StanceSession)

	// Replay all worker events to rebuild state.
	pattern := bus.Pattern{TypePrefix: "worker."}
	return h.bus.Replay(pattern, 0, func(evt bus.Event) {
		stanceID := evt.EmitterID
		switch evt.Type {
		case bus.EvtWorkerSpawned:
			var payload struct {
				Role  string `json:"role"`
				Model string `json:"model"`
			}
			_ = json.Unmarshal(evt.Payload, &payload)
			h.stances[stanceID] = &StanceSession{
				ID:         stanceID,
				Role:       payload.Role,
				Status:     StatusRunning,
				Model:      payload.Model,
				CreatedAt:  evt.Timestamp,
				pauseCh:    make(chan struct{}),
				resumeCh:   make(chan struct{}),
				pauseAckCh: make(chan struct{}),
			}
		case bus.EvtWorkerPaused:
			if sess, ok := h.stances[stanceID]; ok {
				sess.Status = StatusPaused
				var payload struct {
					Reason string `json:"reason"`
				}
				_ = json.Unmarshal(evt.Payload, &payload)
				sess.PauseReason = payload.Reason
			}
		case bus.EvtWorkerResumed:
			if sess, ok := h.stances[stanceID]; ok {
				sess.Status = StatusRunning
				sess.PauseReason = ""
			}
		case bus.EvtWorkerTerminated:
			if sess, ok := h.stances[stanceID]; ok {
				sess.Status = StatusTerminated
			}
		}
	})
}

// StanceCheckpointer returns the CheckpointCheck function for a stance.
// This is called by the stance runner at safe points to cooperate with pause.
func (h *Harness) StanceCheckpointer(stanceID string) (func(context.Context) error, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sess, ok := h.stances[stanceID]
	if !ok {
		return nil, fmt.Errorf("harness: checkpointer: stance %q not found", stanceID)
	}
	return sess.CheckpointCheck, nil
}

// resolveModel picks the model for a spawn request.
func (h *Harness) resolveModel(req SpawnRequest) string {
	if req.ModelOverride != "" {
		return req.ModelOverride
	}
	if m, ok := h.config.ModelOverrides[req.Role]; ok {
		return m
	}
	return h.config.DefaultModel
}

// resolveTools builds the authorized tools list for a spawn request.
func (h *Harness) resolveTools(req SpawnRequest) []string {
	if len(req.ToolAuthOverride) > 0 {
		return req.ToolAuthOverride
	}
	defaults := htools.DefaultToolsForRole(req.Role)
	out := make([]string, len(defaults))
	for i, t := range defaults {
		out[i] = string(t)
	}
	return out
}
