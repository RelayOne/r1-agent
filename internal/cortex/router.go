package cortex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// DefaultRouterSystemPrompt is the verbatim system prompt used by the
// Router for mid-turn user-input routing decisions. Reproduced exactly
// from specs/cortex-core.md §"The Router". It is referenced as a
// cache-stable constant so the same byte stream is sent every turn.
const DefaultRouterSystemPrompt = `You are r1's mid-turn input router. The user has typed a message while the
main agent is in the middle of a turn. Your only job is to decide how that
message should be handled by calling EXACTLY ONE of the four tools below.

Decision rubric:

- Use ` + "`interrupt`" + ` ONLY when the new input contradicts, retracts, or makes
  the in-flight work unsafe (e.g. "stop", "wait that's wrong", "actually
  use Postgres not MySQL"). Interrupt cancels the live API stream and
  drops partial work.
- Use ` + "`steer`" + ` when the new input adds context, clarifies, or nudges
  direction without invalidating in-flight work (e.g. "also add a tests
  folder", "make sure it's typed strictly"). Steer attaches a soft note
  the main agent reads at the next safe boundary.
- Use ` + "`queue_mission`" + ` when the new input is a fully separate task that
  should run after the current one (e.g. "after this, also fix the bug
  in auth.go"). Queue does not affect the in-flight turn at all.
- Use ` + "`just_chat`" + ` when the new input is conversational — a question to
  YOU (the router) about what's happening, an acknowledgement, a thank-
  you, etc. Just_chat surfaces a short reply in the UI without touching
  the main agent.

Hard rules:
1. You MUST call exactly one tool. Do not emit text before or after.
2. Bias toward ` + "`steer`" + ` when in doubt — interrupt is destructive.
3. If the user says any of {"stop", "cancel", "abort", "halt"} alone or
   as the first word, you MUST use ` + "`interrupt`" + `.
4. Never invoke a tool you were not given.
`

// routerTools is the verbatim 4-tool schema given to the Router model.
// The JSON schemas mirror specs/cortex-core.md §"Tool schemas" exactly;
// the Anthropic Messages API consumes input_schema as a JSON Schema doc.
var routerTools = []provider.ToolDef{
	{
		Name:        "interrupt",
		Description: "Cancel the in-flight turn and inject a synthetic user message. Use only for retractions, hard stops, or contradictions.",
		InputSchema: json.RawMessage(`{
      "type": "object",
      "properties": {
        "reason":   {"type": "string", "description": "≤200 chars, why interrupting"},
        "new_direction": {"type": "string", "description": "the synthetic-user-message body the main agent will see on resume"}
      },
      "required": ["reason", "new_direction"]
    }`),
	},
	{
		Name:        "steer",
		Description: "Attach a soft note the main agent will read at the next safe boundary. Use for clarifications, additions, nudges.",
		InputSchema: json.RawMessage(`{
      "type": "object",
      "properties": {
        "severity": {"type": "string", "enum": ["info", "advice", "warning"], "description": "soft-note priority; 'critical' is reserved for system Lobes"},
        "title":    {"type": "string", "description": "≤80 chars"},
        "body":     {"type": "string", "description": "free-form markdown"}
      },
      "required": ["severity", "title", "body"]
    }`),
	},
	{
		Name:        "queue_mission",
		Description: "Enqueue a separate task to run after the current one. Does not affect the in-flight turn.",
		InputSchema: json.RawMessage(`{
      "type": "object",
      "properties": {
        "brief":    {"type": "string", "description": "the task brief; ≤2000 chars"},
        "priority": {"type": "string", "enum": ["low", "normal", "high"], "default": "normal"}
      },
      "required": ["brief"]
    }`),
	},
	{
		Name:        "just_chat",
		Description: "Conversational reply. Does not touch the main agent. Used for status questions, acknowledgements, off-topic.",
		InputSchema: json.RawMessage(`{
      "type": "object",
      "properties": {
        "reply": {"type": "string", "description": "a short conversational reply, ≤400 chars"}
      },
      "required": ["reply"]
    }`),
	},
}

// DecisionKind enumerates the four routing outcomes a Router may return.
type DecisionKind string

const (
	// DecisionInterrupt cancels the live API stream and feeds a
	// synthetic user message back into the next turn.
	DecisionInterrupt DecisionKind = "interrupt"
	// DecisionSteer attaches a soft Note the main agent reads at the
	// next safe boundary; the in-flight turn keeps running.
	DecisionSteer DecisionKind = "steer"
	// DecisionQueueMission queues a separate task to run after the
	// current one. Does not affect the in-flight turn.
	DecisionQueueMission DecisionKind = "queue_mission"
	// DecisionJustChat surfaces a conversational reply in the UI; the
	// main agent is unaffected.
	DecisionJustChat DecisionKind = "just_chat"
)

// InterruptPayload carries the parameters for a DecisionInterrupt. The
// fields mirror the broader cortex interrupt protocol (TASK-18); Router
// populates Reason and NewDirection from the model's tool call. Source
// and Severity are filled by the caller (REPL) before the payload is
// fed to the drop-partial protocol.
type InterruptPayload struct {
	Source       string `json:"source,omitempty"`
	Severity     string `json:"severity,omitempty"`
	Reason       string `json:"reason"`
	NewDirection string `json:"new_direction"`
}

// SteerPayload carries the parameters for a DecisionSteer. The Router
// publishes a Note built from these fields onto the Workspace so the
// main agent reads it at the next MidturnCheckFn boundary.
type SteerPayload struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Body     string `json:"body"`
}

// QueuePayload carries the parameters for a DecisionQueueMission. The
// REPL forwards Brief + Priority to mission.Runner; cortex-core stops
// at the routing decision (see spec §"Out of scope for cortex-core").
type QueuePayload struct {
	Brief    string `json:"brief"`
	Priority string `json:"priority,omitempty"`
}

// ChatPayload carries the conversational reply for a DecisionJustChat.
// The UI surfaces Reply directly; the main agent is untouched.
type ChatPayload struct {
	Reply string `json:"reply"`
}

// RouterDecision is one of {DecisionInterrupt, DecisionSteer,
// DecisionQueueMission, DecisionJustChat}; the matching payload field
// is populated and the rest are nil. RawToolName preserves the literal
// tool name the model emitted for debugging when the kind is somehow
// ambiguous.
type RouterDecision struct {
	Kind        DecisionKind
	Interrupt   *InterruptPayload
	Steer       *SteerPayload
	Queue       *QueuePayload
	JustChat    *ChatPayload
	RawToolName string
}

// RouterConfig carries Router construction parameters. Provider is
// required; Model and MaxTokens default per the spec; SystemPrompt
// defaults to DefaultRouterSystemPrompt when blank.
type RouterConfig struct {
	Provider     provider.Provider
	Model        string
	MaxTokens    int
	SystemPrompt string
	Bus          *hub.Bus
}

// RouterInput is the per-call input snapshot the chat REPL hands to
// Router.Route. UserInput is the new mid-turn message; History is the
// last-N messages from the active conversation; Workspace is the
// current Workspace.Snapshot() so the model can summarize what's in
// flight in a single line.
type RouterInput struct {
	UserInput string
	History   []agentloop.Message
	Workspace []Note
}

// Router is the Haiku-4.5 mid-turn input handler. Construct with
// NewRouter; call Route once per stdin line.
type Router struct {
	cfg RouterConfig
	bus *hub.Bus
}

// NewRouter validates Provider is non-nil and returns a Router with
// defaults applied (Model="claude-haiku-4-5", MaxTokens=1024,
// SystemPrompt=DefaultRouterSystemPrompt). Returns an error if
// Provider is nil.
func NewRouter(cfg RouterConfig) (*Router, error) {
	if cfg.Provider == nil {
		return nil, errors.New("cortex/router: Provider is required")
	}
	if cfg.Model == "" {
		cfg.Model = "claude-haiku-4-5"
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 1024
	}
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = DefaultRouterSystemPrompt
	}
	return &Router{cfg: cfg, bus: cfg.Bus}, nil
}

// historyWindow returns the last n messages of msgs (or all of them if
// len(msgs)<=n), as a freshly allocated slice. The Router prompt only
// needs a recent snapshot — the full conversation lives on the main
// thread and is NOT replayed here (it would bust cache and waste tokens).
func historyWindow(msgs []agentloop.Message, n int) []agentloop.Message {
	if n <= 0 || len(msgs) == 0 {
		return nil
	}
	if len(msgs) <= n {
		out := make([]agentloop.Message, len(msgs))
		copy(out, msgs)
		return out
	}
	out := make([]agentloop.Message, n)
	copy(out, msgs[len(msgs)-n:])
	return out
}

// summarizeWorkspace returns a single-line synopsis of the current
// Workspace snapshot so the Router model has enough context to decide
// whether the new user input is contradicting, clarifying, or off-topic.
// Format: "<count> notes in flight: <sev1> <sev2> ..." with severities
// counted, OR "no notes in flight" if empty. Bounded length so the
// prompt stays cache-friendly.
func summarizeWorkspace(notes []Note) string {
	if len(notes) == 0 {
		return "no notes in flight"
	}
	counts := map[Severity]int{}
	for _, n := range notes {
		counts[n.Severity]++
	}
	parts := make([]string, 0, 4)
	for _, sev := range []Severity{SevCritical, SevWarning, SevAdvice, SevInfo} {
		if c := counts[sev]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, sev))
		}
	}
	return fmt.Sprintf("%d notes in flight: %s", len(notes), strings.Join(parts, ", "))
}

// buildUserMessage returns the ChatMessage that wraps the new user
// input plus a one-line workspace summary. The format keeps the Router
// prompt cache-stable (system prompt + tool defs are byte-identical
// across calls) while still feeding fresh context per call.
func buildUserMessage(in RouterInput) (provider.ChatMessage, error) {
	body := fmt.Sprintf(
		"Workspace: %s\n\nNew user message:\n%s",
		summarizeWorkspace(in.Workspace),
		in.UserInput,
	)
	contentBlocks := []map[string]any{{"type": "text", "text": body}}
	contentJSON, err := json.Marshal(contentBlocks)
	if err != nil {
		return provider.ChatMessage{}, fmt.Errorf("marshal user content: %w", err)
	}
	return provider.ChatMessage{Role: "user", Content: contentJSON}, nil
}

// historyToChat converts the last-N agentloop.Message history into the
// provider.ChatMessage shape the Anthropic adapter expects. Each
// Message's typed Content blocks are JSON-encoded as the Content field.
func historyToChat(msgs []agentloop.Message) ([]provider.ChatMessage, error) {
	out := make([]provider.ChatMessage, 0, len(msgs))
	for i, m := range msgs {
		c, err := json.Marshal(m.Content)
		if err != nil {
			return nil, fmt.Errorf("marshal history[%d]: %w", i, err)
		}
		out = append(out, provider.ChatMessage{Role: m.Role, Content: c})
	}
	return out, nil
}

// Route is the synchronous entry point. It builds the Router request
// (system prompt + 4 tools + last-10-message snapshot + new user msg),
// calls Provider.ChatStream (no streaming callback — we await the final
// response), parses the response for exactly one tool_use block,
// populates the matching RouterDecision payload, emits
// hub.EventCortexRouterDecided, and returns.
//
// Errors:
//   - 0 tool_use blocks → errors.New("cortex/router: model emitted no tool call")
//   - >1 tool_use blocks → use the first, log WARN via slog
//   - unknown tool name → wrapped error
//   - malformed tool input → wrapped error
func (r *Router) Route(ctx context.Context, in RouterInput) (*RouterDecision, error) {
	start := time.Now()

	hist := historyWindow(in.History, 10)
	chatHist, err := historyToChat(hist)
	if err != nil {
		return nil, err
	}
	userMsg, err := buildUserMessage(in)
	if err != nil {
		return nil, err
	}
	msgs := append(chatHist, userMsg)

	req := provider.ChatRequest{
		Model:     r.cfg.Model,
		System:    r.cfg.SystemPrompt,
		Messages:  msgs,
		MaxTokens: r.cfg.MaxTokens,
		Tools:     routerTools,
	}

	// Honor ctx cancellation by short-circuiting before the API call.
	// The provider interface itself does not take a ctx; if the request
	// is already cancelled, fail fast instead of issuing a wasted call.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	resp, err := r.cfg.Provider.ChatStream(req, nil)
	if err != nil {
		return nil, fmt.Errorf("cortex/router: provider.ChatStream: %w", err)
	}
	if resp == nil {
		return nil, errors.New("cortex/router: provider returned nil response")
	}

	toolUses := make([]provider.ResponseContent, 0, 1)
	for _, c := range resp.Content {
		if c.Type == "tool_use" {
			toolUses = append(toolUses, c)
		}
	}
	if len(toolUses) == 0 {
		return nil, errors.New("cortex/router: model emitted no tool call")
	}
	if len(toolUses) > 1 {
		slog.Warn("cortex/router: model emitted multiple tool calls; using first",
			"count", len(toolUses),
			"first", toolUses[0].Name,
		)
	}

	picked := toolUses[0]
	dec, err := decodeToolUse(picked)
	if err != nil {
		return nil, err
	}

	if r.bus != nil {
		r.bus.EmitAsync(&hub.Event{
			Type: hub.EventCortexRouterDecided,
			Custom: map[string]any{
				"kind":       string(dec.Kind),
				"latency_ms": time.Since(start).Milliseconds(),
			},
		})
	}
	return dec, nil
}

// decodeToolUse turns a single tool_use ResponseContent into the
// matching RouterDecision. Unknown tool names produce a wrapped error
// so callers can distinguish "model misbehaved" from "transport
// failed".
func decodeToolUse(tu provider.ResponseContent) (*RouterDecision, error) {
	dec := &RouterDecision{RawToolName: tu.Name}
	// Re-marshal Input (a map[string]any from the streaming parser) so we
	// can json.Unmarshal into the typed payload struct. This is cheap
	// (≤200 bytes typically) and avoids hand-rolled type assertions.
	raw, err := json.Marshal(tu.Input)
	if err != nil {
		return nil, fmt.Errorf("cortex/router: re-marshal tool input: %w", err)
	}
	switch tu.Name {
	case "interrupt":
		var p struct {
			Reason       string `json:"reason"`
			NewDirection string `json:"new_direction"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("cortex/router: decode interrupt: %w", err)
		}
		dec.Kind = DecisionInterrupt
		dec.Interrupt = &InterruptPayload{
			Reason:       p.Reason,
			NewDirection: p.NewDirection,
		}
	case "steer":
		var p SteerPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("cortex/router: decode steer: %w", err)
		}
		dec.Kind = DecisionSteer
		dec.Steer = &p
	case "queue_mission":
		var p QueuePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("cortex/router: decode queue_mission: %w", err)
		}
		dec.Kind = DecisionQueueMission
		dec.Queue = &p
	case "just_chat":
		var p ChatPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("cortex/router: decode just_chat: %w", err)
		}
		dec.Kind = DecisionJustChat
		dec.JustChat = &p
	default:
		return nil, fmt.Errorf("cortex/router: unknown tool name %q", tu.Name)
	}
	return dec, nil
}
