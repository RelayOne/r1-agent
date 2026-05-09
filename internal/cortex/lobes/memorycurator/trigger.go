// Trigger machinery for MemoryCuratorLobe (spec item 29).
//
// Two orthogonal trigger paths:
//
//  1. Per-Run cadence: every curatorTurnInterval (5) Run ticks fire the
//     haikuCall pipeline. Implemented in lobe.go's Run; this file
//     contains the haikuCall stub TASK-29 wires from the per-Run path.
//
//  2. Hub event: every EventTaskCompleted fires the haikuCall pipeline
//     out-of-cadence. The subscriber is installed exactly once (in
//     ensureSubscribed/subscribeImpl) and is safe with a nil hubBus.
//
// Both paths converge on fireTrigger, which observes triggerCount and
// dispatches to onTrigger. The default onTrigger is haikuCall (wired
// from the constructor on package init below); TASK-30 extends it with
// the privacy filter + auto-apply + audit-log pipeline.
package memorycurator

import (
	"context"
	"log/slog"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// taskCompletedSubscriberID is the stable subscriber identifier used by
// the EventTaskCompleted handler. hub.Bus.Register dedups by ID so
// repeated subscription registrations are idempotent.
const taskCompletedSubscriberID = "memory-curator-task-completed"

// subscribeImpl wires the EventTaskCompleted subscriber required by
// TASK-29's "task.completed" trigger arm. Safe with a nil hubBus
// (skipped). The actual ensureSubscribed gate is in lobe.go; this
// method assumes hubBus != nil.
func (l *MemoryCuratorLobe) subscribeImpl() {
	if l.hubBus == nil {
		return
	}
	l.hubBus.Register(hub.Subscriber{
		ID:     taskCompletedSubscriberID,
		Events: []hub.EventType{hub.EventTaskCompleted},
		Mode:   hub.ModeObserve,
		Handler: func(ctx context.Context, ev *hub.Event) *hub.HookResponse {
			l.handleTaskCompleted(ctx, ev)
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})
}

// handleTaskCompleted is the EventTaskCompleted subscriber body. It
// converts the event into a synthetic LobeInput (carrying any history
// the publisher attached under ev.Custom["history"]) and calls
// fireTrigger directly — task.completed is an out-of-cadence trigger
// not gated by the every-5-turns predicate.
//
// Slot acquisition: this path runs in the bus's goroutine and bypasses
// LobeRunner.runOnce, so the runner-level Acquire never fires. The
// fireTrigger invocation is wrapped in a per-call MustAcquire/release
// pair so the slot cap is honored. The cadence path (Run -> fireTrigger)
// is gated by the runner's outer Acquire instead, so this is the only
// place per-call gating is needed. A nil semaphore is a no-op (legacy
// tests). See internal/cortex/lobes/llm/slot.go.
func (l *MemoryCuratorLobe) handleTaskCompleted(ctx context.Context, ev *hub.Event) {
	if ev == nil {
		return
	}
	history, _ := ev.Custom["history"].([]agentloop.Message)
	in := cortex.LobeInput{
		History: history,
		Bus:     l.hubBus,
	}

	release, err := llm.MustAcquire(ctx, l.semaphore)
	if err != nil {
		// ctx cancelled or deadline before a slot was free; drop the
		// trigger silently — the next cadence tick or event fires
		// its own.
		return
	}
	defer release()

	l.fireTrigger(ctx, in)
}

// haikuCall executes one Haiku request for the supplied LobeInput. The
// last curatorRecentN messages from in.History are rendered into the
// user-message preamble; the rememberTool is sent as the only tool;
// the verbatim curatorSystemPrompt is the system prompt.
//
// Slot acquisition: this method does NOT take the LLM slot itself —
// the slot is acquired by the caller. Two paths reach haikuCall:
//
//  1. The cadence path (Run -> fireTrigger -> onTrigger ->
//     defaultOnTrigger -> haikuCall) runs inside
//     LobeRunner.runOnce, which already wraps the call in the
//     runner-level Acquire/Release for KindLLM Lobes.
//
//  2. The task.completed event path (handleTaskCompleted ->
//     fireTrigger -> ...) runs in the bus's goroutine. That caller
//     wraps fireTrigger in its own per-call MustAcquire/release so
//     the slot cap is honored without double-acquiring on path 1.
//
// Returns the raw assistant content blocks. defaultOnTrigger (in
// curate.go) consumes them: parses the tool_use blocks, runs the
// privacy filter on candidate content, dispatches each to either
// the auto-apply path (memory.Store.Save + audit-log append +
// memory Note publish) or the confirm-queue path (publish a
// memory-confirm Note for the main thread to surface).
//
// Errors are logged at warn level and otherwise swallowed — the next
// trigger fires on its own cadence.
func (l *MemoryCuratorLobe) haikuCall(ctx context.Context, in cortex.LobeInput) []provider.ResponseContent {
	if l.client == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return nil
	}

	tail := tailMessages(in.History, curatorRecentN)
	userMsg := renderUserPreamble(tail)

	pb := llm.LobePromptBuilder{
		Model:        curatorModel,
		SystemPrompt: curatorSystemPrompt,
		Tools:        []provider.ToolDef{rememberTool},
		MaxTokens:    curatorMaxTokens,
	}
	req := pb.Build(userMsg, nil)

	resp, err := l.client.ChatStream(req, nil)
	if err != nil {
		slog.Warn("memory-curator: chat stream failed", "err", err, "lobe", l.ID())
		return nil
	}
	if resp == nil {
		return nil
	}
	return resp.Content
}

// tailMessages returns the trailing window of agentloop.Messages from
// history. Returns the input unchanged when len(history) <= n. Always
// safe with a nil/empty history.
func tailMessages(history []agentloop.Message, n int) []agentloop.Message {
	if n <= 0 || len(history) == 0 {
		return nil
	}
	if len(history) <= n {
		out := make([]agentloop.Message, len(history))
		copy(out, history)
		return out
	}
	out := make([]agentloop.Message, n)
	copy(out, history[len(history)-n:])
	return out
}

// renderUserPreamble formats the message tail as a plain-text transcript
// the model can reason over. Each message is prefixed with a short role
// tag (USER:/ASSISTANT:) so the model can disambiguate without
// structured input.
//
// Privacy-sensitive content is NOT redacted here — the privacy filter
// in TASK-30 either skips the entire haikuCall (if any message in the
// tail carries the "private" tag) or accepts it as-is. This rendering
// step assumes the caller has already validated privacy.
func renderUserPreamble(tail []agentloop.Message) string {
	if len(tail) == 0 {
		return "(no recent conversation)"
	}
	out := "Recent conversation tail (last " + itoa(len(tail)) + " messages):\n"
	for _, m := range tail {
		text := joinTextBlocks(m.Content)
		if text == "" {
			continue
		}
		role := "USER"
		if m.Role == "assistant" {
			role = "ASSISTANT"
		}
		out += role + ": " + text + "\n"
	}
	return out
}

// joinTextBlocks collapses a slice of agentloop.ContentBlock into a
// single concatenated string of every "text"-typed block. Other block
// types (tool_use, tool_result, thinking, ...) are skipped.
func joinTextBlocks(blocks []agentloop.ContentBlock) string {
	var out string
	for _, blk := range blocks {
		if blk.Type != "" && blk.Type != "text" {
			continue
		}
		if blk.Text == "" {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += blk.Text
	}
	return out
}

// itoa is a tiny strconv.Itoa shim so renderUserPreamble does not
// need to import strconv just for the tail count. Production callers
// would inline strconv.Itoa; we keep the dependency surface minimal.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
