package sessionhub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// gatedProvider lets the test control when each Chat/ChatStream
// call returns. Each call blocks on releaseCh receiving a value, so
// the test can pre-load the inbox before allowing the bootstrap
// turn's end_turn to fire.
type gatedProvider struct {
	mu        sync.Mutex
	prompts   []string
	releaseCh chan struct{}
}

func (p *gatedProvider) Name() string { return "gated" }

func (p *gatedProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return p.handle(req)
}

func (p *gatedProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return p.handle(req)
}

func (p *gatedProvider) handle(req provider.ChatRequest) (*provider.ChatResponse, error) {
	// Block until the test signals us to release.
	<-p.releaseCh
	p.mu.Lock()
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != "user" {
			continue
		}
		p.prompts = append(p.prompts, string(m.Content))
		break
	}
	p.mu.Unlock()
	return &provider.ChatResponse{
		Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
		StopReason: "end_turn",
	}, nil
}

// TestSession_InboxDrainDelivers asserts that a session.Send call
// landing while the agent loop is running gets picked up at the next
// end_turn boundary and feeds the next turn.
//
// Sequencing:
//  1. Start Run with bootstrap "first turn".
//  2. Provider call 1 blocks on releaseCh.
//  3. Test calls Send("drained turn"). Inbox now has 1 turn.
//  4. Test releases provider call 1 → bootstrap turn ends with end_turn.
//  5. EndTurnContinuation drains the inbox, injects "drained turn" as
//     the next user turn, loop continues.
//  6. Provider call 2 blocks on releaseCh.
//  7. Test releases call 2 → end_turn fires, inbox empty, loop exits.
//  8. Test asserts prov.prompts contains "drained turn".
func TestSession_InboxDrainDelivers(t *testing.T) {
	s := newSession("s-inbox-drain", t.TempDir(), "model")

	prov := &gatedProvider{releaseCh: make(chan struct{}, 4)}

	runCtx, runCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer runCancel()

	done := make(chan error, 1)
	go func() {
		_, err := s.Run(runCtx, RunOptions{
			Provider:    prov,
			LoopConfig:  agentloop.Config{MaxTurns: 4},
			UserMessage: "first turn",
		})
		done <- err
	}()

	// Wait for Run to install the inbox (synchronous before first
	// provider call) — Send returning nil is the readiness signal.
	deadline := time.Now().Add(2 * time.Second)
	var sendErr error
	for time.Now().Before(deadline) {
		sendErr = s.Send(InboundTurn{Text: "drained turn"})
		if sendErr == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sendErr != nil {
		t.Fatalf("Send never accepted; last err=%v", sendErr)
	}

	// Release the bootstrap turn's provider call. The loop's
	// EndTurnContinuation will then drain the inbox and inject
	// "drained turn" as a new user message, triggering call 2.
	prov.releaseCh <- struct{}{}

	// Release call 2 (drained turn). end_turn fires, inbox empty,
	// Run exits.
	prov.releaseCh <- struct{}{}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("loop did not exit within 3s")
	}

	prov.mu.Lock()
	defer prov.mu.Unlock()
	foundDrained := false
	for _, p := range prov.prompts {
		if substringMatch(p, "drained turn") {
			foundDrained = true
			break
		}
	}
	if !foundDrained {
		t.Errorf("provider never received the drained turn; saw prompts=%v", prov.prompts)
	}
	if len(prov.prompts) < 2 {
		t.Errorf("expected at least 2 provider calls (bootstrap + drained); got %d", len(prov.prompts))
	}
}

// substringMatch is a tiny helper to avoid importing strings just
// for this test.
func substringMatch(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
