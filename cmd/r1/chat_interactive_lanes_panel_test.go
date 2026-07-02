// Tests for the --lanes chat-interactive wiring (audit A073 /
// specs/tui-lanes.md item 27): explicit precondition errors, phase
// lane lifecycle on the session cortex workspace, and the headless
// panel mount around a workflow phase.
package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/workflow"
)

// TestNewChatLanesPanel_RequiresCortex: --lanes with --cortex=false is
// an explicit error naming both flags, not a silent no-op.
func TestNewChatLanesPanel_RequiresCortex(t *testing.T) {
	_, err := newChatLanesPanel(false, true)
	if err == nil {
		t.Fatal("newChatLanesPanel(cortex=false) = nil error, want explicit --lanes error")
	}
	if !strings.Contains(err.Error(), "--lanes") || !strings.Contains(err.Error(), "--cortex=false") {
		t.Fatalf("error must name --lanes and --cortex=false, got: %v", err)
	}
}

// TestNewChatLanesPanel_RequiresTTY: --lanes on a non-interactive
// stdin is an explicit error.
func TestNewChatLanesPanel_RequiresTTY(t *testing.T) {
	_, err := newChatLanesPanel(true, false)
	if err == nil {
		t.Fatal("newChatLanesPanel(tty=false) = nil error, want explicit --lanes error")
	}
	if !strings.Contains(err.Error(), "--lanes") || !strings.Contains(err.Error(), "TTY") {
		t.Fatalf("error must name --lanes and the TTY requirement, got: %v", err)
	}
}

// TestRunChatInteractiveCmd_LanesWithoutCortexIsExplicitError proves
// the command-level consumption: with the package flag set (as the
// init() strip does for `r1 chat-interactive --lanes`), the command
// fails fast instead of silently ignoring the flag — the exact defect
// audit A073 verified ("a documented CLI flag is accepted and
// silently ignored").
func TestRunChatInteractiveCmd_LanesWithoutCortexIsExplicitError(t *testing.T) {
	prev := chatInteractiveLanesEnabled
	chatInteractiveLanesEnabled = true
	t.Cleanup(func() { chatInteractiveLanesEnabled = prev })

	err := runChatInteractiveCmd([]string{"--repo", t.TempDir(), "--cortex=false"})
	if err == nil {
		t.Fatal("runChatInteractiveCmd(--lanes --cortex=false) = nil error, want explicit error")
	}
	if !strings.Contains(err.Error(), "--lanes") {
		t.Fatalf("error must name --lanes, got: %v", err)
	}
}

// headlessPanel builds a chatLanesPanel whose Bubble Tea program runs
// without a TTY (input disabled, output discarded).
func headlessPanel(t *testing.T) *chatLanesPanel {
	t.Helper()
	c, err := newChatLanesPanel(true, true)
	if err != nil {
		t.Fatalf("newChatLanesPanel: %v", err)
	}
	c.teaOpts = []tea.ProgramOption{
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutSignals(),
		tea.WithoutSignalHandler(),
		tea.WithWindowSize(100, 30),
	}
	c.errOut = io.Discard
	return c
}

// findPhaseLaneStatus returns the status of the tool lane labelled
// phase on the panel's workspace, or "" if absent.
func findPhaseLaneStatus(c *chatLanesPanel, phase string) hub.LaneStatus {
	for _, l := range c.ws.Lanes() {
		snap := l.Snapshot()
		if snap.Kind == hub.LaneKindTool && snap.Label == phase {
			return snap.Status
		}
	}
	return ""
}

// TestRunUnderPanel_PhaseLaneLifecycle runs a workflow fn under the
// mounted panel and asserts the phase lane transitions Running→Done on
// success and Running→Errored on failure, with the fn result passed
// through untouched.
func TestRunUnderPanel_PhaseLaneLifecycle(t *testing.T) {
	c := headlessPanel(t)
	defer c.Close()
	ctx := context.Background()

	// Success path.
	want := workflow.Result{PlanOutput: "the plan"}
	start := time.Now()
	res, err := c.runUnderPanel(ctx, "plan", func(context.Context) (workflow.Result, error) {
		return want, nil
	})
	if err != nil {
		t.Fatalf("runUnderPanel(success fn): %v", err)
	}
	if res.PlanOutput != want.PlanOutput {
		t.Fatalf("result not passed through: got %q want %q", res.PlanOutput, want.PlanOutput)
	}
	if got := findPhaseLaneStatus(c, "plan"); got != hub.LaneStatusDone {
		t.Fatalf("plan lane status = %q, want %q", got, hub.LaneStatusDone)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("runUnderPanel did not tear the panel down promptly (%v)", elapsed)
	}

	// Failure path.
	wantErr := errors.New("execute blew up")
	_, err = c.runUnderPanel(ctx, "execute", func(context.Context) (workflow.Result, error) {
		return workflow.Result{}, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runUnderPanel(failing fn) err = %v, want %v", err, wantErr)
	}
	if got := findPhaseLaneStatus(c, "execute"); got != hub.LaneStatusErrored {
		t.Fatalf("execute lane status = %q, want %q", got, hub.LaneStatusErrored)
	}
}

// TestChatLanesPanel_CloseFinalizesMainLane asserts Close transitions
// the session main lane to a terminal state and is nil-safe.
func TestChatLanesPanel_CloseFinalizesMainLane(t *testing.T) {
	c := headlessPanel(t)
	c.Close()
	if got := c.main.Snapshot().Status; got != hub.LaneStatusDone {
		t.Fatalf("main lane status after Close = %q, want %q", got, hub.LaneStatusDone)
	}
	var nilPanel *chatLanesPanel
	nilPanel.Close() // must not panic
}

// TestLanesEventBus_NilWhenDisabled pins the RunConfig junction: no
// panel → nil bus (app.New's "no events" path), panel → the session
// bus the panel subscribes to.
func TestLanesEventBus_NilWhenDisabled(t *testing.T) {
	s := &chatInteractiveSession{}
	if s.lanesEventBus() != nil {
		t.Fatal("lanesEventBus() must be nil without --lanes")
	}
	c := headlessPanel(t)
	defer c.Close()
	s.lanes = c
	if s.lanesEventBus() != c.bus {
		t.Fatal("lanesEventBus() must return the session lane bus when --lanes is active")
	}
}
