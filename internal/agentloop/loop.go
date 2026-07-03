// Package agentloop implements a native agentic tool-use loop using the
// Anthropic Messages API. Instead of spawning Claude Code CLI as a subprocess,
// this package talks directly to the API, giving Stoke full control over
// tool execution, prompt caching, cost tracking, and context management.
//
// Architecture follows the P61 research specification:
//   - Stateless Messages API: entire conversation sent each turn
//   - Tool definitions with cache_control for 90% cost reduction
//   - Parallel tool execution when Claude requests multiple tools
//   - Extended thinking support with signature preservation
//   - Defensive loop bounds (max turns + consecutive error tracking)
//   - Token usage accumulation for real-time cost tracking
package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/antitrunc"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
	"github.com/RelayOne/r1/internal/throttle"
)

// Anthropic content-block and stop-reason discriminators. Mirrors the
// strings used in the Messages API; centralised so the various switches
// in this file stay in sync.
const (
	blockText             = "text"
	blockToolUse          = "tool_use"
	blockThinking         = "thinking"
	blockRedactedThinking = "redacted_thinking"
)

// CortexHook is the agentloop's view into a parallel-cognition substrate.
// internal/cortex.Cortex satisfies this interface automatically. The
// interface lives here (not in cortex/) to avoid an import cycle since
// cortex imports agentloop for Message.
type CortexHook interface {
	MidturnNote(messages []Message, turn int) string
	PreEndTurnGate(messages []Message) string
}

// Config configures the agent loop.
type Config struct {
	Model              string        // e.g. "claude-sonnet-4-5-20250929"
	MaxTurns           int           // hard limit on API calls (default 25)
	MaxConsecutiveErrs int           // consecutive tool errors before abort (default 3)
	MaxTokens          int           // max output tokens per turn (default 16000)
	SystemPrompt       string        // static system prompt (cached)
	// ThinkingBudget enables extended thinking. 0 = disabled (today's
	// behavior for every existing call site). The value is a hint:
	// adaptive-thinking models get {"type":"adaptive"} and ignore the
	// number; legacy models get it as budget_tokens, which counts
	// against MaxTokens — keep it well below MaxTokens or the visible
	// output headroom collapses. Kill switch: R1_DISABLE_THINKING=1.
	ThinkingBudget int
	Timeout        time.Duration // per-turn timeout

	// Correlation IDs (AL-1 / SEAM-22). When set, the loop copies them
	// into every outbound ChatRequest.Metadata so the provider adapter
	// can set X-Stoke-Session-ID / X-Stoke-Agent-ID / X-Stoke-Task-ID
	// on the HTTP request. Empty strings are skipped (no empty-header
	// emission for standalone runs).
	SessionID string
	AgentID   string
	TaskID    string
	// TenantID identifies the operator/account that owns this session.
	// When set, the throttle gate uses it for the per-tenant bucket
	// (the spec's two-tier policy). Empty disables the per-tenant
	// check (single-tenant local dev).
	TenantID string

	// Throttler is the optional C3 per-tool rate-limit gate. When
	// non-nil, executeTools calls Throttler.AllowAgentloop BEFORE
	// invoking the registered handler. On denial, a synthetic
	// tool_result block with isError=true is emitted in place of the
	// real result so the model can recover or wait. nil disables the
	// gate (matches --one-shot and the R1_DISABLE_THROTTLE escape
	// hatch). See specs/per-tool-throttling.md task T8 and T15.
	Throttler ThrottleGate

	// ValidateToolInput is the parallel A1-T2 promptguard gate that
	// fires AFTER throttle but BEFORE the handler runs. Declared as a
	// sibling field of Throttler so the two branches do not collide
	// on the loop dispatch site.
	ValidateToolInput ToolInputValidator
	// CompactThreshold is the estimated input-token count above which
	// the loop calls CompactFn to rewrite the message history. 0 = no
	// automatic compaction.
	CompactThreshold int
	// CompactFn is a hook that rewrites the message history when the
	// loop detects the conversation has grown past CompactThreshold.
	// The hook receives the current slice and must return a new slice
	// (or the same one) that is safe to continue from. Implementations
	// should:
	//   - Preserve the first user message (the task brief)
	//   - Preserve recent tool_use/tool_result pairs (in-flight work)
	//   - Summarize or drop old tool_result content
	// nil = no compaction.
	CompactFn CompactFunc
	// MidturnCheckFn is an optional supervisor hook called between
	// turns (AFTER tool results are appended, BEFORE the next API
	// call). If it returns a non-empty string, that text is appended
	// to the next user message as a "SUPERVISOR NOTE" so the model
	// sees an out-of-band directive on its next turn.
	//
	// Typical use: "every few writes, scan the declared files for
	// spec violations and push a correction if identifiers drifted".
	// The hook must NOT mutate messages directly — it should only
	// return the text to inject.
	//
	// nil = no midturn checks (default).
	MidturnCheckFn MidturnCheckFunc

	// PreEndTurnCheckFn runs when the model attempts end_turn.
	// If it returns a non-empty string (build errors), the loop
	// injects the errors and forces another turn instead of
	// exiting. This is the Cline/Aider pattern: build errors
	// must be fixed in the SAME conversation turn while the
	// model has full context of what it just wrote.
	//
	// Returns "" to allow end_turn, or an error message to
	// force continuation. The check typically runs the project's
	// build command (tsc --noEmit, go build, cargo check) and
	// returns any compile errors found.
	//
	// nil = no pre-end-turn check (default — all end_turns
	// are accepted immediately).
	PreEndTurnCheckFn func(messages []Message) string

	// HoneypotCheckFn, when non-nil, is invoked at pre-end-turn
	// immediately AFTER PreEndTurnCheckFn passes. Unlike the
	// build-verification gate (which forces a retry on failure),
	// a honeypot firing ABORTS the turn — the model has emitted
	// output matching an injection / exfiltration / jailbreak
	// probe, and no amount of retrying will correct that.
	//
	// Returns "" when no honeypot fired. A non-empty return
	// aborts the loop with StopReason="honeypot_fired" and a
	// wrapped error carrying the firing reason. Intended to be
	// wired by the native runner from a
	// critic.HoneypotRegistry — see
	// internal/critic/default_honeypots.go (Track A Task 3).
	//
	// nil = no honeypot evaluation (default).
	HoneypotCheckFn func(messages []Message) string

	// Cortex is an optional CortexHook (typically *cortex.Cortex) that
	// participates in the MidturnCheckFn and PreEndTurnCheckFn pipelines.
	// The Cortex hook fires FIRST; operator hooks (existing fields) fire
	// SECOND. Outputs are joined with "\n\n" for MidturnCheckFn;
	// PreEndTurnCheckFn short-circuits on the first non-empty return.
	Cortex CortexHook

	// PreTurnHook fires once at the top of every turn iteration —
	// before compaction, before the API call, before any token
	// estimation. The hook is passed the loop's ctx; returning an
	// error aborts Run with that error. Returning nil with a
	// non-default ctx replacement is intentionally NOT supported —
	// pause-style gates should block inside the hook on their own
	// channel and respect ctx.Done() themselves.
	//
	// Typical use: pause/resume gating from the daemon's Session
	// layer. sessionhub installs a hook that calls Session.WaitWhilePaused;
	// while paused, the hook blocks on the resume channel; when ctx
	// fires, the hook returns ctx.Err() and the loop exits cleanly.
	//
	// Optional. nil = no pre-turn gate.
	PreTurnHook func(ctx context.Context, turn int, messages []Message) error

	// EndTurnContinuation fires when the model emits end_turn (after
	// the build-verification gate has accepted the turn) and lets a
	// caller convert pending inbound user input into another turn
	// instead of exiting Run. Returns the text of the next user turn
	// (non-empty) to inject and continue the loop, or "" to allow
	// end_turn to terminate Run as usual.
	//
	// Typical use: the daemon's Session layer drains its inbox
	// channel here so a session.send RPC delivered while the model
	// was thinking gets picked up at end_turn rather than dropped.
	//
	// The hook MUST be quick + non-blocking. To wait for the next
	// user message, return "" and let the caller drive the next Run
	// invocation. To deliver up to one queued message per end_turn
	// without blocking, do a non-blocking channel receive and return
	// the text or "".
	//
	// Optional. nil = end_turn always terminates Run.
	EndTurnContinuation func() string

	// defaultsApplied guards defaults() against double-wrap when the
	// method is invoked more than once on the same Config (e.g. test
	// helpers that re-init or call defaults() before passing to New).
	defaultsApplied bool

	// AntiTruncEnforce, when true, prepends an antitrunc.Gate to
	// PreEndTurnCheckFn. The gate refuses end_turn while the
	// model has emitted truncation phrases or while plan / spec
	// items remain unchecked. See internal/agentloop/antitrunc.go
	// and internal/antitrunc/gate.go for the layered defense.
	//
	// Default false during rollout; flips to true once the
	// integration test suite (item 25) passes in CI for one full
	// week without false positives.
	AntiTruncEnforce bool

	// AntiTruncPlanPath is the path the gate reads to count
	// unchecked items. Empty disables plan scanning.
	AntiTruncPlanPath string

	// AntiTruncSpecPaths are the spec markdown files the gate
	// scans for STATUS:in-progress + unchecked items.
	AntiTruncSpecPaths []string

	// AntiTruncCommitLookbackFn returns recent commit bodies for
	// the gate's false-completion check. nil = skip commit scan.
	AntiTruncCommitLookbackFn func(n int) ([]string, error)

	// AntiTruncAdvisory demotes the gate to advisory-only:
	// findings are forwarded to AntiTruncAdvisoryFn but the gate
	// returns "" so the loop is not blocked. This is the operator
	// override (`--no-antitrunc-enforce`).
	AntiTruncAdvisory bool

	// AntiTruncAdvisoryFn receives findings when AntiTruncAdvisory
	// is true. nil = silently dropped (the gate still detects but
	// does nothing observable).
	AntiTruncAdvisoryFn func(antitrunc.Finding)
}

// MidturnCheckFunc is the signature for the between-turn supervisor
// hook. It receives the current message history and the turn number
// (0-indexed, counting only turns that produced tool calls) and
// returns a supervisor message to append, or "" if no intervention is
// needed.
type MidturnCheckFunc func(messages []Message, turn int) string

// CompactFunc is the signature for the per-turn compaction hook. It
// receives the current message list and the estimated token count; it
// must return a new message list that preserves tool_use/tool_result
// pair integrity (otherwise the API will reject the next request).
type CompactFunc func(messages []Message, estimatedTokens int) []Message

func (c *Config) defaults() {
	if c.defaultsApplied {
		return
	}
	if c.MaxTurns == 0 {
		c.MaxTurns = 25
	}
	if c.MaxConsecutiveErrs == 0 {
		c.MaxConsecutiveErrs = 3
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 16000
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Minute
	}
	// Compose Cortex with operator hooks. Cortex fires FIRST per
	// spec §"Integration points" §2 and §3:
	//   - MidturnCheckFn: outputs joined with "\n\n" (cortex first, operator second)
	//   - PreEndTurnCheckFn: short-circuits on cortex non-empty return
	if c.Cortex != nil {
		cortex := c.Cortex
		operatorMid := c.MidturnCheckFn
		c.MidturnCheckFn = func(msgs []Message, turn int) string {
			cx := cortex.MidturnNote(msgs, turn)
			var op string
			if operatorMid != nil {
				op = operatorMid(msgs, turn)
			}
			switch {
			case cx == "" && op == "":
				return ""
			case cx == "":
				return op
			case op == "":
				return cx
			default:
				return cx + "\n\n" + op
			}
		}
		operatorEnd := c.PreEndTurnCheckFn
		c.PreEndTurnCheckFn = func(msgs []Message) string {
			if cx := cortex.PreEndTurnGate(msgs); cx != "" {
				return cx // critical Note refuses end_turn — short-circuit
			}
			if operatorEnd != nil {
				return operatorEnd(msgs)
			}
			return ""
		}
	}

	// Anti-truncation: install the gate AFTER cortex composition so it
	// wraps the resulting PreEndTurnCheckFn and fires FIRST when invoked.
	// Per spec 9 D-2026-05-04-02 the gate refusal short-circuits all
	// downstream hooks (cortex + operator). See internal/agentloop/antitrunc.go.
	c.installAntiTruncGate()

	c.defaultsApplied = true
}

// ContentBlock is a typed content block in a message.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`         // tool_use ID
	Name      string          `json:"name,omitempty"`       // tool name
	Input     json.RawMessage `json:"input,omitempty"`      // tool input JSON
	ToolUseID string          `json:"tool_use_id,omitempty"` // for tool_result
	Content   string          `json:"content,omitempty"`     // tool_result content
	IsError   bool            `json:"is_error,omitempty"`    // tool_result error flag
	Thinking  string          `json:"thinking,omitempty"`    // thinking content
	Signature string          `json:"signature,omitempty"`   // thinking signature
	Data      string          `json:"data,omitempty"`        // redacted_thinking opaque payload
}

// Message is a conversation message with typed content blocks.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ToolHandler executes a tool and returns the result string.
// If the tool fails, return an error and the loop will send is_error: true.
type ToolHandler func(ctx context.Context, name string, input json.RawMessage) (string, error)

// ThrottleGate is the narrow interface executeTools uses to consult
// the C3 rate-limit gate before invoking a tool handler. The throttle
// package's *limiter satisfies it directly; tests can supply a stub.
//
// We import internal/throttle here rather than redeclaring the
// Decision type to keep one source of truth for the wire shape and
// to let the daemon pass its throttle.Limiter straight into
// Config.Throttler without an adapter wrapper.
type ThrottleGate interface {
	AllowAgentloop(ctx context.Context, sessionID, tenantID, tool string) throttle.Decision
}

// ToolInputValidator is the A1-T2 promptguard input-validation gate
// signature. Declared here (rather than in internal/promptguard) for
// the same import-cycle reason as ThrottleGate. The agentloop only
// cares about the decision shape; whether it scans patterns or runs
// an LLM secondary check is the validator's business.
type ToolInputValidator func(ctx context.Context, sessionID, tenantID, tool string, input json.RawMessage) ToolInputDecision

// ToolInputDecision is the parallel-branch promptguard return.
// Allowed=false replaces the tool result with Message + IsError.
type ToolInputDecision struct {
	Allowed bool
	Message string
}

// OnTextFunc is called with incremental text output for streaming display.
type OnTextFunc func(text string)

// OnToolUseFunc is called when a tool is about to be executed.
type OnToolUseFunc func(name string, input json.RawMessage)

// Result is the final output of an agent loop run.
type Result struct {
	// FinalText is the concatenated text output from the final assistant turn.
	FinalText string `json:"final_text"`
	// Turns is the number of API calls made.
	Turns int `json:"turns"`
	// TotalCost tracks token usage across all turns.
	TotalCost CostTracker `json:"cost"`
	// StopReason is how the loop terminated.
	StopReason string `json:"stop_reason"`
	// Messages is the full conversation history.
	Messages []Message `json:"messages"`
}

// CostTracker accumulates token usage across turns.
type CostTracker struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
}

// Add accumulates usage from a single API response.
func (c *CostTracker) Add(usage stream.TokenUsage) {
	c.InputTokens += usage.Input
	c.OutputTokens += usage.Output
	c.CacheWriteTokens += usage.CacheCreation
	c.CacheReadTokens += usage.CacheRead
}

// TotalCostUSD computes the total cost based on model pricing.
func (c *CostTracker) TotalCostUSD(model string) float64 {
	var inputPrice, outputPrice, cacheWritePrice, cacheReadPrice float64

	// Pricing per token (derived from per-MTok rates)
	switch {
	case contains(model, "opus"):
		inputPrice = 5.0 / 1_000_000
		outputPrice = 25.0 / 1_000_000
		cacheWritePrice = 6.25 / 1_000_000
		cacheReadPrice = 0.50 / 1_000_000
	case contains(model, "haiku"):
		inputPrice = 1.0 / 1_000_000
		outputPrice = 5.0 / 1_000_000
		cacheWritePrice = 1.25 / 1_000_000
		cacheReadPrice = 0.10 / 1_000_000
	default: // sonnet and others
		inputPrice = 3.0 / 1_000_000
		outputPrice = 15.0 / 1_000_000
		cacheWritePrice = 3.75 / 1_000_000
		cacheReadPrice = 0.30 / 1_000_000
	}

	return float64(c.InputTokens)*inputPrice +
		float64(c.OutputTokens)*outputPrice +
		float64(c.CacheWriteTokens)*cacheWritePrice +
		float64(c.CacheReadTokens)*cacheReadPrice
}

// Loop is the main agentic tool-use loop.
type Loop struct {
	config    Config
	provider  provider.Provider
	tools     []provider.ToolDef
	handler   ToolHandler
	onText    OnTextFunc
	onToolUse OnToolUseFunc
	eventBus  *hub.Bus // optional: publishes EvtToolPreUse/EvtToolPostUse events
}

// New creates a new agent loop.
func New(p provider.Provider, cfg Config, tools []provider.ToolDef, handler ToolHandler) *Loop {
	cfg.defaults()
	return &Loop{
		config:   cfg,
		provider: p,
		tools:    tools,
		handler:  handler,
	}
}

// SetOnText sets the callback for streaming text output.
func (l *Loop) SetOnText(fn OnTextFunc) { l.onText = fn }

// SetOnToolUse sets the callback for tool execution notifications.
func (l *Loop) SetOnToolUse(fn OnToolUseFunc) { l.onToolUse = fn }

// SetEventBus sets the hub event bus for publishing tool use events.
func (l *Loop) SetEventBus(bus *hub.Bus) { l.eventBus = bus }

// checkThrottle consults the configured ThrottleGate (if any). Nil
// gate or nil-config means "allow everything" — preserves the
// existing behavior for call sites that don't wire the C3 throttle.
// See specs/per-tool-throttling.md task T8 and T15.
func (l *Loop) checkThrottle(ctx context.Context, tool string) throttle.Decision {
	if l == nil || l.config.Throttler == nil {
		return throttle.Decision{Allowed: true, Tool: tool}
	}
	return l.config.Throttler.AllowAgentloop(ctx, l.config.SessionID, l.config.TenantID, tool)
}

// Run executes the agentic loop starting from a user message.
// It continues until the model stops requesting tools or limits are hit.
func (l *Loop) Run(ctx context.Context, userMessage string) (*Result, error) {
	messages := []Message{{
		Role:    "user",
		Content: []ContentBlock{{Type: blockText, Text: userMessage}},
	}}
	return l.RunWithHistory(ctx, messages)
}

// RunWithHistory executes the loop with a pre-existing conversation history.
func (l *Loop) RunWithHistory(ctx context.Context, messages []Message) (*Result, error) {
	result := &Result{Messages: messages}
	consecutiveErrors := 0

	for turn := 0; turn < l.config.MaxTurns; turn++ {
		select {
		case <-ctx.Done():
			result.StopReason = "cancelled"
			result.Turns = turn
			return result, ctx.Err()
		default:
		}

		// PreTurnHook gate. Fires before any token-estimating or
		// API-side work so a paused session pays no model cost while
		// it waits. The hook is responsible for honouring ctx.Done()
		// itself; returning an error aborts the loop cleanly.
		if l.config.PreTurnHook != nil {
			if err := l.config.PreTurnHook(ctx, turn, messages); err != nil {
				result.StopReason = "cancelled"
				result.Turns = turn
				return result, err
			}
		}

		// Progressive compaction: before every API call, estimate the
		// conversation's token footprint and call the CompactFn hook if
		// it's grown past CompactThreshold. The hook returns a (possibly
		// rewritten) message list; we trust it to preserve
		// tool_use/tool_result pair integrity so the next API call
		// doesn't 400.
		if l.config.CompactFn != nil && l.config.CompactThreshold > 0 {
			est := estimateMessagesTokens(messages)
			if est > l.config.CompactThreshold {
				compacted := l.config.CompactFn(messages, est)
				if len(compacted) > 0 {
					messages = compacted
					result.Messages = messages
				}
			}
		}

		// Build the API request
		chatReq := l.buildRequest(messages)

		// Call the API with streaming
		var assistantBlocks []ContentBlock
		var lastText string
		resp, err := l.provider.ChatStream(chatReq, func(ev stream.Event) {
			if ev.DeltaText != "" && l.onText != nil {
				l.onText(ev.DeltaText)
				lastText += ev.DeltaText
			}
		})
		if err != nil {
			return result, fmt.Errorf("turn %d API call: %w", turn, err)
		}

		// Accumulate cost
		result.TotalCost.Add(resp.Usage)

		// TASK-24: emit EventModelPostCall with Model.Role="main" so a
		// Cortex (or any other subscriber) can track main-turn token
		// usage from the bus. Gated on l.eventBus != nil so loops
		// without a bus stay zero-overhead. This is the ONE site where
		// the agentloop accounts for its own API call; other emitters
		// (workflow.go, builtin/cost_tracker, tui/cost_dashboard) are
		// downstream of this signal in different roles and will not
		// collide because Role differs.
		if l.eventBus != nil {
			l.eventBus.EmitAsync(&hub.Event{
				Type:      hub.EventModelPostCall,
				Timestamp: time.Now(),
				MissionID: l.config.SessionID,
				TaskID:    l.config.TaskID,
				AgentID:   l.config.AgentID,
				Model: &hub.ModelEvent{
					Model:        l.config.Model,
					Role:         "main",
					InputTokens:  resp.Usage.Input,
					OutputTokens: resp.Usage.Output,
					CachedTokens: resp.Usage.CacheRead,
					StopReason:   resp.StopReason,
				},
			})
		}

		// Convert response content to our ContentBlock format
		for _, rc := range resp.Content {
			block := ContentBlock{Type: rc.Type}
			switch rc.Type {
			case blockText:
				block.Text = rc.Text
			case blockToolUse:
				block.ID = rc.ID
				block.Name = rc.Name
				if rc.Input != nil {
					inputJSON, _ := json.Marshal(rc.Input)
					block.Input = inputJSON
				}
			case blockThinking:
				// Thinking + signature must survive into history
				// verbatim: with extended thinking + tool use, the API
				// requires the assistant turn's thinking blocks to be
				// replayed unchanged on the next request.
				block.Thinking = rc.Thinking
				block.Signature = rc.Signature
			case blockRedactedThinking:
				block.Data = rc.Data
			}
			assistantBlocks = append(assistantBlocks, block)
		}

		// Append assistant response to history
		assistantMsg := Message{Role: "assistant", Content: assistantBlocks}
		messages = append(messages, assistantMsg)

		// Check stop reason — but INTERCEPT end_turn when a
		// pre-completion check fails (Cline pattern: build
		// errors must be fixed in the SAME turn, not a
		// separate repair dispatch).
		if resp.StopReason != blockToolUse {
			// Pre-end-turn verification: run the build check
			// and force another turn if it fails. This is the
			// single biggest quality improvement — the model
			// sees tsc errors while it still has full context
			// of what it just wrote.
			if l.config.PreEndTurnCheckFn != nil && turn < l.config.MaxTurns-1 {
				if errMsg := l.config.PreEndTurnCheckFn(messages); errMsg != "" {
					// Build failed — inject errors and continue
					// the loop instead of exiting. The model
					// gets one more chance to fix in-context.
					messages = append(messages, Message{
						Role: "user",
						Content: []ContentBlock{{
							Type: blockText,
							Text: "[BUILD VERIFICATION FAILED — fix before completing]\n\n" + errMsg + "\n\nFix these errors now. Do NOT end your turn until the build passes.",
						}},
					})
					continue // back to top of loop for another turn
				}
			}
			// Honeypot evaluation runs AFTER the build gate. A firing
			// means the model emitted output matching an injection /
			// exfil / jailbreak probe — we abort rather than retry,
			// because a compromised turn can't be "fixed" by asking
			// the (possibly compromised) model to try again.
			if l.config.HoneypotCheckFn != nil {
				if hpMsg := l.config.HoneypotCheckFn(messages); hpMsg != "" {
					slog.Error("honeypot triggered — aborting turn",
						"turn", turn,
						"reason", hpMsg)
					result.StopReason = "honeypot_fired"
					result.Turns = turn + 1
					result.FinalText = extractText(assistantBlocks)
					result.Messages = messages
					return result, fmt.Errorf("aborted: honeypot triggered (%s)", hpMsg)
				}
			}
			// EndTurnContinuation: when set, ask the caller whether
			// there's pending inbound input (e.g. a queued
			// session.send) that should keep the loop going instead
			// of returning. Non-empty return becomes the next user
			// turn; empty return falls through to the normal
			// end_turn exit. Hook MUST be non-blocking.
			if l.config.EndTurnContinuation != nil {
				if next := l.config.EndTurnContinuation(); next != "" {
					messages = append(messages, Message{
						Role: "user",
						Content: []ContentBlock{{
							Type: blockText,
							Text: next,
						}},
					})
					continue // run another turn with the new user message
				}
			}
			result.StopReason = resp.StopReason
			result.Turns = turn + 1
			result.FinalText = extractText(assistantBlocks)
			result.Messages = messages
			return result, nil
		}

		// Execute tools and collect results
		toolResults, hasError := l.executeTools(ctx, assistantBlocks)

		// Track consecutive errors
		if hasError {
			consecutiveErrors++
		} else {
			consecutiveErrors = 0
		}
		if consecutiveErrors >= l.config.MaxConsecutiveErrs {
			result.StopReason = "max_errors"
			result.Turns = turn + 1
			result.FinalText = extractText(assistantBlocks)
			result.Messages = messages
			return result, fmt.Errorf("aborted after %d consecutive tool errors", consecutiveErrors)
		}

		// Append tool results as user message
		messages = append(messages, Message{
			Role:    "user",
			Content: toolResults,
		})

		// Midturn supervisor check. Runs AFTER tool results are in
		// but BEFORE the next API call, so any note gets attached to
		// the existing user message (which already holds the tool
		// results). The supervisor typically uses this to run a
		// spec-faithfulness scan every N writes and push a
		// correction when code diverges from canonical identifiers.
		if l.config.MidturnCheckFn != nil {
			if note := l.config.MidturnCheckFn(messages, turn); note != "" {
				// Attach as an extra text block on the same user
				// message. Anthropic content blocks can mix
				// tool_result and text in one message — the model
				// reads them in order.
				lastIdx := len(messages) - 1
				messages[lastIdx].Content = append(messages[lastIdx].Content, ContentBlock{
					Type: blockText,
					Text: "[SUPERVISOR NOTE] " + note,
				})
			}
		}
	}

	result.StopReason = "max_turns"
	result.Turns = l.config.MaxTurns
	result.Messages = messages
	return result, fmt.Errorf("exceeded max turns (%d)", l.config.MaxTurns)
}

// buildRequest constructs the provider.ChatRequest from current state.
// Tools are sorted alphabetically to prevent cache busting.
// System prompt uses cache_control breakpoints for 90% cost reduction.
func (l *Loop) buildRequest(messages []Message) provider.ChatRequest {
	// Convert our Message format to provider's ChatMessage format
	chatMsgs := make([]provider.ChatMessage, len(messages))
	for i, msg := range messages {
		contentJSON, _ := json.Marshal(msg.Content)
		chatMsgs[i] = provider.ChatMessage{
			Role:    msg.Role,
			Content: contentJSON,
		}
	}

	// Sort tools alphabetically — non-deterministic ordering busts cache on every turn
	sortedTools := SortToolsDeterministic(l.tools)

	// Build system prompt with cache_control breakpoint on static content
	systemBlocks := BuildCachedSystemPrompt(l.config.SystemPrompt, "")
	systemJSON, _ := json.Marshal(systemBlocks)

	// AL-1: populate Metadata with correlation IDs so the provider
	// adapter sets X-Stoke-* headers on the outbound HTTP call. Only
	// non-empty values populate the map so standalone runs emit no
	// correlation headers.
	var meta map[string]string
	if l.config.SessionID != "" || l.config.AgentID != "" || l.config.TaskID != "" {
		meta = make(map[string]string, 3)
		if l.config.SessionID != "" {
			meta["stoke-session-id"] = l.config.SessionID
		}
		if l.config.AgentID != "" {
			meta["stoke-agent-id"] = l.config.AgentID
		}
		if l.config.TaskID != "" {
			meta["stoke-task-id"] = l.config.TaskID
		}
	}

	// Extended thinking: emitted only when a positive budget is
	// configured AND the kill switch is off. The provider adapter is
	// responsible for model-correct wire shape (adaptive vs
	// budget_tokens) and fails closed on unknown models.
	var thinking *provider.ThinkingSpec
	if l.config.ThinkingBudget > 0 && os.Getenv("R1_DISABLE_THINKING") != "1" {
		thinking = &provider.ThinkingSpec{BudgetTokens: l.config.ThinkingBudget}
	}

	return provider.ChatRequest{
		Model:        l.config.Model,
		SystemRaw:    systemJSON,
		Messages:     chatMsgs,
		MaxTokens:    l.config.MaxTokens,
		Tools:        sortedTools,
		CacheEnabled: true,
		Metadata:     meta,
		Thinking:     thinking,
	}
}

// executeTools runs all tool_use blocks and returns tool_result blocks.
func (l *Loop) executeTools(ctx context.Context, blocks []ContentBlock) ([]ContentBlock, bool) {
	var toolCalls []ContentBlock
	for _, b := range blocks {
		if b.Type == blockToolUse {
			toolCalls = append(toolCalls, b)
		}
	}

	if len(toolCalls) == 0 {
		return nil, false
	}

	// Execute tools in parallel when multiple are requested
	results := make([]ContentBlock, len(toolCalls))
	hasError := false
	var mu sync.Mutex

	execOne := func(idx int, tc ContentBlock) {
		if l.onToolUse != nil {
			l.onToolUse(tc.Name, tc.Input)
		}

		// Pre-dispatch gates (C3 throttle + A1-T2 promptguard input
		// validation). Both are siblings on Config: nil-disabled by
		// default so existing call sites are unaffected. Throttle
		// fires first because it is cheap and deterministic.
		if dec := l.checkThrottle(ctx, tc.Name); !dec.Allowed {
			mu.Lock()
			hasError = true
			results[idx] = ContentBlock{
				Type:      "tool_result",
				ToolUseID: tc.ID,
				Content:   fmt.Sprintf("Throttled: retry after %.1fs. Use a different tool or wait.", dec.RetryAfter.Seconds()),
				IsError:   true,
			}
			mu.Unlock()
			return
		}
		if l.config.ValidateToolInput != nil {
			if dec := l.config.ValidateToolInput(ctx, l.config.SessionID, l.config.TenantID, tc.Name, tc.Input); !dec.Allowed {
				mu.Lock()
				hasError = true
				results[idx] = ContentBlock{
					Type:      "tool_result",
					ToolUseID: tc.ID,
					Content:   dec.Message,
					IsError:   true,
				}
				mu.Unlock()
				return
			}
		}

		// Emit pre-use event (gate can block)
		if l.eventBus != nil {
			preEv := &hub.Event{
				Type:      hub.EventToolPreUse,
				Timestamp: time.Now(),
				Tool: &hub.ToolEvent{
					Name:  tc.Name,
					Input: parseToolInput(tc.Input),
				},
			}
			resp := l.eventBus.Emit(ctx, preEv)
			if resp.Decision == hub.Deny {
				mu.Lock()
				hasError = true
				results[idx] = ContentBlock{
					Type:      "tool_result",
					ToolUseID: tc.ID,
					Content:   fmt.Sprintf("Tool blocked by policy: %s", resp.Reason),
					IsError:   true,
				}
				mu.Unlock()
				return
			}
		}

		// Per-tool input validation (specs/promptguard-hardening.md
		// §T2 item 9). Runs AFTER the hub.EventToolPreUse gate (so a
		// policy hook can still cheaply deny first) but BEFORE the
		// tool handler executes. On rejection the tool_result fed
		// back into the model is is_error=true with the structured
		// rejection text so the model can see and revise. The model
		// is NEVER given the raw payload — only the rejection reason.
		if rejErr := validateAgentloopToolInput(ctx, tc.Name, tc.Input); rejErr != nil {
			mu.Lock()
			hasError = true
			results[idx] = ContentBlock{
				Type:      "tool_result",
				ToolUseID: tc.ID,
				Content:   rejErr.Error(),
				IsError:   true,
			}
			mu.Unlock()
			return
		}

		start := time.Now()
		content, err := l.handler(ctx, tc.Name, tc.Input)
		duration := time.Since(start)

		// Defensive sanitization: tool output can carry adversarial
		// content (Read on an attacker file, Bash stdout, MCP replies)
		// that flows straight back into the model context. Three-layer
		// guard: size cap, chat-template-token scrub, injection-shape
		// annotation. Only log when something was actually actioned so
		// the happy path stays quiet.
		sanitized, sanReport := SanitizeToolOutput(content, tc.Name)
		if sanReport.Actioned() {
			slog.Warn("tool output sanitized",
				"tool", tc.Name,
				"original_bytes", sanReport.OriginalBytes,
				"final_bytes", sanReport.FinalBytes,
				"truncated", sanReport.Truncated,
				"template_tokens", sanReport.TemplateTokensFound,
				"injection_threats", sanReport.InjectionThreats,
			)
		}

		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			hasError = true
			results[idx] = ContentBlock{
				Type:      "tool_result",
				ToolUseID: tc.ID,
				Content:   fmt.Sprintf("Error: %v. Try a different approach.", err),
				IsError:   true,
			}
		} else {
			results[idx] = ContentBlock{
				Type:      "tool_result",
				ToolUseID: tc.ID,
				Content:   sanitized,
			}
		}

		// Emit post-use event
		if l.eventBus != nil {
			postEv := &hub.Event{
				Type:      hub.EventToolPostUse,
				Timestamp: time.Now(),
				Tool: &hub.ToolEvent{
					Name:     tc.Name,
					Input:    parseToolInput(tc.Input),
					Output:   truncateOutput(content, 1024),
					Duration: duration,
				},
			}
			l.eventBus.EmitAsync(postEv)
		}
	}

	if len(toolCalls) == 1 {
		execOne(0, toolCalls[0])
	} else {
		var wg sync.WaitGroup
		for i, tc := range toolCalls {
			wg.Add(1)
			go func(idx int, call ContentBlock) {
				defer wg.Done()
				execOne(idx, call)
			}(i, tc)
		}
		wg.Wait()
	}

	return results, hasError
}

// extractText concatenates all text blocks from a response.
func extractText(blocks []ContentBlock) string {
	var text string
	for _, b := range blocks {
		if b.Type == blockText {
			text += b.Text
		}
	}
	return text
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// parseToolInput converts JSON input to a map for hub events.
func parseToolInput(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// truncateOutput limits output size for hub events.
func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}

// estimateMessagesTokens returns a rough token count for a message list.
// Uses the 4-chars-per-token heuristic (matches the rest of Stoke's
// estimator helpers). Accuracy is not critical — this feeds the
// CompactThreshold check which is a best-effort guard, not a hard
// budget.
func estimateMessagesTokens(messages []Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Role) + 8 // role + framing overhead
		for _, c := range m.Content {
			chars += len(c.Type) + 4
			chars += len(c.Text)
			chars += len(c.Content)   // tool_result content
			chars += len(c.Thinking)
			if len(c.Input) > 0 {
				chars += len(c.Input)
			}
			if len(c.ID) > 0 {
				chars += len(c.ID) + 4
			}
			if len(c.Name) > 0 {
				chars += len(c.Name) + 4
			}
		}
	}
	if chars == 0 {
		return 0
	}
	return chars / 4
}
