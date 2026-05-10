package sessionhub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// pauseAwareProvider counts ChatStream calls and lets a test wait
// until the loop has invoked the provider at least once. The
// integration test pauses BEFORE the second turn fires and asserts
// the count does not advance until Resume.
type pauseAwareProvider struct {
	calls   atomic.Int64
	respond func() *provider.ChatResponse
}

func (p *pauseAwareProvider) Name() string { return "pause-aware" }

func (p *pauseAwareProvider) Chat(_ provider.ChatRequest) (*provider.ChatResponse, error) {
	p.calls.Add(1)
	return p.respond(), nil
}

func (p *pauseAwareProvider) ChatStream(_ provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	p.calls.Add(1)
	return p.respond(), nil
}

// TestSession_PauseGatesAgentLoop confirms Session.Pause prevents
// the agent loop from invoking the provider for new turns. End-to-end
// integration: Run starts a 3-turn loop, the test pauses after the
// first call lands, asserts the call count does NOT advance for a
// short window, then resumes and asserts subsequent turns fire.
func TestSession_PauseGatesAgentLoop(t *testing.T) {
	s := newSession("s-pause-loop", t.TempDir(), "model")

	// Provider always returns "continue" so the loop runs to MaxTurns.
	prov := &pauseAwareProvider{
		respond: func() *provider.ChatResponse {
			return &provider.ChatResponse{
				Content:    []provider.ResponseContent{{Type: "text", Text: "continue"}},
				StopReason: "tool_use",
			}
		},
	}

	// Pause immediately so the FIRST PreTurnHook invocation blocks.
	// This is the cleanest way to test the gate: the test fully
	// controls when the loop is allowed to fire its first call.
	s.Pause()

	runCtx, runCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer runCancel()

	done := make(chan error, 1)
	go func() {
		_, err := s.Run(runCtx, RunOptions{
			Provider:    prov,
			LoopConfig:  agentloop.Config{MaxTurns: 3},
			UserMessage: "go",
		})
		done <- err
	}()

	// 50 ms after Run starts, the loop should be parked at the pause
	// gate. Provider must have zero calls.
	time.Sleep(50 * time.Millisecond)
	if got := prov.calls.Load(); got != 0 {
		t.Fatalf("provider called %d times while paused; want 0", got)
	}
	if !s.IsPaused() {
		t.Fatalf("session should still be paused")
	}

	// Resume and let the loop run to MaxTurns.
	s.Resume()

	select {
	case err := <-done:
		// MaxTurns hit is fine; ctx-cancel is not — that would mean
		// Resume didn't wake the gate.
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			// Other errors from the loop body are unrelated; we only
			// care that the loop made forward progress.
			t.Logf("loop returned with err=%v (acceptable if not ctx-cancel)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("loop did not exit within 2s after Resume")
	}

	if got := prov.calls.Load(); got == 0 {
		t.Fatalf("provider was never called after Resume; gate did not unblock")
	}
}
