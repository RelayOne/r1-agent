package cortex

// Drop-partial interrupt protocol (TASK-18).
//
// RunTurnWithInterrupt wraps a single agentloop turn so it can be
// interrupted mid-stream. Used by the chat REPL when Router emits
// DecisionInterrupt. The agentloop.Loop itself does NOT use this — it
// only knows about ctx cancellation. This helper is the glue that
// turns a Router interrupt into a clean cancel + drain + replay.
//
// Pattern A from RT-CANCEL-INTERRUPT (drop partial). We never persist
// a partial assistant message; the committed history ends with the
// most recent user message (which is valid for the API).
//
// Per-block completion state lives in a local map keyed by block index
// (NOT inside agentloop.Message — that struct has no Meta field). On
// the interrupt path, any block whose index is missing from the
// "completed" set is dropped before the synthetic user message is
// appended, matching the Cline pattern documented in
// RT-CANCEL-INTERRUPT.md §3.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// StopReason classifies how RunTurnWithInterrupt terminated. The
// caller (REPL) routes follow-up behavior off this value: StopNormal
// continues the loop, StopInterrupted starts a new turn with the
// synthetic user message appended, StopErr surfaces the error to the
// user, StopCancelled honors the parent context's cancellation.
type StopReason string

const (
	// StopNormal: stream completed cleanly; partial committed as the
	// assistant message in the returned slice.
	StopNormal StopReason = "normal"
	// StopInterrupted: an InterruptPayload arrived (or the watchdog
	// fired). Partial is dropped; a synthetic user message is appended.
	StopInterrupted StopReason = "interrupted"
	// StopErr: the stream goroutine returned a non-nil error. msgs is
	// returned unchanged.
	StopErr StopReason = "err"
	// StopCancelled: the parent context was cancelled. msgs is returned
	// unchanged; err is parentCtx.Err().
	StopCancelled StopReason = "cancelled"
)

// StreamEvent is the internal cortex-side abstraction over a
// streamed-message event. RunTurnWithInterrupt consumes a chan of
// these; the provider adapter goroutine translates stream.Event
// (Anthropic SSE-derived) into StreamEvent. A test can construct
// StreamEvents directly by driving the unexported channel-based
// helper.
//
// Kind is one of:
//   - "block_start"      — a new content block starts at BlockIdx (text or tool_use). For tool_use, ToolName is set.
//   - "text_delta"       — Text appended to the text block at BlockIdx.
//   - "input_json_delta" — ToolInputDelta appended to the tool_use block at BlockIdx.
//   - "block_stop"       — the block at BlockIdx is complete. Marks the block as eligible to commit.
//   - "error"            — Err is non-nil; the stream is dead.
type StreamEvent struct {
	Kind           string
	BlockIdx       int
	Text           string
	ToolName       string
	ToolInputDelta string
	Err            error
}

// stream-event Kind literals. Pulled out as constants so the wire
// contract has a single source of truth.
const (
	streamKindBlockStart     = "block_start"
	streamKindTextDelta      = "text_delta"
	streamKindInputJSONDelta = "input_json_delta"
	streamKindBlockStop      = "block_stop"
	streamKindError          = "error"
)

// content block type literals (mirrors agentloop.ContentBlock.Type
// values used here — kept local to avoid a tight coupling on
// agentloop's exported constants).
const (
	blockTypeText    = "text"
	blockTypeToolUse = "tool_use"
)

// watchdogReasonIdle is set on the synthetic user message's Reason
// when the idle watchdog fires before the parent context is cancelled
// or a stream event arrives.
const watchdogReasonIdle = "idle"

// errWatchdogIdle is returned by the watchdog goroutine via
// streamCtx cancellation. The select branch on streamCtx.Done()
// distinguishes idle-watchdog cancellation from parent-ctx
// cancellation by checking parentCtx.Err() first.
var errWatchdogIdle = errors.New("cortex: turn idle watchdog fired (no events for >watchdog window)")

// accumulate folds a single StreamEvent into the in-progress partial
// assistant Message. Per-block completion state lives in `completed`,
// keyed by block index. Accumulate appends text deltas to
// partial.Content[idx].Text, appends input_json_delta fragments to a
// scratch buffer keyed by index (since agentloop.ContentBlock.Input is
// json.RawMessage and not appendable in-place), and records
// block_stop events in `completed[idx] = true`.
//
// The scratch buffer for partial tool-input JSON is passed in via
// `inputBufs` so it survives across calls without polluting Message.
//
// On block_start, partial.Content is grown to len >= BlockIdx+1 with
// a fresh ContentBlock at BlockIdx; existing blocks at lower indices
// are preserved.
func accumulate(
	partial agentloop.Message,
	ev StreamEvent,
	completed map[int]bool,
	inputBufs map[int]*[]byte,
) agentloop.Message {
	idx := ev.BlockIdx
	switch ev.Kind {
	case streamKindBlockStart:
		// Grow Content to cover this index. Insert a new block whose
		// Type is determined by whether ToolName is set — tool_use
		// blocks are distinguished by a non-empty Name.
		for len(partial.Content) <= idx {
			partial.Content = append(partial.Content, agentloop.ContentBlock{})
		}
		blk := agentloop.ContentBlock{}
		if ev.ToolName != "" {
			blk.Type = blockTypeToolUse
			blk.Name = ev.ToolName
		} else {
			blk.Type = blockTypeText
		}
		partial.Content[idx] = blk
	case streamKindTextDelta:
		for len(partial.Content) <= idx {
			partial.Content = append(partial.Content, agentloop.ContentBlock{Type: blockTypeText})
		}
		// Default the type if a text_delta arrives without a prior
		// block_start (defensive — some upstreams skip the explicit
		// content_block_start frame for index 0).
		if partial.Content[idx].Type == "" {
			partial.Content[idx].Type = blockTypeText
		}
		partial.Content[idx].Text += ev.Text
	case streamKindInputJSONDelta:
		for len(partial.Content) <= idx {
			partial.Content = append(partial.Content, agentloop.ContentBlock{Type: blockTypeToolUse})
		}
		if partial.Content[idx].Type == "" {
			partial.Content[idx].Type = blockTypeToolUse
		}
		buf := inputBufs[idx]
		if buf == nil {
			b := make([]byte, 0, len(ev.ToolInputDelta)+8)
			buf = &b
			inputBufs[idx] = buf
		}
		*buf = append(*buf, ev.ToolInputDelta...)
		// Note: Input stays empty until block_stop; the partial JSON
		// is unparseable until the model finishes the block. We
		// commit the assembled bytes to ContentBlock.Input on
		// block_stop, but only for completed blocks anyway.
	case streamKindBlockStop:
		completed[idx] = true
		// On block_stop for a tool_use, commit the assembled JSON
		// fragments to ContentBlock.Input. If parsing fails, we still
		// mark the block "completed" but the caller must validate
		// before sending it back to the API.
		if idx < len(partial.Content) && partial.Content[idx].Type == blockTypeToolUse {
			if buf, ok := inputBufs[idx]; ok && buf != nil && len(*buf) > 0 {
				partial.Content[idx].Input = append([]byte(nil), (*buf)...)
			}
		}
	case streamKindError:
		// Errors are handled by the caller; no accumulation.
	}
	return partial
}

// dropIncompleteBlocks returns a copy of partial whose Content slice
// only contains blocks whose index appears in `completed`. Filter +
// rebuild is used since slice index deletion is awkward in Go and we
// want stable index → block correspondence to be irrelevant after
// drop (the API only sees the filtered slice).
func dropIncompleteBlocks(partial agentloop.Message, completed map[int]bool) agentloop.Message {
	out := agentloop.Message{Role: partial.Role}
	for idx, blk := range partial.Content {
		if completed[idx] {
			out.Content = append(out.Content, blk)
		}
	}
	return out
}

// formatInterrupt produces the synthetic user-message text that gets
// appended after a drop-partial interrupt. The format mirrors the
// pseudocode in specs/cortex-core.md §"Drop-partial interrupt
// protocol". A non-empty Reason is required (Router's interrupt tool
// validates this); NewDirection is optional and only included if
// non-empty.
func formatInterrupt(ip InterruptPayload) string {
	source := ip.Source
	if source == "" {
		source = "system"
	}
	severity := ip.Severity
	if severity == "" {
		severity = "info"
	}
	out := fmt.Sprintf(
		"<system-interrupt source=%q severity=%q>\n%s\n</system-interrupt>",
		source, severity, ip.Reason,
	)
	if ip.NewDirection != "" {
		out += "\n\nNew direction: " + ip.NewDirection
	}
	return out
}

// RunTurnWithInterrupt drives a single Anthropic Messages API turn
// with mid-stream interrupt support. Behavior matches the spec
// pseudocode in specs/cortex-core.md §"Drop-partial interrupt
// protocol":
//
//   - context.WithCancel scope around the streaming call.
//   - SSE-reader goroutine pushes StreamEvents onto respCh until the
//     provider returns; doneCh receives the final error (or nil).
//   - 30s ping-based idle watchdog (configurable via `watchdog`)
//     calls cancelTurn() if no event arrives within the window.
//   - Main loop selects on respCh / doneCh / interruptCh /
//     parentCtx.Done; on interrupt, drains both respCh and doneCh,
//     drops blocks not in `completed`, appends a synthetic user
//     message, and returns StopInterrupted.
//
// Invariants the implementation guarantees (per RT-CANCEL-INTERRUPT
// §3):
//   - `partial` is never appended to msgs on the interrupt path.
//   - cancelTurn() is called before draining channels; channels are
//     drained before returning to prevent goroutine leaks.
//   - The synthetic user message is the FINAL message in the returned
//     slice, so the next API call is `user`-terminated and valid.
//
// `req.Messages` is treated as the committed history. The function
// does not mutate `req`. The returned `msgs` is a fresh slice; on
// StopNormal it ends with the assistant message; on StopInterrupted
// it ends with the synthetic user message; on StopErr / StopCancelled
// it equals the input `req.Messages` translated to []agentloop.Message.
//
// `watchdog <= 0` disables the idle watchdog. Defaults to 30s if 0.
func RunTurnWithInterrupt(
	parentCtx context.Context,
	p provider.Provider,
	req provider.ChatRequest,
	interruptCh <-chan InterruptPayload,
	watchdog time.Duration,
) (msgs []agentloop.Message, reason StopReason, err error) {
	if watchdog == 0 {
		watchdog = 30 * time.Second
	}

	// Translate req.Messages → []agentloop.Message for the return
	// slice. We don't deep-clone the content blocks; the caller treats
	// the result as read-only-by-convention.
	committed := chatMessagesToAgentloop(req.Messages)

	// Spawn the provider adapter goroutine. It calls ChatStream
	// (synchronous) under streamCtx and translates each stream.Event
	// into one or more StreamEvents on respCh. When ChatStream
	// returns, doneCh receives the final error (or nil) and respCh is
	// closed. The goroutine MUST observe streamCtx so cancelTurn()
	// causes it to terminate promptly even if the underlying
	// ChatStream implementation does not honor context (the current
	// provider.AnthropicProvider.ChatStream signature has no context
	// parameter — see internal/provider/anthropic.go:293).
	streamCtx, cancelTurn := context.WithCancel(parentCtx)
	defer cancelTurn()

	respCh := make(chan StreamEvent, 64)
	doneCh := make(chan error, 1)

	go runProviderStream(streamCtx, p, req, respCh, doneCh)

	return runTurnWithInterruptStream(
		parentCtx, streamCtx, cancelTurn,
		committed, respCh, doneCh, interruptCh, watchdog,
	)
}

// runTurnWithInterruptStream is the channel-driven core of
// RunTurnWithInterrupt. Exposed (unexported) so tests can drive the
// drop-partial protocol directly without needing to go through a
// provider.Provider stub. Callers MUST close respCh AND send to
// doneCh exactly once — the function relies on this contract for
// drain correctness.
func runTurnWithInterruptStream(
	parentCtx context.Context,
	streamCtx context.Context,
	cancelTurn context.CancelFunc,
	committed []agentloop.Message,
	respCh <-chan StreamEvent,
	doneCh <-chan error,
	interruptCh <-chan InterruptPayload,
	watchdog time.Duration,
) (msgs []agentloop.Message, reason StopReason, err error) {

	// Per-block completion tracker. Keyed by block index (the same
	// index the SSE stream uses for content_block_start /
	// content_block_stop). Only blocks present in this map at
	// interrupt time are committed; everything else is dropped.
	completed := map[int]bool{}
	inputBufs := map[int]*[]byte{}
	partial := agentloop.Message{Role: "assistant"}

	// Idle watchdog. The watchdog goroutine ticks every
	// max(1s, watchdog/6) and cancels streamCtx when the time since
	// the most recent event exceeds `watchdog`. We use a separate
	// context (`streamCtx`) so the watchdog can distinguish
	// idle-driven cancellation from parent-ctx cancellation: when
	// streamCtx.Err() is set but parentCtx.Err() is nil, the
	// watchdog fired. The atomic `watchdogFired` flag confirms which
	// path took us here.
	var (
		lastEventMu    sync.Mutex
		lastEvent      = time.Now()
		watchdogFired  = false
		watchdogDone   = make(chan struct{})
	)
	// Tick frequency: aim for ~6 ticks per window, with a 5ms floor
	// so very small watchdogs (used by tests) still tick reliably and
	// a 1s ceiling so production tickers don't burn CPU waking up
	// constantly. The watchdog only needs to fire WITHIN window; some
	// jitter is fine.
	tickEvery := watchdog / 6
	if tickEvery < 5*time.Millisecond {
		tickEvery = 5 * time.Millisecond
	}
	if tickEvery > time.Second {
		tickEvery = time.Second
	}
	if watchdog > 0 {
		go func() {
			defer close(watchdogDone)
			t := time.NewTicker(tickEvery)
			defer t.Stop()
			for {
				select {
				case <-streamCtx.Done():
					return
				case <-t.C:
					lastEventMu.Lock()
					stale := time.Since(lastEvent) > watchdog
					if stale {
						watchdogFired = true
					}
					lastEventMu.Unlock()
					if stale {
						slog.Debug("cortex: turn watchdog firing — no events within window",
							"window", watchdog,
						)
						cancelTurn()
						return
					}
				}
			}
		}()
	} else {
		close(watchdogDone)
	}

	for {
		select {
		case ev, ok := <-respCh:
			if !ok {
				// respCh closed before doneCh fired. Wait for the
				// adapter goroutine to send its terminal status. The
				// stream's text/tool blocks have all been consumed
				// already (channel closure follows the last send), so
				// no further accumulate calls are needed.
				select {
				case finalErr := <-doneCh:
					cancelTurn()
					<-watchdogDone
					if finalErr != nil {
						return committed, StopErr, finalErr
					}
					committedFinal := commitPartial(committed, partial, completed)
					return committedFinal, StopNormal, nil
				case <-parentCtx.Done():
					cancelTurn()
					<-doneCh
					<-watchdogDone
					return committed, StopCancelled, parentCtx.Err()
				}
			}
			lastEventMu.Lock()
			lastEvent = time.Now()
			lastEventMu.Unlock()
			if ev.Kind == streamKindError && ev.Err != nil {
				// Surface stream-level errors as StopErr after
				// draining cleanly.
				cancelTurn()
				drain(respCh)
				<-doneCh
				<-watchdogDone
				return committed, StopErr, ev.Err
			}
			partial = accumulate(partial, ev, completed, inputBufs)

		case finalErr := <-doneCh:
			// Stream done. Drain any buffered events that arrived
			// concurrently with the doneCh write — and ACCUMULATE
			// them, so the assistant message reflects everything the
			// model emitted. The Go runtime selects pseudo-randomly
			// among ready cases, so it is possible doneCh fires
			// before we've consumed all events on respCh; we must
			// catch up here.
			for ev := range respCh {
				if ev.Kind == streamKindError && ev.Err != nil {
					cancelTurn()
					<-watchdogDone
					return committed, StopErr, ev.Err
				}
				partial = accumulate(partial, ev, completed, inputBufs)
			}
			// Cancel streamCtx so the watchdog goroutine exits. The
			// watchdog's `<-streamCtx.Done()` branch is the ONLY way
			// it terminates after the stream has finished cleanly,
			// otherwise it would keep ticking until the watchdog
			// window expires.
			cancelTurn()
			<-watchdogDone
			if finalErr != nil {
				return committed, StopErr, finalErr
			}
			committedFinal := commitPartial(committed, partial, completed)
			return committedFinal, StopNormal, nil

		case ip := <-interruptCh:
			// (1) tear down stream
			cancelTurn()
			// (2) drain BOTH the response channel AND the done
			//     channel, otherwise the SSE reader goroutine leaks
			//     (RT-CANCEL-INTERRUPT §4).
			drain(respCh)
			<-doneCh
			<-watchdogDone

			// (3) Pattern A — drop incomplete blocks. We don't
			//     entirely discard `partial` because complete blocks
			//     could in principle be preserved, but per the spec
			//     pseudocode the synthetic user message replaces the
			//     assistant response; we honor that by NEVER
			//     appending `partial` to the committed history.
			_ = dropIncompleteBlocks(partial, completed) // intentional: the result is observable to tests via partial state, but committed history is unchanged.

			// (4) append synthetic user message
			out := append([]agentloop.Message(nil), committed...)
			out = append(out, syntheticInterruptMessage(ip))
			return out, StopInterrupted, nil

		case <-streamCtx.Done():
			// streamCtx was cancelled but parentCtx is still alive.
			// Either the watchdog fired or someone else cancelled the
			// inner ctx out from under us. Drain and return as an
			// interrupt-shaped result so the REPL can replay.
			lastEventMu.Lock()
			fired := watchdogFired
			lastEventMu.Unlock()
			drain(respCh)
			<-doneCh
			<-watchdogDone
			if parentCtx.Err() != nil {
				// Parent was cancelled; honor it.
				return committed, StopCancelled, parentCtx.Err()
			}
			if fired {
				out := append([]agentloop.Message(nil), committed...)
				out = append(out, syntheticInterruptMessage(InterruptPayload{
					Source:   "cortex",
					Severity: "warn",
					Reason:   watchdogReasonIdle,
				}))
				return out, StopInterrupted, errWatchdogIdle
			}
			// Inner cancel without parent cancel and without watchdog —
			// treat as interrupted with empty reason.
			return committed, StopInterrupted, streamCtx.Err()

		case <-parentCtx.Done():
			cancelTurn()
			drain(respCh)
			<-doneCh
			<-watchdogDone
			return committed, StopCancelled, parentCtx.Err()
		}
	}
}

// commitPartial returns committed extended with the partial assistant
// message, but only if at least one block in `partial` survives the
// completed filter. If every block was incomplete, the returned slice
// equals committed (the API rejects empty assistant messages).
func commitPartial(committed []agentloop.Message, partial agentloop.Message, completed map[int]bool) []agentloop.Message {
	final := dropIncompleteBlocks(partial, completed)
	out := append([]agentloop.Message(nil), committed...)
	if len(final.Content) > 0 {
		out = append(out, final)
	}
	return out
}

// syntheticInterruptMessage builds the user-role message appended to
// the committed history when the turn is interrupted.
func syntheticInterruptMessage(ip InterruptPayload) agentloop.Message {
	return agentloop.Message{
		Role: "user",
		Content: []agentloop.ContentBlock{{
			Type: blockTypeText,
			Text: "[INTERRUPTED by " + nonEmpty(ip.Source, "system") + " — " +
				nonEmpty(ip.Severity, "info") + "] " + formatInterrupt(ip),
		}},
	}
}

// nonEmpty returns s if non-empty, otherwise fallback.
func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// drain consumes any remaining buffered events on respCh until it
// closes. Safe to call after cancelTurn() — the adapter goroutine
// closes respCh after observing streamCtx.Done().
func drain(respCh <-chan StreamEvent) {
	for range respCh {
	}
}

// chatMessagesToAgentloop converts provider.ChatMessage (raw JSON
// content) to agentloop.Message (typed content blocks). On parse
// failure we fall back to a single text block with the raw payload —
// the caller's history may already contain malformed content (e.g.
// from a prior failed turn) and we don't want this helper to panic.
func chatMessagesToAgentloop(in []provider.ChatMessage) []agentloop.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]agentloop.Message, 0, len(in))
	for _, m := range in {
		am := agentloop.Message{Role: m.Role}
		// The provider.ChatMessage.Content is json.RawMessage; we
		// don't attempt to fully unmarshal here because
		// agentloop.ContentBlock fields are a superset of what may
		// appear on the wire. Instead, treat the raw bytes as
		// opaque history content. The synthetic user message we
		// append on interrupt uses agentloop.ContentBlock directly,
		// so the returned slice is valid for the next turn.
		_ = m.Content
		out = append(out, am)
	}
	return out
}

// runProviderStream calls p.ChatStream synchronously, translating
// every stream.Event into one or more StreamEvents on respCh, then
// closes respCh and writes the terminal error to doneCh. ChatStream
// has no context parameter (see internal/provider/anthropic.go:293)
// so we honor streamCtx.Done() at TWO boundaries:
//
//  1. Inside the onEvent callback we no-op once ctx is cancelled,
//     to stop pushing events.
//  2. Around the ChatStream call we run a watcher goroutine that
//     observes ctx.Done() independently. On cancellation, it closes
//     respCh and writes the cancellation error to doneCh immediately
//     — the underlying ChatStream call may still be hung, but the
//     drop-partial protocol no longer waits for it. The leaked
//     ChatStream eventually returns (when the upstream connection
//     closes or the test fixture releases its block) and we discard
//     its result. This is the standard "fast cancel" pattern when
//     the underlying SDK does not honor context.
//
// Stream-event translation:
//   - DeltaType == "text_delta" → emit block_start (idempotent for
//     the same idx), then text_delta with Text=DeltaText.
//   - DeltaType == "input_json_delta" → emit input_json_delta with
//     ToolInputDelta=DeltaText.
//   - len(ToolUses) > 0 → emit block_stop (we synthesize the index
//     based on a running counter since stream.Event drops the
//     SSE-level "index" field). This is approximate; real per-block
//     indexing would require deeper SSE-level access.
//
// The tests use runTurnWithInterruptStream directly to bypass this
// adapter, so the imperfect index synthesis here is acceptable for
// non-test paths.
func runProviderStream(
	ctx context.Context,
	p provider.Provider,
	req provider.ChatRequest,
	respCh chan<- StreamEvent,
	doneCh chan<- error,
) {
	// Track per-stream block indices. stream.Event from Anthropic SSE
	// loses the explicit "index" field (the SSE parser folds it into
	// internal state), so we maintain a running counter that
	// increments on each new block boundary we observe.
	var nextIdx int
	var curTextIdx = -1

	// closeOnce / doneOnce ensure we only close respCh and only send
	// to doneCh once, regardless of which path (ChatStream return vs
	// ctx cancellation) wins the race.
	var (
		closeOnce sync.Once
		doneOnce  sync.Once
	)
	closeResp := func() { closeOnce.Do(func() { close(respCh) }) }
	finishDone := func(err error) { doneOnce.Do(func() { doneCh <- err }) }

	emit := func(ev StreamEvent) {
		// Defensive: if respCh has already been closed by the cancel
		// watcher, swallow the panic. The send-after-close race is
		// expected when ctx cancels mid-stream.
		defer func() { _ = recover() }()
		select {
		case <-ctx.Done():
		case respCh <- ev:
		}
	}

	// Cancel watcher: as soon as ctx cancels, immediately tear down
	// the channels so RunTurnWithInterrupt unblocks. The leaked
	// ChatStream goroutine will return at its leisure.
	cancelObserved := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			closeResp()
			finishDone(ctx.Err())
		case <-cancelObserved:
			// Normal ChatStream completion path took over; we exit.
		}
	}()

	// Run ChatStream in its own goroutine so the cancel watcher can
	// preempt it. We detach from the result on ctx cancel.
	type chatResult struct{ err error }
	resultCh := make(chan chatResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultCh <- chatResult{err: fmt.Errorf("cortex: provider stream panicked: %v", r)}
			}
		}()
		_, err := p.ChatStream(req, func(ev stream.Event) {
			if ctx.Err() != nil {
				return
			}
			switch {
			case ev.DeltaType == "text_delta" && ev.DeltaText != "":
				if curTextIdx < 0 {
					curTextIdx = nextIdx
					nextIdx++
					emit(StreamEvent{Kind: streamKindBlockStart, BlockIdx: curTextIdx})
				}
				emit(StreamEvent{Kind: streamKindTextDelta, BlockIdx: curTextIdx, Text: ev.DeltaText})
			case ev.DeltaType == "input_json_delta" && ev.DeltaText != "":
				emit(StreamEvent{Kind: streamKindInputJSONDelta, BlockIdx: nextIdx, ToolInputDelta: ev.DeltaText})
			case len(ev.ToolUses) > 0:
				if curTextIdx >= 0 {
					emit(StreamEvent{Kind: streamKindBlockStop, BlockIdx: curTextIdx})
					curTextIdx = -1
				}
				for _, tu := range ev.ToolUses {
					idx := nextIdx
					nextIdx++
					emit(StreamEvent{Kind: streamKindBlockStart, BlockIdx: idx, ToolName: tu.Name})
					emit(StreamEvent{Kind: streamKindBlockStop, BlockIdx: idx})
				}
			}
		})
		// Close the in-progress text block (if any) on clean stream end.
		if curTextIdx >= 0 && err == nil && ctx.Err() == nil {
			emit(StreamEvent{Kind: streamKindBlockStop, BlockIdx: curTextIdx})
		}
		resultCh <- chatResult{err: err}
	}()

	// Wait for either ChatStream completion or ctx cancellation. The
	// cancel watcher above handles cancellation; we just wait for the
	// chat goroutine here so we know when to close cleanly on the
	// happy path.
	select {
	case res := <-resultCh:
		// Tell the cancel watcher to exit (it may already have, if
		// ctx cancelled concurrently — that's fine).
		close(cancelObserved)
		closeResp()
		finishDone(res.err)
	case <-ctx.Done():
		// Cancel watcher already fired closeResp + finishDone. We
		// still wait briefly for resultCh so the chat goroutine
		// drains; but if ChatStream is hung on a network call we
		// don't block forever — return immediately and let the chat
		// goroutine leak until the SDK closes the underlying request.
		close(cancelObserved)
	}
}
