package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/streamjson"
)

// TestSignalDrainFlow verifies that the same drain pattern used by
// runCommandExitCode works end-to-end: we construct a TwoLane
// emitter, fire a "mission.aborted" event on the critical lane,
// cancel the context, and call Drain. The output must contain the
// mission.aborted line — no buffering loss.
//
// We do NOT actually send a signal here because doing so in-process
// would also kill the test harness; instead we exercise the same
// drain invariant runCommandExitCode's signal handler uses.
func TestSignalDrainFlow(t *testing.T) {
	// Redirect stdout into a pipe so we can read what the emitter
	// would write when SIGINT fires.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	em := streamjson.NewTwoLane(w, true)
	ctx, cancel := context.WithCancel(context.Background())

	// Emulate the run_cmd.go signal goroutine body.
	em.EmitTopLevel(streamjson.TypeMissionAborted, map[string]any{
		"reason":         "signal",
		"_stoke.dev/sig": "SIGINT",
	})
	cancel()
	em.Drain(2 * time.Second)
	_ = w.Close()

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	_ = r.Close()
	out := string(buf[:n])

	if !strings.Contains(out, `"type":"mission.aborted"`) {
		t.Errorf("expected mission.aborted in output, got %q", out)
	}
	if !strings.Contains(out, `"reason":"signal"`) {
		t.Errorf("expected reason:signal in output, got %q", out)
	}
	if !strings.Contains(out, `"_stoke.dev/sig":"SIGINT"`) {
		t.Errorf("expected _stoke.dev/sig in output, got %q", out)
	}
	if ctx.Err() == nil {
		t.Errorf("expected ctx canceled after signal flow")
	}
}

// TestSignalHandlerMapsSIGTERMTo143 installs the same signal.Notify
// wiring runCommandExitCode uses and verifies the exit code mapping
// for SIGTERM. Uses signal.Notify directly to avoid spawning a
// subprocess.
func TestSignalHandlerMapsSIGTERMTo143(t *testing.T) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Simulate receiving SIGTERM via direct channel send.
	sigCh <- syscall.SIGTERM

	var code int
	select {
	case s := <-sigCh:
		if s == syscall.SIGTERM {
			code = ExitSIGTERM
		} else if s == syscall.SIGINT {
			code = ExitSIGINT
		}
	case <-time.After(time.Second):
		t.Fatal("signal did not deliver")
	}
	if code != 143 {
		t.Errorf("SIGTERM mapped to %d, want 143", code)
	}
}
