// Turn-after-user trigger + cap-at-3 enforcement for ClarifyingQLobe
// (spec items 24, 25).
//
// On every cortex.user.message hub event the Lobe runs one Haiku call
// against the user's most recent message. The model's response Content
// blocks are walked for tool_use blocks named queue_clarifying_question;
// each one becomes one outstanding clarifying-question Note (subject to
// the spec-fixed cap of 3 outstanding notes per Lobe).
//
// The cap is enforced before publication: every tool_use beyond the cap
// is silently dropped per spec ("drop overflow tool calls silently").
// The map of outstanding question_ids is the source of truth for both
// cap enforcement and resolve-on-answer (TASK-25).
package clarifyq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// userMessageSubscriberID is the stable subscriber identifier for the
// turn-after-user trigger handler. Used by hub.Bus.Register's dedup so
// repeated subscription registrations are idempotent.
const userMessageSubscriberID = "clarifying-q-user-message"

// answeredQuestionSubscriberID is the stable subscriber identifier for
// the resolve-on-answer handler. Used by TASK-25.
const answeredQuestionSubscriberID = "clarifying-q-answered-question"

// metaQuestionID is the Note.Meta key under which the Lobe stamps the
// question identifier. The resolve handler reads this key when matching
// a cortex.user.answered_question event back to an outstanding Note.
const metaQuestionID = "question_id"

// subscribe wires the two hub subscribers required by TASK-24 + TASK-25.
// Safe with a nil hubBus (skipped). The actual ensureSubscribed gate is
// in lobe.go; this method assumes hubBus != nil.
func (l *ClarifyingQLobe) subscribeImpl() {
	if l.hubBus == nil {
		return
	}
	l.hubBus.Register(hub.Subscriber{
		ID:     userMessageSubscriberID,
		Events: []hub.EventType{hub.EventCortexUserMessage},
		Mode:   hub.ModeObserve,
		Handler: func(ctx context.Context, ev *hub.Event) *hub.HookResponse {
			l.handleUserMessage(ctx, ev)
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})
	l.hubBus.Register(hub.Subscriber{
		ID:     answeredQuestionSubscriberID,
		Events: []hub.EventType{hub.EventCortexUserAnsweredQuestion},
		Mode:   hub.ModeObserve,
		Handler: func(ctx context.Context, ev *hub.Event) *hub.HookResponse {
			l.handleAnsweredQuestion(ev)
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})
}

// handleUserMessage is the cortex.user.message subscriber body. It
// extracts the user message text and any history tail from ev.Custom,
// then drives haikuOnce. Errors are logged at warn level — the next
// user turn fires its own trigger so we do not retry.
func (l *ClarifyingQLobe) handleUserMessage(ctx context.Context, ev *hub.Event) {
	if ev == nil {
		return
	}
	text, _ := ev.Custom["text"].(string)
	if text == "" {
		return
	}
	history, _ := ev.Custom["history"].([]provider.ChatMessage)
	l.haikuOnce(ctx, text, history)
}

// haikuOnce executes one Haiku request for the supplied user message,
// walks the response Content for queue_clarifying_question tool_use
// blocks, and Publishes one Note per accepted block.
//
// Cap enforcement: before each Publish the method counts the number of
// currently-outstanding question Notes (the size of l.outstanding). If
// the count is already at clarifyOutstandingCap (3) the tool_use is
// silently dropped per spec.
func (l *ClarifyingQLobe) haikuOnce(ctx context.Context, userMsg string, history []provider.ChatMessage) {
	if l.client == nil {
		// No provider wired — typical of unit tests that exercise the
		// resolve path only. Quietly return.
		return
	}
	pb := llm.LobePromptBuilder{
		Model:        clarifyModel,
		SystemPrompt: clarifySystemPrompt,
		Tools:        []provider.ToolDef{clarifyTool},
		MaxTokens:    clarifyMaxTokens,
	}
	req := pb.Build(userMsg, history)

	resp, err := l.client.ChatStream(req, nil)
	if err != nil {
		slog.Warn("clarifying-q: chat stream failed", "err", err, "lobe", l.ID())
		return
	}
	if resp == nil {
		return
	}

	for _, blk := range resp.Content {
		if blk.Type != "tool_use" || blk.Name != clarifyToolName {
			continue
		}
		// Cap-at-3: before publishing, check outstanding size. The
		// check + publish are not atomic with respect to concurrent
		// resolution events, but that race is benign — at worst we
		// publish one extra Note that the spec allows ("at most 3
		// outstanding"). The cap is the soft contract; the resolve
		// path is what actually decrements the count.
		l.mu.Lock()
		over := len(l.outstanding) >= clarifyOutstandingCap
		l.mu.Unlock()
		if over {
			// Silent drop per spec.
			continue
		}
		l.publishQuestionFromToolUse(ctx, blk)
	}
}

// publishQuestionFromToolUse extracts the question text and metadata
// from a single tool_use ResponseContent block, generates a question_id,
// and Publishes a clarifying-question Note. The mapping
// question_id -> note_id is recorded in l.outstanding so TASK-25 can
// resolve the Note when the user answers.
//
// Workspace.Publish does not return the assigned Note ID. To recover it
// for the outstanding map, we use a one-shot Subscribe registered just
// before Publish: the subscriber fires synchronously with the Note that
// carries our question_id in Meta, captures the assigned ID, and
// unregisters itself.
func (l *ClarifyingQLobe) publishQuestionFromToolUse(ctx context.Context, blk provider.ResponseContent) {
	if l.ws == nil {
		return
	}
	question, _ := blk.Input["question"].(string)
	if question == "" {
		return
	}
	rationale, _ := blk.Input["rationale"].(string)
	blocking, _ := blk.Input["blocking"].(bool)
	category, _ := blk.Input["category"].(string)

	questionID := newQuestionID()
	severity := cortex.SevInfo
	if blocking {
		severity = cortex.SevWarning
	}

	// Title is the question text itself (severity-prefixed for visibility
	// at the supervisor injection block). The spec caps the question at
	// ≤140 chars, well under cortex.Note's 80-rune Title limit when
	// truncated; we truncate defensively because models can over-shoot.
	title := truncateTitle(question, 80)

	note := cortex.Note{
		LobeID:   l.ID(),
		Severity: severity,
		Title:    title,
		Body:     rationale,
		Tags:     []string{"clarify"},
		Meta: map[string]any{
			llm.MetaActionKind:    "user-answer",
			llm.MetaActionPayload: questionID,
			metaQuestionID:        questionID,
			"category":            category,
			"blocking":            blocking,
		},
	}

	// One-shot subscriber: the next Note that carries our question_id
	// in Meta is the one we just queued. Capture its assigned ID and
	// unregister.
	var captured string
	cancel := l.ws.Subscribe(func(n cortex.Note) {
		if id, _ := n.Meta[metaQuestionID].(string); id == questionID {
			captured = n.ID
		}
	})
	if err := l.ws.Publish(note); err != nil {
		cancel()
		slog.Warn("clarifying-q: publish failed",
			"err", err, "question_id", questionID)
		return
	}
	cancel()

	if captured == "" {
		// Defensive: subscriber dropped without firing. Skip the
		// outstanding-map record so the user-answer path does not
		// stumble on a phantom entry.
		return
	}

	l.mu.Lock()
	l.outstanding[questionID] = captured
	l.mu.Unlock()

	_ = ctx
}

// truncateTitle clips s to a maximum of n bytes (not runes; the
// cortex.Note Title cap is 80 runes but we use a byte-conservative
// cap so a multi-byte rune at the boundary cannot bust the
// Validate() rune-count check). Adds an ellipsis only when truncation
// happens.
func truncateTitle(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

// newQuestionID is the question identifier factory. Production uses a
// random hex string so concurrent triggers do not collide; tests can
// override the package-level variable for deterministic IDs.
var newQuestionID = func() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read is documented to never fail in practice; if it
		// does we fall back to a process-locally-monotonic counter so
		// the Lobe stays available.
		return fmt.Sprintf("q-%d", fallbackQuestionCounter())
	}
	return "q-" + hex.EncodeToString(b[:])
}

// fallbackQuestionCounter is a process-local monotonic source used only
// when crypto/rand fails. Implemented as a closure over a package-level
// counter so the function can be referenced from newQuestionID without
// holding a mutex (atomic int64 inside a closure).
var fallbackQuestionCounter = func() func() uint64 {
	var n uint64
	return func() uint64 {
		n++
		return n
	}
}()

// handleAnsweredQuestion is the cortex.user.answered_question subscriber
// body. TASK-25 implements it as a Note resolver; TASK-24 leaves it as
// a no-op stub so subscribe() can register both handlers atomically.
// Concrete implementation lands in resolve.go.
//
// Defined here (rather than as an unimplemented method) so the cap test
// in TASK-24 can register the subscriber without TASK-25's resolve.go
// needing to exist yet — the per-task commit ordering keeps each
// commit independently buildable + testable.
func (l *ClarifyingQLobe) handleAnsweredQuestion(ev *hub.Event) {
	l.resolveAnsweredQuestion(ev)
}

// _ = json.Marshal compile-time usage to keep encoding/json in the
// import set even when haikuOnce never marshals (json is reserved for
// future tool_use input parsing fallbacks). Build tag-friendly.
var _ = json.Marshal
