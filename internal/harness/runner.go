package harness

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/bus"
	htools "github.com/RelayOne/r1/internal/harness/tools"
	"github.com/RelayOne/r1/internal/ledger"
)

// defaultMaxTurns bounds a stance run when RunnerConfig.MaxTurns is unset.
const defaultMaxTurns = 8

// ToolExecutor executes an authorized tool call on behalf of a stance and
// returns the result text fed back to the model. The runner NEVER passes an
// unauthorized call to the executor — htools.IsAuthorized gates every call.
type ToolExecutor interface {
	Execute(ctx context.Context, stanceID string, call ToolCall) (string, error)
}

// RunnerConfig tunes a StanceRunner.
type RunnerConfig struct {
	// MaxTurns caps the number of model turns per Run. <= 0 means
	// defaultMaxTurns.
	MaxTurns int
}

// RunOutcome summarizes a completed stance run.
type RunOutcome struct {
	StanceID        string
	Turns           int
	FinalContent    string // model output of the last turn
	TokensUsed      int64  // tokens consumed by THIS run
	CostUSD         float64
	ToolCallsTotal  int
	ToolCallsDenied int
	HitMaxTurns     bool // loop stopped because MaxTurns was reached
	Terminated      bool // loop stopped because the stance was terminated
}

// StanceRunner drives a stance's execution loop: model turn → tool
// authorization → tool execution → checkpoint → repeat, until the model
// stops calling tools, MaxTurns is reached, the stance is terminated, or
// the context ends. It is the execution engine SpawnStance prepares
// sessions for (audit A041 — previously no runner existed and stances did
// bookkeeping only).
type StanceRunner struct {
	h        *Harness
	provider Provider
	exec     ToolExecutor
	maxTurns int
}

// NewStanceRunner creates a runner for stances spawned on this harness.
// exec may be nil; authorized tool calls then receive an honest
// "no executor wired" result instead of executing.
func (h *Harness) NewStanceRunner(p Provider, exec ToolExecutor, cfg RunnerConfig) *StanceRunner {
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	return &StanceRunner{h: h, provider: p, exec: exec, maxTurns: maxTurns}
}

// Run drives the stance loop for stanceID. task seeds the first user
// message; when empty, the stance is pointed at its concern field. Between
// turns the runner calls sess.CheckpointCheck, which is where PauseStance
// acknowledgments land — a paused stance blocks inside the checkpoint until
// ResumeStance or context cancellation.
func (r *StanceRunner) Run(ctx context.Context, stanceID string, task string) (*RunOutcome, error) {
	r.h.mu.RLock()
	sess, ok := r.h.stances[stanceID]
	r.h.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("harness: run: stance %q not found", stanceID)
	}
	if status := r.stanceStatus(sess); status != StatusRunning {
		return nil, fmt.Errorf("harness: run: stance %q is %s, not running", stanceID, status)
	}

	if task == "" {
		task = "Proceed with the task described in your concern field."
	}
	messages := []Message{{Role: "user", Content: task}}

	tools := make([]ToolDef, 0, len(sess.AuthorizedTools))
	for _, name := range sess.AuthorizedTools {
		tools = append(tools, ToolDef{Name: name})
	}

	outcome := &RunOutcome{StanceID: stanceID}

	for turn := 1; turn <= r.maxTurns; turn++ {
		// Safe point: pause acknowledgments and cancellations land here.
		if err := sess.CheckpointCheck(ctx); err != nil {
			return nil, err
		}
		if r.stanceStatus(sess) == StatusTerminated {
			outcome.Terminated = true
			return outcome, nil
		}

		r.publishAction(bus.EvtWorkerActionStarted, sess, map[string]any{
			"action": "model_turn",
			"turn":   turn,
			"model":  sess.Model,
		})

		resp, err := r.provider.Chat(ctx, ChatRequest{
			Model:        sess.Model,
			SystemPrompt: sess.SystemPrompt,
			Messages:     messages,
			Tools:        tools,
		})
		if err != nil {
			return nil, fmt.Errorf("harness: run: stance %q turn %d: %w", stanceID, turn, err)
		}

		outcome.Turns = turn
		outcome.FinalContent = resp.Content
		outcome.TokensUsed += resp.TokensIn + resp.TokensOut
		outcome.CostUSD += resp.CostUSD
		r.account(sess, resp, turn)

		r.publishAction(bus.EvtWorkerActionCompleted, sess, map[string]any{
			"action":     "model_turn",
			"turn":       turn,
			"tokens_in":  resp.TokensIn,
			"tokens_out": resp.TokensOut,
			"cost_usd":   resp.CostUSD,
			"tool_calls": len(resp.ToolCalls),
		})

		if len(resp.ToolCalls) == 0 {
			return outcome, nil
		}

		messages = append(messages, Message{Role: "assistant", Content: resp.Content})
		for _, call := range resp.ToolCalls {
			outcome.ToolCallsTotal++
			result, denied := r.runToolCall(ctx, sess, call, turn)
			if denied {
				outcome.ToolCallsDenied++
			}
			messages = append(messages, Message{
				Role:    "user",
				Content: fmt.Sprintf("tool_result[%s]: %s", call.Name, result),
			})
		}
	}

	outcome.HitMaxTurns = true
	return outcome, nil
}

// runToolCall enforces htools authorization, executes authorized calls via
// the executor, and emits worker.action events for both outcomes. The
// returned string is the result text fed back to the model; denied reports
// whether authorization rejected the call.
func (r *StanceRunner) runToolCall(ctx context.Context, sess *StanceSession, call ToolCall, turn int) (result string, denied bool) {
	authorized := htools.IsAuthorized(sess.Role, htools.ToolName(call.Name))

	r.publishAction(bus.EvtWorkerActionStarted, sess, map[string]any{
		"action":     "tool_call",
		"tool":       call.Name,
		"turn":       turn,
		"authorized": authorized,
	})

	completed := map[string]any{
		"action":     "tool_call",
		"tool":       call.Name,
		"turn":       turn,
		"authorized": authorized,
	}

	switch {
	case !authorized:
		result = fmt.Sprintf("tool denied: %q is not authorized for role %q", call.Name, sess.Role)
		denied = true
		completed["error"] = result
	case r.exec == nil:
		result = fmt.Sprintf("tool unavailable: no executor wired for %q", call.Name)
		completed["error"] = result
	default:
		out, err := r.exec.Execute(ctx, sess.ID, call)
		if err != nil {
			result = fmt.Sprintf("tool error: %v", err)
			completed["error"] = err.Error()
		} else {
			result = out
		}
	}

	r.publishAction(bus.EvtWorkerActionCompleted, sess, completed)
	return result, denied
}

// account folds a turn's usage into the session under h.mu and writes the
// cost_record ledger node bench.ComputeMetrics sums for mission metrics.
// Telemetry failures are swallowed: observability loss must not kill a run.
func (r *StanceRunner) account(sess *StanceSession, resp *ChatResponse, turn int) {
	r.h.mu.Lock()
	sess.TokensUsed += resp.TokensIn + resp.TokensOut
	sess.CostUSD += resp.CostUSD
	r.h.mu.Unlock()

	content, err := json.Marshal(map[string]any{
		"cost_usd":    resp.CostUSD,
		"tokens_used": resp.TokensIn + resp.TokensOut,
		"model":       sess.Model,
		"stance_id":   sess.ID,
		"turn":        turn,
	})
	if err != nil {
		return
	}
	_, _ = r.h.ledger.AddNode(context.Background(), ledger.Node{
		Type:          "cost_record",
		SchemaVersion: 1,
		CreatedBy:     sess.ID,
		MissionID:     r.h.config.MissionID,
		Content:       content,
	})
}

// publishAction emits a worker.action.* event scoped to the stance.
// Fire-and-forget: bus failures are swallowed like the governance Governor's
// handle cases — the run itself must not die on telemetry loss.
func (r *StanceRunner) publishAction(evtType bus.EventType, sess *StanceSession, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = r.h.bus.Publish(bus.Event{
		Type:      evtType,
		EmitterID: sess.ID,
		Scope: bus.Scope{
			MissionID: r.h.config.MissionID,
			TaskID:    sess.SpawnRequest.TaskDAGScope,
			StanceID:  sess.ID,
			LoopID:    sess.SpawnRequest.LoopRef,
		},
		Payload: body,
	})
}

// stanceStatus reads the session status under the harness lock.
func (r *StanceRunner) stanceStatus(sess *StanceSession) StanceStatus {
	r.h.mu.RLock()
	defer r.h.mu.RUnlock()
	return sess.Status
}
