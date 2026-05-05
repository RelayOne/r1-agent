package cortex

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// hangProvider is a provider.Provider whose ChatStream blocks until
// the embedded done channel closes. Used by TestWatchdogIdle to
// simulate a stalled SSE connection that never sends events.
type hangProvider struct {
	done chan struct{}
}

func (h *hangProvider) Name() string { return "hang" }
func (h *hangProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	<-h.done
	return &provider.ChatResponse{}, nil
}
func (h *hangProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	<-h.done
	return &provider.ChatResponse{}, nil
}

// TestDropPartialOnInterrupt verifies the core invariant of
// RT-CANCEL-INTERRUPT §3: when an InterruptPayload arrives mid-stream
// during a tool_use block (after content_block_start + 2
// input_json_delta but BEFORE content_block_stop), the partial
// tool_use block is dropped from the committed history and a
// synthetic user message is appended.
//
// The test drives runTurnWithInterruptStream directly to avoid the
// provider adapter goroutine — we want full control over event
// ordering.
func TestDropPartialOnInterrupt(t *testing.T) {
	t.Parallel()

	parentCtx := context.Background()
	streamCtx, cancelTurn := context.WithCancel(parentCtx)

	respCh := make(chan StreamEvent, 16)
	doneCh := make(chan error, 1)
	interruptCh := make(chan InterruptPayload, 1)

	committed := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "do the thing"}}},
	}

	// Background streamer: emits the prefix events and then waits for
	// streamCtx to be cancelled before closing respCh + doneCh. This
	// simulates an SSE reader that observes ctx cancellation and
	// terminates cleanly without ever sending content_block_stop for
	// block 1.
	streamerExited := make(chan struct{})
	go func() {
		defer close(streamerExited)
		// block 0: a complete text block (start + delta + stop)
		respCh <- StreamEvent{Kind: streamKindBlockStart, BlockIdx: 0}
		respCh <- StreamEvent{Kind: streamKindTextDelta, BlockIdx: 0, Text: "thinking..."}
		respCh <- StreamEvent{Kind: streamKindBlockStop, BlockIdx: 0}
		// block 1: tool_use, start + 2 input_json deltas, NO stop
		respCh <- StreamEvent{Kind: streamKindBlockStart, BlockIdx: 1, ToolName: "Read"}
		respCh <- StreamEvent{Kind: streamKindInputJSONDelta, BlockIdx: 1, ToolInputDelta: `{"path":`}
		respCh <- StreamEvent{Kind: streamKindInputJSONDelta, BlockIdx: 1, ToolInputDelta: `"/tmp/x"`}
		// Wait for cancellation, then close cleanly.
		<-streamCtx.Done()
		close(respCh)
		doneCh <- streamCtx.Err()
	}()

	// Fire interrupt after we expect ~3 events to have been consumed.
	// Use a small sleep then send — this is a deterministic-enough
	// proxy without a fake clock; the function's drain logic is what
	// we're really testing.
	go func() {
		time.Sleep(20 * time.Millisecond)
		interruptCh <- InterruptPayload{
			Source:       "concern.security",
			Severity:     "critical",
			Reason:       "secret leak detected",
			NewDirection: "stop reading files; await audit",
		}
	}()

	msgs, reason, err := runTurnWithInterruptStream(
		parentCtx, streamCtx, cancelTurn,
		committed, respCh, doneCh, interruptCh,
		5*time.Second, // long watchdog so it doesn't fire
	)
	if err != nil {
		t.Fatalf("RunTurnWithInterruptStream: unexpected error: %v", err)
	}
	if reason != StopInterrupted {
		t.Fatalf("reason = %q, want %q", reason, StopInterrupted)
	}
	<-streamerExited

	if len(msgs) != len(committed)+1 {
		t.Fatalf("len(msgs) = %d, want %d (committed + 1 synthetic user)", len(msgs), len(committed)+1)
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" {
		t.Errorf("last message role = %q, want %q", last.Role, "user")
	}
	if len(last.Content) != 1 || last.Content[0].Type != "text" {
		t.Fatalf("last message content shape unexpected: %+v", last.Content)
	}
	if !strings.HasPrefix(last.Content[0].Text, "[INTERRUPTED") {
		t.Errorf("synthetic message text doesn't start with [INTERRUPTED: %q", last.Content[0].Text)
	}
	if !strings.Contains(last.Content[0].Text, "secret leak detected") {
		t.Errorf("synthetic message missing reason: %q", last.Content[0].Text)
	}
	if !strings.Contains(last.Content[0].Text, "stop reading files") {
		t.Errorf("synthetic message missing new direction: %q", last.Content[0].Text)
	}

	// Critical invariant: NO assistant message in msgs should contain
	// the partial tool_use block (block index 1 — never received
	// block_stop).
	for i, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, blk := range m.Content {
			if blk.Type == "tool_use" && blk.Name == "Read" {
				t.Errorf("msgs[%d] contains partial Read tool_use block — drop-partial invariant violated", i)
			}
		}
	}
}

// TestStreamDoneDrained verifies that the function waits for both
// respCh closure and doneCh send before returning, AND that no
// goroutines leak across calls. We snapshot runtime.NumGoroutine
// before and after.
func TestStreamDoneDrained(t *testing.T) {
	t.Parallel()

	// Settle goroutine count before snapshotting. Run a few small
	// iterations first to let any package-level init goroutines
	// stabilize.
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	before := runtime.NumGoroutine()

	for i := 0; i < 8; i++ {
		runOnceClean(t)
	}

	// Allow leaked goroutines (if any) a moment to be visible.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()

	// Allow a small slack — testing goroutines, runtime debug
	// goroutines, etc. can fluctuate by 1-2.
	if after > before+3 {
		t.Errorf("goroutine leak: before=%d after=%d (delta=%d)", before, after, after-before)
	}
}

// runOnceClean invokes runTurnWithInterruptStream for a streamer that
// completes cleanly (block + stop + done with nil error) and asserts
// StopNormal.
func runOnceClean(t *testing.T) {
	t.Helper()
	parentCtx := context.Background()
	streamCtx, cancelTurn := context.WithCancel(parentCtx)

	respCh := make(chan StreamEvent, 4)
	doneCh := make(chan error, 1)
	interruptCh := make(chan InterruptPayload, 1)

	go func() {
		respCh <- StreamEvent{Kind: streamKindBlockStart, BlockIdx: 0}
		respCh <- StreamEvent{Kind: streamKindTextDelta, BlockIdx: 0, Text: "ok"}
		respCh <- StreamEvent{Kind: streamKindBlockStop, BlockIdx: 0}
		close(respCh)
		doneCh <- nil
	}()

	msgs, reason, err := runTurnWithInterruptStream(
		parentCtx, streamCtx, cancelTurn,
		nil, respCh, doneCh, interruptCh,
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("clean run: unexpected error: %v", err)
	}
	if reason != StopNormal {
		t.Fatalf("clean run: reason = %q, want %q", reason, StopNormal)
	}
	if len(msgs) != 1 || msgs[0].Role != "assistant" {
		t.Fatalf("clean run: msgs shape unexpected: %+v", msgs)
	}
	if msgs[0].Content[0].Text != "ok" {
		t.Errorf("clean run: text = %q, want %q", msgs[0].Content[0].Text, "ok")
	}
}

// TestWatchdogIdle verifies the idle watchdog fires when no events
// arrive within the configured window. We use watchdog=50ms and a
// streamer that only sends doneCh after streamCtx is cancelled —
// simulating a totally silent stream.
//
// Per spec §"Drop-partial interrupt protocol", the function returns
// StopInterrupted (with err set to a watchdog-derived sentinel and a
// synthetic user message whose Reason is "idle"). The function MUST
// return promptly after the watchdog window — we assert <500ms (10x
// the window) to allow for CI scheduling jitter.
func TestWatchdogIdle(t *testing.T) {
	t.Parallel()

	parentCtx := context.Background()
	streamCtx, cancelTurn := context.WithCancel(parentCtx)

	respCh := make(chan StreamEvent, 1)
	doneCh := make(chan error, 1)
	interruptCh := make(chan InterruptPayload, 1)

	go func() {
		// Wait for streamCtx cancellation (which the watchdog will
		// trigger), then close cleanly.
		<-streamCtx.Done()
		close(respCh)
		doneCh <- streamCtx.Err()
	}()

	start := time.Now()
	msgs, reason, err := runTurnWithInterruptStream(
		parentCtx, streamCtx, cancelTurn,
		nil, respCh, doneCh, interruptCh,
		50*time.Millisecond,
	)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("watchdog took too long: %v (want <500ms)", elapsed)
	}
	if reason != StopInterrupted {
		t.Fatalf("watchdog: reason = %q, want %q", reason, StopInterrupted)
	}
	if !errors.Is(err, errWatchdogIdle) {
		t.Errorf("watchdog: err = %v, want errWatchdogIdle", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("watchdog: len(msgs) = %d, want 1 (synthetic user)", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("watchdog: msgs[0].Role = %q, want %q", msgs[0].Role, "user")
	}
	if !strings.Contains(msgs[0].Content[0].Text, "idle") {
		t.Errorf("watchdog: synthetic message missing 'idle' reason: %q", msgs[0].Content[0].Text)
	}
}

// TestAccumulateBlockTracking verifies the per-block completion
// tracking semantics that the drop-partial protocol relies on.
// accumulate must record block_stop events into `completed` and must
// append text deltas to the block at BlockIdx without disturbing
// other blocks.
func TestAccumulateBlockTracking(t *testing.T) {
	t.Parallel()

	completed := map[int]bool{}
	inputBufs := map[int]*[]byte{}
	partial := agentloop.Message{Role: "assistant"}

	events := []StreamEvent{
		{Kind: streamKindBlockStart, BlockIdx: 0},
		{Kind: streamKindTextDelta, BlockIdx: 0, Text: "hello "},
		{Kind: streamKindTextDelta, BlockIdx: 0, Text: "world"},
		{Kind: streamKindBlockStop, BlockIdx: 0},
		{Kind: streamKindBlockStart, BlockIdx: 1, ToolName: "Read"},
		{Kind: streamKindInputJSONDelta, BlockIdx: 1, ToolInputDelta: `{"path":"/x"}`},
		// no block_stop for block 1
	}

	for _, ev := range events {
		partial = accumulate(partial, ev, completed, inputBufs)
	}

	if !completed[0] {
		t.Errorf("completed[0] = false, want true")
	}
	if completed[1] {
		t.Errorf("completed[1] = true, want false (no block_stop received)")
	}
	if len(partial.Content) != 2 {
		t.Fatalf("len(partial.Content) = %d, want 2", len(partial.Content))
	}
	if partial.Content[0].Type != "text" || partial.Content[0].Text != "hello world" {
		t.Errorf("block 0 content = %+v, want type=text text='hello world'", partial.Content[0])
	}
	if partial.Content[1].Type != "tool_use" || partial.Content[1].Name != "Read" {
		t.Errorf("block 1 content = %+v, want type=tool_use name=Read", partial.Content[1])
	}

	// dropIncompleteBlocks must drop block 1 (incomplete) and keep block 0.
	final := dropIncompleteBlocks(partial, completed)
	if len(final.Content) != 1 {
		t.Errorf("final.Content len = %d, want 1 (block 1 dropped)", len(final.Content))
	}
	if final.Content[0].Type != "text" {
		t.Errorf("final.Content[0].Type = %q, want text", final.Content[0].Type)
	}
}

// TestRunTurnWithInterruptViaProvider exercises the public entry
// point with a hangProvider to confirm the watchdog path works
// end-to-end through the provider adapter goroutine. This is a smoke
// test — the deeper invariants are covered by the channel-driven
// tests above.
func TestRunTurnWithInterruptViaProvider(t *testing.T) {
	t.Parallel()

	hp := &hangProvider{done: make(chan struct{})}
	defer close(hp.done) // ensure the provider goroutine eventually returns

	interruptCh := make(chan InterruptPayload, 1)
	req := provider.ChatRequest{
		Model: "claude-haiku-4-5",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: []byte(`"hi"`)},
		},
	}

	start := time.Now()
	_, reason, err := RunTurnWithInterrupt(context.Background(), hp, req, interruptCh, 50*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("public path: watchdog took too long: %v", elapsed)
	}
	if reason != StopInterrupted {
		t.Errorf("public path: reason = %q, want %q (watchdog should fire on hangProvider)", reason, StopInterrupted)
	}
	if !errors.Is(err, errWatchdogIdle) {
		t.Errorf("public path: err = %v, want errWatchdogIdle", err)
	}
}
