// Package plan — clarification round-trip.
//
// WHY THIS EXISTS
//
// A dispatched worker that encounters genuine ambiguity in its SOW (say,
// "use OAuth" — 2.0 or 1.0a?) today has two bad options: guess (and
// sometimes guess wrong, producing bugs the reviewer has to undo) or
// quietly abandon the task without saying what it actually needed. Both
// paths waste a repair cycle.
//
// The clarification round-trip gives the worker a dedicated, deterministic
// channel to pause and ask. It lands in one of two places:
//
//   - CHAT MODE   — the question is surfaced to the user, who answers
//                   in natural language. The chat UI blocks until the
//                   user responds (bounded by a timeout) and feeds the
//                   answer back into the worker's tool-use loop.
//   - HEADLESS    — the question is answered by a supervisor LLM
//                   synthesized from the SOW + codebase. The supervisor
//                   is constrained to answer ONLY from the written
//                   material and must return "UNKNOWN — escalate" when
//                   the SOW does not speak to the ambiguity.
//
// Either way the abandon path is a first-class outcome. The worker is
// instructed (by the tool description and by the supervisor prompt) that
// "UNKNOWN — escalate" means: stop guessing, leave this task unfinished,
// and document the ambiguity in your completion summary. That is always
// preferred to silent fabrication.
package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/RelayOne/r1/internal/provider"
)

// ClarifyUnknownAnswer is the sentinel string the worker sees when the
// responder cannot answer the question from the SOW or the user does
// not respond in time. Workers MUST treat this as a signal to abandon
// the task rather than fall back to guessing.
const ClarifyUnknownAnswer = "UNKNOWN — escalate"

// ClarifyRequest is what a worker emits when it cannot proceed without
// a human (or supervisor-LLM) answer. Short and scoped — NOT a catch-
// all "I'm stuck." The worker must cite the ambiguity explicitly.
type ClarifyRequest struct {
	// Question is the specific question the worker needs answered.
	// One sentence, ending in a question mark. MUST cite the
	// ambiguity: "The SOW says 'use OAuth' — OAuth 2.0 or OAuth 1.0a?"
	Question string `json:"question"`

	// Context is the short excerpt of code/spec around the ambiguity
	// — what the worker was reading when it got stuck. Max 800 chars
	// enforced by the responder.
	Context string `json:"context,omitempty"`

	// Options is the worker's proposed candidates. When populated, the
	// responder can pick one by index instead of free-form answering.
	// 2-5 entries.
	Options []string `json:"options,omitempty"`

	// TaskID is the task identifier of the worker asking. Passed
	// through for routing and for log attribution.
	TaskID string `json:"task_id"`
}

// ClarifyAnswer is the response back to a paused worker.
type ClarifyAnswer struct {
	// Answer is the free-form text response. If the responder picked
	// an option by index, the engine fills this with Options[Index].
	Answer string `json:"answer"`

	// SelectedOption is the zero-based index of the Options entry
	// picked, or -1 for free-form answers. Populated to help the
	// worker reason about structured choices.
	SelectedOption int `json:"selected_option"`

	// Source is "user" (chat mode), "supervisor-llm" (headless
	// supervisor), or "none" (no responder configured — immediate
	// UNKNOWN). The worker may weight confidence differently.
	Source string `json:"source"`
}

// ClarifyResponder produces an answer to a ClarifyRequest. Chat mode
// wires a user-prompting implementation; headless mode wires a
// SupervisorResponder. Both implementations must honor ctx cancellation.
type ClarifyResponder interface {
	// Respond returns an answer for the given request. Implementations
	// must never return nil answer without an error. To signal "I
	// cannot help", set Answer to ClarifyUnknownAnswer.
	Respond(ctx context.Context, req ClarifyRequest) (*ClarifyAnswer, error)
}

// MaxClarificationsPerTask caps how many times a single worker can
// invoke request_clarification. The 4th and subsequent calls return a
// rate-limit answer immediately, with no responder invocation. Three is
// enough for legitimate multi-step ambiguity but low enough to catch
// workers abusing the channel.
const MaxClarificationsPerTask = 3

// ClarifyRateLimitAnswer is injected when a worker exceeds
// MaxClarificationsPerTask. Phrased so the model knows the limit and
// what to do next.
const ClarifyRateLimitAnswer = "Clarification limit reached — make your best judgment or abandon this task, citing the ambiguity in your final message."

// ClarifyCounter is a per-task counter used by the engine to enforce
// MaxClarificationsPerTask. Safe for concurrent use.
type ClarifyCounter struct{ n int64 }

// Inc atomically increments and returns the new count.
func (c *ClarifyCounter) Inc() int64 {
	if c == nil {
		return 0
	}
	return atomic.AddInt64(&c.n, 1)
}

// Count returns the current count.
func (c *ClarifyCounter) Count() int64 {
	if c == nil {
		return 0
	}
	return atomic.LoadInt64(&c.n)
}

// NoopResponder is the fallback used when neither a chat responder nor
// a supervisor provider is configured. It immediately returns an
// UNKNOWN answer with Source="none" so the worker abandons rather than
// guessing.
type NoopResponder struct{}

// Respond implements ClarifyResponder. Always returns UNKNOWN.
func (NoopResponder) Respond(ctx context.Context, req ClarifyRequest) (*ClarifyAnswer, error) {
	return &ClarifyAnswer{
		Answer:         ClarifyUnknownAnswer,
		SelectedOption: -1,
		Source:         "none",
	}, nil
}

// SupervisorResponder synthesizes a ClarifyAnswer from the SOW and
// codebase. Runs a one-shot Chat call with a tight system prompt that
// authorizes it to answer on the operator's behalf ONLY when the SOW
// speaks to the ambiguity.
//
// The responder is deliberately single-turn: it does not invoke tools,
// it only reads the SOW excerpt provided at construction time. For
// questions whose answer depends on running code or reading files the
// worker did not include in its Context, the supervisor should return
// UNKNOWN and let the worker abandon.
type SupervisorResponder struct {
	// Provider is the LLM used for the one-shot answer. When nil,
	// Respond returns ClarifyUnknownAnswer with Source="none".
	Provider provider.Provider
	// Model is the model ID (e.g. "claude-sonnet-4-6"). Required
	// when Provider is non-nil.
	Model string
	// RepoRoot is the absolute path of the project being built.
	// Included in the prompt for grounding but the supervisor does
	// not read files — the worker's Context block is the only
	// authority.
	RepoRoot string
	// RawSOW is the SOW text as the operator wrote it. The supervisor
	// is explicitly instructed that this is the source of truth and
	// that fabrication is forbidden.
	RawSOW string
}

const supervisorSystemPrompt = `You are a supervisor answering a worker's clarification request.

Rules you MUST follow:
  1. Answer ONLY from the SOW text and the worker's quoted context. Do
     not invent facts, libraries, versions, identifiers, or design
     decisions. If the SOW does not address the ambiguity, you MUST
     return "UNKNOWN — escalate" verbatim as your answer.
  2. Be concise. One to three sentences. No preamble, no apology.
  3. When the worker provided Options and exactly one is supported by
     the SOW, pick that option by index. When none are supported,
     return "UNKNOWN — escalate". When multiple are equally supported,
     pick the most conservative one and say so.
  4. You do not have filesystem or tool access. If the answer requires
     reading code the worker did not quote, return "UNKNOWN — escalate".

Return JSON ONLY, matching this schema:
{
  "answer":           "<your one-to-three-sentence answer>",
  "selected_option":  <0-based index of the Options picked, or -1>,
  "reasoning":        "<optional: where in the SOW you found the answer>"
}`

// supervisorResponseShape is the JSON shape the supervisor's Chat call
// must produce. Parsing is defensive — jsonutil handles mixed prose/
// JSON.
type supervisorResponseShape struct {
	Answer         string `json:"answer"`
	SelectedOption int    `json:"selected_option"`
	Reasoning      string `json:"reasoning,omitempty"`
}

// Respond implements ClarifyResponder.
func (r *SupervisorResponder) Respond(ctx context.Context, req ClarifyRequest) (*ClarifyAnswer, error) {
	if r == nil || r.Provider == nil {
		return &ClarifyAnswer{
			Answer:         ClarifyUnknownAnswer,
			SelectedOption: -1,
			Source:         "none",
		}, nil
	}
	if strings.TrimSpace(req.Question) == "" {
		return nil, errors.New("clarify: empty question")
	}

	// Enforce Context length ceiling so a worker cannot blow the
	// supervisor's context window with a runaway excerpt.
	ctxExcerpt := req.Context
	if len(ctxExcerpt) > 800 {
		ctxExcerpt = ctxExcerpt[:800] + "... (truncated)"
	}

	var userBuf strings.Builder
	fmt.Fprintf(&userBuf, "TASK ID: %s\n\n", req.TaskID)
	fmt.Fprintf(&userBuf, "QUESTION:\n%s\n\n", strings.TrimSpace(req.Question))
	if len(req.Options) > 0 {
		userBuf.WriteString("OPTIONS:\n")
		for i, opt := range req.Options {
			fmt.Fprintf(&userBuf, "  %d. %s\n", i, opt)
		}
		userBuf.WriteString("\n")
	}
	if ctxExcerpt != "" {
		fmt.Fprintf(&userBuf, "WORKER CONTEXT EXCERPT:\n%s\n\n", ctxExcerpt)
	}
	if r.RawSOW != "" {
		fmt.Fprintf(&userBuf, "SOW (verbatim, authoritative):\n%s\n", r.RawSOW)
	}

	userMsgRaw, _ := json.Marshal([]map[string]string{{"type": "text", "text": userBuf.String()}})

	chatReq := provider.ChatRequest{
		Model:     r.Model,
		System:    supervisorSystemPrompt,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userMsgRaw}},
		MaxTokens: 2000,
	}

	// Honor ctx cancellation by running the call in a goroutine.
	type chatOut struct {
		resp *provider.ChatResponse
		err  error
	}
	ch := make(chan chatOut, 1)
	go func() {
		resp, err := r.Provider.Chat(chatReq)
		ch <- chatOut{resp: resp, err: err}
	}()

	var resp *provider.ChatResponse
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case o := <-ch:
		if o.err != nil {
			return nil, fmt.Errorf("clarify supervisor: %w", o.err)
		}
		resp = o.resp
	}

	raw := supervisorRawText(resp)
	if strings.TrimSpace(raw) == "" {
		return &ClarifyAnswer{
			Answer:         ClarifyUnknownAnswer,
			SelectedOption: -1,
			Source:         "supervisor-llm",
		}, nil
	}

	shape := parseSupervisorJSON(raw)
	answer := strings.TrimSpace(shape.Answer)
	if answer == "" || strings.EqualFold(answer, "unknown") {
		answer = ClarifyUnknownAnswer
	}

	sel := shape.SelectedOption
	if sel < -1 || sel >= len(req.Options) {
		sel = -1
	}
	// If the supervisor picked an option, make sure the Answer field
	// echoes the option text so the worker doesn't have to cross-
	// reference indices.
	if sel >= 0 && sel < len(req.Options) {
		answer = req.Options[sel]
	}

	return &ClarifyAnswer{
		Answer:         answer,
		SelectedOption: sel,
		Source:         "supervisor-llm",
	}, nil
}

// supervisorRawText pulls the first text/thinking block's text from the
// response. Falls back to thinking when the model emitted only a
// reasoning block (rare but possible on extended-thinking configs).
func supervisorRawText(resp *provider.ChatResponse) string {
	if resp == nil {
		return ""
	}
	for _, c := range resp.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			return c.Text
		}
	}
	for _, c := range resp.Content {
		if strings.TrimSpace(c.Thinking) != "" {
			return c.Thinking
		}
	}
	return ""
}

// parseSupervisorJSON tolerantly extracts the supervisor JSON shape
// from raw text. Missing or malformed input yields an empty shape with
// SelectedOption=-1 so the caller falls through to UNKNOWN.
func parseSupervisorJSON(raw string) supervisorResponseShape {
	out := supervisorResponseShape{SelectedOption: -1}

	// Fast path: the whole blob is valid JSON.
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		return out
	}

	// Slow path: the model wrapped JSON in prose. Find the first
	// {...} balanced block and try again.
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(raw[start:end+1]), &out); err == nil {
			return out
		}
	}

	// Last resort: treat the whole raw text as the answer.
	out.Answer = strings.TrimSpace(raw)
	out.SelectedOption = -1
	return out
}

// ClarifyLogEntry is a compact record of one clarify round-trip. Run
// orchestrators write these to their event stream so the operator can
// see after-run what ambiguities the workers encountered.
type ClarifyLogEntry struct {
	TaskID   string   `json:"task_id"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Answer   string   `json:"answer"`
	Source   string   `json:"source"`
	Selected int      `json:"selected_option"`
}

// FormatClarifyForLog renders a one-line summary of a clarify round-trip
// for console output. Kept short so it doesn't drown the build log.
func FormatClarifyForLog(entry ClarifyLogEntry) string {
	q := strings.ReplaceAll(strings.TrimSpace(entry.Question), "\n", " ")
	if len(q) > 120 {
		q = q[:120] + "…"
	}
	a := strings.ReplaceAll(strings.TrimSpace(entry.Answer), "\n", " ")
	if len(a) > 120 {
		a = a[:120] + "…"
	}
	return fmt.Sprintf("    ❔ clarify[%s→%s]: %q → %q", entry.TaskID, entry.Source, q, a)
}
