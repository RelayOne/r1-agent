// Chat-mode clarification responder.
//
// When a dispatched worker emits request_clarification, chat mode
// surfaces the question to the user and blocks the worker until they
// type an answer. Keeping the user in the loop is the whole point of
// chat mode: the operator is already at the terminal, they can make a
// quick call on a genuine ambiguity faster than any supervisor LLM can
// synthesize one from the SOW.
//
// The responder is thread-safe but NOT concurrent — only one clarify
// question can be pending at a time. If a second worker fires a
// request_clarification while the first is still waiting on the user,
// the second blocks until the first clears. This preserves the "one
// question, one answer" UX that chat mode depends on.

package chat

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1-agent/internal/plan"
)

// DefaultChatClarifyTimeout is how long the responder waits for the
// user to answer before returning UNKNOWN and letting the worker
// abandon. 5 minutes is enough for the operator to think, check the
// SOW, and type; longer than that the worker is almost certainly
// idle-blocked and should be released.
const DefaultChatClarifyTimeout = 5 * time.Minute

// ClarifyPrompter is the function a chat host provides so the responder
// can render the question+options to the user. Implementations should
// return as soon as the banner is on screen — they must NOT block
// waiting for input. User input arrives separately via
// ChatResponder.Submit.
type ClarifyPrompter func(req plan.ClarifyRequest)

// ChatResponder is the chat-mode implementation of plan.ClarifyResponder.
// It prints the worker's question via Prompter, waits for the host to
// call Submit with the user's typed answer, and returns a ClarifyAnswer
// with Source="user". On timeout, returns UNKNOWN.
//
// One ChatResponder is shared across all workers a chat-dispatched SOW
// spawns — the host wires the REPL's input line (or shell prompt) to
// Submit and that plumbing handles every worker's question in turn.
type ChatResponder struct {
	// Prompter renders the question+options to the user. Required.
	Prompter ClarifyPrompter
	// Timeout bounds how long Respond waits for a Submit call. 0 =
	// DefaultChatClarifyTimeout. Negative = no timeout (dangerous;
	// use only in tests).
	Timeout time.Duration

	// mu serializes Respond calls — only one question may be pending
	// at a time. Without this, two concurrent workers would try to
	// own stdin simultaneously and the answers would cross-wire.
	mu sync.Mutex

	// pending tracks the currently-awaiting Respond call. When nil,
	// Submit has nothing to deliver to and returns an error.
	pendingMu sync.Mutex
	pending   chan string
	pendingReq *plan.ClarifyRequest
}

// ErrNoPendingClarify is returned by Submit when no Respond call is
// currently blocked waiting for a user answer.
var ErrNoPendingClarify = errors.New("chat: no clarification is currently pending")

// Respond implements plan.ClarifyResponder. Blocks until the host
// calls Submit or the timeout expires. Safe for use by the native
// runner's tool handler.
func (r *ChatResponder) Respond(ctx context.Context, req plan.ClarifyRequest) (*plan.ClarifyAnswer, error) {
	if r == nil {
		return nil, errors.New("chat: nil ChatResponder")
	}
	if r.Prompter == nil {
		return nil, errors.New("chat: ChatResponder has no Prompter")
	}

	// Serialize: one clarify at a time.
	r.mu.Lock()
	defer r.mu.Unlock()

	// Install a fresh pending slot. Buffer 1 so Submit never blocks
	// on a healthy deliver-then-go path.
	ch := make(chan string, 1)
	r.pendingMu.Lock()
	r.pending = ch
	reqCopy := req
	r.pendingReq = &reqCopy
	r.pendingMu.Unlock()

	defer func() {
		r.pendingMu.Lock()
		r.pending = nil
		r.pendingReq = nil
		r.pendingMu.Unlock()
	}()

	// Display the question to the user.
	r.Prompter(req)

	timeout := r.Timeout
	if timeout == 0 {
		timeout = DefaultChatClarifyTimeout
	}

	var timerCh <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timerCh = t.C
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timerCh:
		// User didn't respond in time. Log and return UNKNOWN so
		// the worker abandons cleanly instead of blocking forever.
		fmt.Printf("  ⚠ clarify: user did not respond within %s — returning UNKNOWN\n", timeout)
		return &plan.ClarifyAnswer{
			Answer:         plan.ClarifyUnknownAnswer,
			SelectedOption: -1,
			Source:         "user-timeout",
		}, nil
	case raw := <-ch:
		return parseChatUserAnswer(raw, req), nil
	}
}

// Submit delivers the user's typed answer to the pending Respond call.
// Returns ErrNoPendingClarify if no worker is currently waiting.
// Typical host wiring: the REPL's input loop checks Pending() before
// treating a line as a chat message; when something is pending it
// routes the line to Submit instead of Send.
func (r *ChatResponder) Submit(answer string) error {
	r.pendingMu.Lock()
	ch := r.pending
	r.pendingMu.Unlock()
	if ch == nil {
		return ErrNoPendingClarify
	}
	select {
	case ch <- answer:
		return nil
	default:
		// Already delivered or channel closed — the previous Respond
		// won the race.
		return ErrNoPendingClarify
	}
}

// Pending reports whether a Respond call is currently awaiting input.
// Hosts check this on every REPL line to decide whether to treat the
// line as a chat message or as a clarify answer.
func (r *ChatResponder) Pending() (plan.ClarifyRequest, bool) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	if r.pendingReq == nil {
		return plan.ClarifyRequest{}, false
	}
	return *r.pendingReq, true
}

// parseChatUserAnswer converts the user's raw typed line into a
// ClarifyAnswer. Supported shapes:
//
//   - Pure integer within options range → treated as an index pick.
//   - "Option N" / "pick N" / "#N" → parsed as an index.
//   - Anything else → free-form answer with SelectedOption=-1.
//
// Blank/whitespace-only input returns UNKNOWN so the user can "pass"
// by hitting enter — the worker will abandon rather than have the
// operator fabricate a guess under pressure.
func parseChatUserAnswer(raw string, req plan.ClarifyRequest) *plan.ClarifyAnswer {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return &plan.ClarifyAnswer{
			Answer:         plan.ClarifyUnknownAnswer,
			SelectedOption: -1,
			Source:         "user",
		}
	}

	// Allow the user to pick by index when options were provided.
	if len(req.Options) > 0 {
		if idx, ok := extractOptionIndex(trimmed); ok && idx >= 0 && idx < len(req.Options) {
			return &plan.ClarifyAnswer{
				Answer:         req.Options[idx],
				SelectedOption: idx,
				Source:         "user",
			}
		}
	}

	return &plan.ClarifyAnswer{
		Answer:         trimmed,
		SelectedOption: -1,
		Source:         "user",
	}
}

// extractOptionIndex tries to pull a 0-based index out of the user's
// input. Accepts "0", "1", "#0", "option 2", "pick 3".
func extractOptionIndex(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	lower := strings.ToLower(s)
	for _, prefix := range []string{"#", "option ", "opt ", "pick ", "choose "} {
		if strings.HasPrefix(lower, prefix) {
			rest := strings.TrimSpace(lower[len(prefix):])
			if n, err := strconv.Atoi(rest); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// FormatClarifyPrompt renders a ClarifyRequest into a multi-line
// string suitable for printing to the user's terminal. Hosts can use
// it directly in their Prompter, or reformat as they see fit.
func FormatClarifyPrompt(req plan.ClarifyRequest) string {
	var b strings.Builder
	b.WriteString("\n")
	fmt.Fprintf(&b, "  ❔ stoke worker %s needs a clarification:\n", req.TaskID)
	fmt.Fprintf(&b, "     %s\n", strings.TrimSpace(req.Question))
	if strings.TrimSpace(req.Context) != "" {
		b.WriteString("\n     Context:\n")
		for _, line := range strings.Split(strings.TrimSpace(req.Context), "\n") {
			fmt.Fprintf(&b, "       %s\n", line)
		}
	}
	if len(req.Options) > 0 {
		b.WriteString("\n     Options (reply with the number or free-form text):\n")
		for i, opt := range req.Options {
			fmt.Fprintf(&b, "       %d) %s\n", i, opt)
		}
	}
	b.WriteString("\n     Type your answer (blank = UNKNOWN, let the worker abandon):\n")
	return b.String()
}
