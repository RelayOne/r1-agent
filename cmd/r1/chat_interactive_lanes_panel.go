package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/tui/lanes"
	"github.com/RelayOne/r1/internal/workflow"
)

// chatLanesPanel owns the chat-interactive session's lane surface
// (specs/tui-lanes.md item 27, audit A073): a session-scoped hub.Bus +
// cortex.Workspace that the session emits real lane lifecycle events
// onto (main lane at startup, one tool lane per plan/execute phase),
// and the lanes.Mount hook that renders them as the Bubble Tea v2
// panel while a workflow phase runs.
//
// The SAME bus is handed to app.RunConfig.EventBus, so when the native
// runner builds its per-run deterministic cortex it shares this bus
// (engine.NativeRunner.EventBus) — any lane events emitted inside the
// run reach the panel through the identical localTransport
// subscription, no extra plumbing.
//
// Lifecycle: constructed once per REPL session when --lanes is set
// (explicit error otherwise — never a silent no-op), phase lanes open/
// close around each workflow run, Close() finalizes the main lane at
// session exit.
type chatLanesPanel struct {
	sessionID string
	bus       *hub.Bus
	ws        *cortex.Workspace
	main      *cortex.Lane

	// teaOpts is forwarded verbatim to lanes.Mount. Production leaves
	// it nil (panel binds the real TTY); tests inject
	// tea.WithInput(nil) / tea.WithOutput(io.Discard) to run headless.
	teaOpts []tea.ProgramOption

	// errOut receives panel startup/teardown diagnostics. A panel
	// failure must degrade to the plain REPL, never abort the
	// workflow. Defaults to os.Stderr.
	errOut io.Writer
}

// newChatLanesPanel validates the --lanes preconditions and builds the
// session workspace. Per audit A073: "--lanes without a workspace =
// explicit error not silent success" — both failure modes return
// errors that name the flag and the remedy.
func newChatLanesPanel(cortexEnabled, interactiveTTY bool) (*chatLanesPanel, error) {
	if !cortexEnabled {
		return nil, fmt.Errorf("--lanes: the lanes panel renders the chat session's cortex workspace and cannot run with --cortex=false; drop --cortex=false or --lanes (specs/tui-lanes.md item 27)")
	}
	if !interactiveTTY {
		return nil, fmt.Errorf("--lanes: the lanes panel requires an interactive terminal (stdin is not a TTY); re-run without --lanes")
	}

	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	sessionID := fmt.Sprintf("chat-interactive-%d", os.Getpid())
	ws.SetSessionID(sessionID)

	main := ws.NewMainLane(context.Background())
	if err := main.Transition(hub.LaneStatusRunning, "started", "chat-interactive session started"); err != nil {
		return nil, fmt.Errorf("--lanes: main lane transition: %w", err)
	}

	return &chatLanesPanel{
		sessionID: sessionID,
		bus:       bus,
		ws:        ws,
		main:      main,
		errOut:    os.Stderr,
	}, nil
}

// Close finalizes the session's main lane. Safe on nil receiver so the
// call site can defer unconditionally.
func (c *chatLanesPanel) Close() {
	if c == nil || c.main == nil {
		return
	}
	_ = c.main.Transition(hub.LaneStatusDone, "ok", "chat-interactive session ended")
}

// runUnderPanel executes one workflow phase with the lanes panel
// mounted: it opens a tool lane for the phase (child of the main
// lane), starts the panel via lanes.Mount + p.Run in a goroutine,
// runs fn, closes the phase lane with Done/Errored, then quits the
// panel and waits for the terminal to be restored before returning
// control to the line REPL.
//
// The panel is an observer: if p.Run fails (e.g. no TTY after all),
// the failure is reported on errOut and fn's result is returned
// untouched. Pressing q in the panel dismisses it early; the workflow
// keeps running.
func (c *chatLanesPanel) runUnderPanel(ctx context.Context, phase string, fn func(context.Context) (workflow.Result, error)) (workflow.Result, error) {
	lane := c.ws.NewToolLane(ctx, c.main, phase)
	if err := lane.Transition(hub.LaneStatusRunning, "started", phase+" started"); err != nil {
		fmt.Fprintf(c.errOut, "lanes: phase lane transition: %v\n", err)
	}

	p, _, cleanup := lanes.Mount(c.sessionID, lanes.NewLocalTransport(c.ws), c.teaOpts)
	panelDone := make(chan struct{})
	go func() {
		defer close(panelDone)
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(c.errOut, "lanes panel: %v (continuing without panel)\n", err)
		}
	}()

	res, err := fn(ctx)

	if err != nil {
		_ = lane.Transition(hub.LaneStatusErrored, "error", err.Error())
	} else {
		_ = lane.Transition(hub.LaneStatusDone, "ok", phase+" complete")
	}

	cleanup()
	select {
	case <-panelDone:
	case <-time.After(3 * time.Second):
		// The event loop wedged (should not happen) — force teardown
		// so the REPL gets its terminal back.
		p.Kill()
		<-panelDone
	}
	return res, err
}

// lanesEventBus returns the shared hub bus when the lanes panel is
// active, so app.New (and through it the native runner's cortex) emit
// onto the same bus the panel subscribes to. nil when --lanes is off —
// app.New treats nil EventBus as "no events", the pre-lanes behavior.
func (s *chatInteractiveSession) lanesEventBus() *hub.Bus {
	if s.lanes == nil {
		return nil
	}
	return s.lanes.bus
}
