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
	"fmt"
	"log/slog"
	"sync/atomic"

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
// question_id -> note_id is recorded in l.outstanding so the resolve
// path can match the Note when the user answers.
//
// Workspace.Publish does not return the assigned Note ID. To recover it
// for the outstanding map, we use a one-shot Subscribe registered just
// before Publish: the subscriber fires synchronously with the Note that
// carries our question_id in Meta, captures the assigned ID, and
// unregisters itself.
//
// ctx is honored at entry: a cancelled context drops the publication
// without partial state mutation (no Subscribe, no Publish, no
// outstanding-map entry).
func (l *ClarifyingQLobe) publishQuestionFromToolUse(ctx context.Context, blk provider.ResponseContent) {
	if l.ws == nil {
		return
	}
	if err := ctx.Err(); err != nil {
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
}

// truncateTitle clips s so its rune count does not exceed n. cortex.Note
// Validate enforces a Title cap of 80 runes (not bytes); this helper
// counts runes and slices on rune boundaries so a multi-byte glyph at
// the boundary does not produce an invalid Title or trip Validate.
// Adds an ellipsis only when truncation happens.
func truncateTitle(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-3]) + "..."
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
// when crypto/rand fails. Backed by sync/atomic so concurrent triggers
// (multiple haikuOnce calls overlapping a transient rand.Read failure)
// cannot collide on the same fallback ID.
var fallbackQuestionCounter = func() func() uint64 {
	var n atomic.Uint64
	return func() uint64 {
		return n.Add(1)
	}
}()

// handleAnsweredQuestion is the cortex.user.answered_question subscriber
// body. The concrete resolution logic lives in resolve.go
// (resolveAnsweredQuestion); this thin wrapper keeps the subscriber
// registration site (subscribeImpl) independent from the resolution
// implementation, so changes to one do not perturb the other.
func (l *ClarifyingQLobe) handleAnsweredQuestion(ev *hub.Event) {
	l.resolveAnsweredQuestion(ev)
}
