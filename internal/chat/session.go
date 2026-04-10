// Package chat provides a streaming, multi-turn conversational loop on
// top of internal/provider with first-class support for DISPATCHER TOOLS.
//
// Chat is the "talk to Stoke" path. A user types free text at the REPL
// or shell, chat replies conversationally, and when the user signals
// agreement ("yeah build it", "ya make that a scope", "ship it") the
// model emits a dispatcher tool call that routes to the real Stoke
// pipeline (/scope, /build, /ship, /plan, /audit, /scan, /status).
//
// Design notes:
//   - Streaming-first. Callers pass an OnDelta callback and see the
//     reply appear token by token. The full text is also returned for
//     tests and for the "last reply" concept.
//   - Stateful session, stateless provider. The provider is re-used
//     across turns but the Session owns the conversation history and
//     feeds the entire history back on each turn (Anthropic-style).
//   - Tools are dispatcher tools, not file-edit tools. The LLM cannot
//     touch the filesystem from chat mode — if it wants to edit code,
//     it must dispatch to /run or /ship, which kick off a real
//     supervised build pipeline with its own hooks, worktree isolation,
//     and verification.
//   - Tool execution is user-visible. Each dispatch is announced via
//     OnDispatch BEFORE the tool handler runs so the UI can paint a
//     "▸ Dispatching to /scope..." banner. The handler's return string
//     goes back to the model as the tool_result, and the loop continues
//     so the model can summarize what happened or ask a follow-up.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/stream"
)

// DefaultSystemPrompt is the baseline system prompt for Stoke's chat mode.
// It introduces Stoke, explains the tool-use dispatch model, and asks the
// model to stay conversational until the user signals agreement.
const DefaultSystemPrompt = `You are Stoke, a supervised AI build orchestrator running locally in the user's terminal. You are in CHAT MODE, talking directly to the user.

How chat mode works:
  1. You and the user discuss what they want to do.
  2. You ask clarifying questions, propose approaches, and help them refine their idea.
  3. When the user signals AGREEMENT ("yeah, build it", "ya make that a scope", "ship it", "do it", "go", etc.), you call the matching dispatcher tool. The tool description is your paraphrase of the agreed-upon work — concise, 1-3 sentences, capturing the goal and key decisions.

Rules:
  - Keep replies short and conversational. No markdown headers, no emojis, no bullet lists unless the user asks for one.
  - Do NOT dispatch until the user has clearly agreed. If the user says "what if we add X", that is NOT a dispatch signal — it is a refinement. Only dispatch on explicit "do it" style signals.
  - You cannot touch the filesystem from chat. You cannot run commands. You can only have a conversation and then dispatch to one of the tools.
  - When you dispatch, the tool description should be self-contained — the downstream pipeline does not have access to this conversation's history. Include the important decisions from our discussion inline.
  - If the user asks a question you can answer without dispatching (e.g. "what does this package do?", "explain X"), answer in plain text. No tool call needed.
  - If the user gives an instruction that is clearly a single-shot build directive with no ambiguity (e.g. "add a README.md at the root explaining Stoke"), you may dispatch immediately without a discussion round — the user has already made the decision.

Available dispatchers:
  dispatch_scope   — start an interactive scoping session to flesh out scope before committing to a build
  dispatch_build   — single task through the full plan → execute → verify pipeline (equivalent to /run)
  dispatch_ship    — build → review → fix loop until ship-ready (equivalent to /ship)
  dispatch_plan    — generate a plan without executing
  dispatch_audit   — run the multi-persona audit
  dispatch_scan    — run the deterministic code scanner
  show_status      — show the current session dashboard`

// Config controls a chat Session's behavior.
type Config struct {
	// Model is the provider-specific model ID (e.g. "claude-sonnet-4-6").
	Model string
	// SystemPrompt is the system instruction sent with every turn. If
	// empty, DefaultSystemPrompt is used.
	SystemPrompt string
	// MaxTokens caps the output length per turn. Defaults to 2048.
	MaxTokens int
	// Temperature is optional; nil means provider default.
	Temperature *float64
	// Tools is the set of dispatcher tools the model may call during
	// this session. If nil, chat runs in text-only mode.
	Tools []provider.ToolDef
	// MaxTurns bounds the per-Send tool-use loop. Defaults to 6: plenty
	// of room for dispatch → tool_result → follow-up → done, while
	// still bounding runaway loops. Chat turns are short so this is a
	// soft cap; exceeding it returns a context-continue error and the
	// user can type again.
	MaxTurns int
}

// Session holds the conversation history and dispatches turns through a
// provider. Send is NOT safe for concurrent calls — the caller must
// serialize them because tool_use/tool_result pair integrity matters.
type Session struct {
	cfg      Config
	provider provider.Provider

	mu       sync.Mutex
	messages []provider.ChatMessage
}

// NewSession constructs a chat Session. The provider must be non-nil.
// Model is required; SystemPrompt defaults to DefaultSystemPrompt.
func NewSession(p provider.Provider, cfg Config) (*Session, error) {
	if p == nil {
		return nil, errors.New("chat: provider is nil")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("chat: model is required")
	}
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = DefaultSystemPrompt
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 2048
	}
	if cfg.MaxTurns == 0 {
		cfg.MaxTurns = 6
	}
	return &Session{cfg: cfg, provider: p}, nil
}

// OnDelta is called for each incremental text chunk streamed from the
// model. Implementations should return quickly; blocking stalls the
// stream reader.
type OnDelta func(delta string)

// OnDispatch is called when the model emits a dispatcher tool call. It
// runs the corresponding Stoke command (the caller decides how) and
// returns a short human-readable result that becomes the tool_result
// content on the next turn. Returning an error is also fine; the error
// message is sent back as the tool_result with is_error=true so the
// model can surface the failure to the user.
//
// The name argument is the dispatcher tool name (e.g. "dispatch_scope").
// The input argument is the raw JSON the model produced — callers
// typically pull out a "description" field.
type OnDispatch func(ctx context.Context, name string, input json.RawMessage) (string, error)

// Result summarizes the outcome of a Send call beyond the streamed
// reply text. It tells the UI what happened so the REPL can paint
// "▸ Dispatched to /build" banners or prompt the user to confirm.
type Result struct {
	// Text is the final assistant text (after all tool turns).
	Text string
	// DispatchedTools lists each dispatcher tool the model invoked
	// during this Send, in order. Empty if nothing dispatched.
	DispatchedTools []DispatchRecord
	// Turns is the number of provider API calls made.
	Turns int
}

// DispatchRecord is one tool invocation during a Send turn.
type DispatchRecord struct {
	Name   string          // tool name, e.g. "dispatch_scope"
	Input  json.RawMessage // raw JSON arguments the model emitted
	Result string          // the tool handler's return value
	Err    error           // non-nil if the handler failed
}

// Send appends userText to the conversation, runs the model-tool-use
// loop until the model stops requesting tools, and returns the result.
// Streaming text is delivered via onDelta. Dispatcher tools are routed
// through onDispatch (if nil, the session runs text-only).
func (s *Session) Send(ctx context.Context, userText string, onDelta OnDelta, onDispatch OnDispatch) (*Result, error) {
	if strings.TrimSpace(userText) == "" {
		return nil, errors.New("chat: user text is empty")
	}

	userMsg, err := newUserTextMessage(userText)
	if err != nil {
		return nil, fmt.Errorf("chat: build user message: %w", err)
	}

	// We operate on a working copy of history so a mid-turn error
	// leaves the canonical history untouched.
	s.mu.Lock()
	working := make([]provider.ChatMessage, 0, len(s.messages)+4)
	working = append(working, s.messages...)
	s.mu.Unlock()
	working = append(working, userMsg)

	result := &Result{}

	for turn := 0; turn < s.cfg.MaxTurns; turn++ {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		req := provider.ChatRequest{
			Model:     s.cfg.Model,
			System:    s.cfg.SystemPrompt,
			Messages:  working,
			MaxTokens: s.cfg.MaxTokens,
		}
		if s.cfg.Temperature != nil {
			t := *s.cfg.Temperature
			req.Temperature = &t
		}
		// Only advertise tools if the caller wired an onDispatch
		// handler AND configured tool schemas. Text-only mode skips
		// this entirely so the model cannot emit a dispatch that
		// nothing will execute.
		if onDispatch != nil && len(s.cfg.Tools) > 0 {
			req.Tools = s.cfg.Tools
		}

		// Stream this turn.
		var textBuf strings.Builder
		onEvent := func(ev stream.Event) {
			if ev.DeltaText == "" {
				return
			}
			textBuf.WriteString(ev.DeltaText)
			if onDelta != nil {
				onDelta(ev.DeltaText)
			}
		}

		// provider.ChatStream is synchronous and can block on a slow
		// upstream. Run it in a goroutine so we can honor ctx
		// cancellation promptly — otherwise Ctrl+C during a long
		// chat turn would hang the REPL until the HTTP request
		// eventually finished. The HTTP call itself is NOT canceled
		// (its own client has its own timeout), but control returns
		// to the caller right away.
		type streamResult struct {
			resp *provider.ChatResponse
			err  error
		}
		resultCh := make(chan streamResult, 1)
		go func() {
			resp, err := s.provider.ChatStream(req, onEvent)
			resultCh <- streamResult{resp: resp, err: err}
		}()

		var resp *provider.ChatResponse
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case r := <-resultCh:
			resp = r.resp
			if r.err != nil {
				result.Turns++
				return result, fmt.Errorf("chat: turn %d: %w", turn, r.err)
			}
		}
		result.Turns++

		// Build the assistant message block list so we can append it
		// to history and then match up any tool_use blocks with tool
		// results on the next turn.
		assistantText := textBuf.String()
		if assistantText == "" && resp != nil {
			// Streaming produced nothing; fall back to the final
			// assembled text. Replay to onDelta so the UI catches up.
			assistantText = firstTextBlock(resp.Content)
			if assistantText != "" && onDelta != nil {
				onDelta(assistantText)
			}
		}

		var toolUses []provider.ResponseContent
		if resp != nil {
			for _, c := range resp.Content {
				if c.Type == "tool_use" {
					toolUses = append(toolUses, c)
				}
			}
		}

		assistantMsg, err := newAssistantContentMessage(assistantText, toolUses)
		if err != nil {
			return result, fmt.Errorf("chat: build assistant message: %w", err)
		}
		working = append(working, assistantMsg)

		// If no tools were requested, the turn is complete.
		if len(toolUses) == 0 {
			result.Text = assistantText
			// Persist history only on a clean (final) turn. This is
			// conservative: a mid-loop crash leaves the canonical
			// history stable so the user can retry.
			s.commit(working)
			return result, nil
		}

		// Execute tool calls in order. Parallel dispatch would be
		// nice but complicates the UI ("which one is running?") so
		// keep it serial for now.
		toolResults := make([]toolResultBlock, 0, len(toolUses))
		for _, tu := range toolUses {
			rec := DispatchRecord{Name: tu.Name}
			rawInput, _ := json.Marshal(tu.Input)
			rec.Input = rawInput

			var output string
			var toolErr error
			if onDispatch == nil {
				// Tools were advertised but the caller didn't wire a
				// handler. That is a programming error; tell the
				// model so it stops trying.
				output = "No dispatcher handler is wired. Reply with plain text instead."
				toolErr = errors.New("no dispatcher handler")
			} else {
				output, toolErr = onDispatch(ctx, tu.Name, rawInput)
				if toolErr != nil {
					output = fmt.Sprintf("Error: %v", toolErr)
				}
			}
			rec.Result = output
			rec.Err = toolErr
			result.DispatchedTools = append(result.DispatchedTools, rec)

			toolResults = append(toolResults, toolResultBlock{
				Type:      "tool_result",
				ToolUseID: tu.ID,
				Content:   output,
				IsError:   toolErr != nil,
			})
		}

		// Append a user message carrying all tool_result blocks. The
		// API requires tool_use blocks in one assistant message to be
		// answered by tool_result blocks in the very next user
		// message, one per ID.
		toolResultMsg, err := newToolResultMessage(toolResults)
		if err != nil {
			return result, fmt.Errorf("chat: build tool result message: %w", err)
		}
		working = append(working, toolResultMsg)
		// Loop again so the model can summarize the dispatch result.
	}

	// Exceeded MaxTurns. Commit whatever we have and return.
	s.commit(working)
	return result, fmt.Errorf("chat: exceeded MaxTurns (%d)", s.cfg.MaxTurns)
}

// commit installs the working message list as the canonical history.
func (s *Session) commit(working []provider.ChatMessage) {
	s.mu.Lock()
	s.messages = make([]provider.ChatMessage, len(working))
	copy(s.messages, working)
	s.mu.Unlock()
}

// Reset clears the conversation history. The system prompt and config
// are preserved.
func (s *Session) Reset() {
	s.mu.Lock()
	s.messages = nil
	s.mu.Unlock()
}

// TurnCount returns the number of messages in history.
func (s *Session) TurnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

// History returns a defensive copy of the current message history.
func (s *Session) History() []provider.ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provider.ChatMessage, len(s.messages))
	copy(out, s.messages)
	return out
}

// Model returns the configured model ID.
func (s *Session) Model() string { return s.cfg.Model }

// SystemPrompt returns the system prompt in use.
func (s *Session) SystemPrompt() string { return s.cfg.SystemPrompt }

// --- internal helpers ---

// textContentBlock is a text content block inside a user or assistant
// message.
type textContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolUseBlock is an assistant content block representing a tool call
// the model wants to make. Matches the Anthropic wire format.
type toolUseBlock struct {
	Type  string      `json:"type"`
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Input interface{} `json:"input"`
}

// toolResultBlock is a user content block carrying the output of a
// previously-requested tool call.
type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// newUserTextMessage wraps plain text into the content-block JSON shape
// that provider.ChatMessage expects.
func newUserTextMessage(text string) (provider.ChatMessage, error) {
	blocks := []textContentBlock{{Type: "text", Text: text}}
	raw, err := json.Marshal(blocks)
	if err != nil {
		return provider.ChatMessage{}, err
	}
	return provider.ChatMessage{Role: "user", Content: raw}, nil
}

// newAssistantContentMessage builds an assistant message that may
// contain both a text block and one or more tool_use blocks. The order
// matches the model's original response so the API sees a faithful
// replay on the next turn.
func newAssistantContentMessage(text string, toolUses []provider.ResponseContent) (provider.ChatMessage, error) {
	// Emit a heterogeneous array: text first (if any), then tool_uses.
	// Anthropic accepts both in one assistant message.
	blocks := make([]interface{}, 0, 1+len(toolUses))
	if text != "" {
		blocks = append(blocks, textContentBlock{Type: "text", Text: text})
	}
	for _, tu := range toolUses {
		// Input may be nil for zero-arg tools; preserve as empty object.
		inp := tu.Input
		if inp == nil {
			inp = map[string]interface{}{}
		}
		blocks = append(blocks, toolUseBlock{
			Type:  "tool_use",
			ID:    tu.ID,
			Name:  tu.Name,
			Input: inp,
		})
	}
	// If we ended up with nothing (shouldn't happen in practice — the
	// model always emits at least one block), fall back to an empty
	// text block so the API doesn't reject the message shape.
	if len(blocks) == 0 {
		blocks = append(blocks, textContentBlock{Type: "text", Text: ""})
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		return provider.ChatMessage{}, err
	}
	return provider.ChatMessage{Role: "assistant", Content: raw}, nil
}

// newToolResultMessage wraps a slice of tool_result blocks into a user
// ChatMessage. The Anthropic API requires all tool_results answering a
// single assistant tool_use turn to live in one user message.
func newToolResultMessage(results []toolResultBlock) (provider.ChatMessage, error) {
	raw, err := json.Marshal(results)
	if err != nil {
		return provider.ChatMessage{}, err
	}
	return provider.ChatMessage{Role: "user", Content: raw}, nil
}

// firstTextBlock extracts the first "text" content block's text from a
// provider.ChatResponse. Used as a fallback when streaming deltas
// weren't emitted.
func firstTextBlock(content []provider.ResponseContent) string {
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			return c.Text
		}
	}
	return ""
}
