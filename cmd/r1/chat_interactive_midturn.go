package main

// Mid-turn input routing for the chat-interactive REPL (audit A062;
// specs/cortex-core.md §"OnUserInputMidTurn").
//
// While an execute turn is in flight the operator can keep typing:
// each line is routed through cortex.Cortex.OnUserInputMidTurn, whose
// Router (Haiku 4.5, 4 tools) decides interrupt / steer /
// queue_mission / just_chat:
//
//   - interrupt     → the turn's CancelFunc fires; the run unwinds and
//     the REPL prompts for the next task.
//   - steer         → a Note lands in the REPL cortex Workspace and a
//     confirmation is printed. NOTE: this Workspace is REPL-local; the
//     engine-side cortex the native runner builds for the in-flight
//     loop drains its own Workspace, so steer notes surface to the
//     operator but are not yet injected into the running agent (that
//     needs a shared-Workspace seam through app/workflow/engine).
//   - queue_mission → the brief is queued; the REPL runs it (through
//     the normal plan → approve → execute flow) after the current task.
//   - just_chat     → the reply is printed; nothing else happens.
//
// All of this machinery is inert when midTurn == nil (no --cortex, or
// no provider reachable): the session then reads the scanner directly,
// exactly as it did before this file existed.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/chat"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/workflow"
)

// midTurnRouter is the narrow seam chatInteractiveSession needs from
// *cortex.Cortex, so tests can inject a fake without standing up a
// provider.
type midTurnRouter interface {
	OnUserInputMidTurn(ctx context.Context, userInput string, turnCancel context.CancelFunc) (cortex.RouterDecision, error)
}

// Compile-time proof the real cortex satisfies the seam.
var _ midTurnRouter = (*cortex.Cortex)(nil)

// buildMidTurnCortex constructs a router-only cortex (no Lobes, never
// Started — no goroutines, no pre-warm traffic) for mid-turn input
// routing. Returns nil when no provider is reachable or construction
// fails; mid-turn routing is then silently disabled.
//
// When a proxy BaseURL is configured the Router model follows
// cfg.NativeModel (the proxy is only known to serve that); otherwise
// NewRouter's spec default (claude-haiku-4-5) applies.
func buildMidTurnCortex(cfg chatInteractiveConfig) *cortex.Cortex {
	p, err := chat.NewProviderFromOptions(chat.ProviderOptions{
		BaseURL: cfg.NativeBaseURL,
		APIKey:  cfg.NativeAPIKey,
		Model:   cfg.NativeModel,
	})
	if err != nil {
		return nil
	}
	rcfg := cortex.RouterConfig{}
	if cfg.NativeBaseURL != "" && cfg.NativeModel != "" {
		rcfg.Model = cfg.NativeModel
	}
	c, err := cortex.New(cortex.Config{
		EventBus:  hub.New(),
		Provider:  p,
		RouterCfg: rcfg,
		// Belt-and-braces: the pre-warm pump only runs after Start and
		// this cortex is never Started, but keep the interval long so
		// an accidental future Start stays cheap.
		PreWarmInterval: time.Hour,
	})
	if err != nil {
		slog.Warn("chat-interactive: mid-turn cortex construction failed; mid-turn routing disabled", "err", err)
		return nil
	}
	return c
}

// startInputPump launches the single goroutine that owns the scanner
// for the lifetime of the session, forwarding each line onto s.lines.
// Required for mid-turn routing: while a turn is in flight the
// listener in executeWithMidTurn consumes lines and between turns
// prompt() does — one pump means the two never race on one
// bufio.Scanner.
func (s *chatInteractiveSession) startInputPump() {
	s.lines = make(chan string)
	go func() {
		defer close(s.lines)
		for s.in.Scan() {
			s.lines <- s.in.Text()
		}
		// Written before the deferred close; prompt() reads it only
		// after observing the closed channel, so this is race-free.
		s.readErr = s.in.Err()
	}()
}

// nextTask returns the next task brief: a queue_mission brief queued
// during the previous turn takes precedence over prompting the
// operator. Queued briefs still go through the normal plan → approve →
// execute flow, so nothing runs without operator sign-off.
func (s *chatInteractiveSession) nextTask() (string, error) {
	if brief := s.popQueued(); brief != "" {
		fmt.Fprintf(s.out, "%s\n", chatDimStyle.Render("▸ starting queued task: "+brief))
		return brief, nil
	}
	return s.prompt("task> ")
}

// popQueued removes and returns the oldest queued brief, or "" when
// the queue is empty.
func (s *chatInteractiveSession) popQueued() string {
	s.queuedMu.Lock()
	defer s.queuedMu.Unlock()
	if len(s.queued) == 0 {
		return ""
	}
	brief := s.queued[0]
	s.queued = s.queued[1:]
	return brief
}

// executeWithMidTurn runs the execute workflow while concurrently
// routing any lines the operator types through the cortex mid-turn
// Router. Degrades to a plain execFn call when mid-turn routing is
// disabled.
//
// Output-safety invariant: the listener goroutine writes to s.out only
// while the main goroutine is blocked inside execFn (which never
// writes to s.out), and executeWithMidTurn joins the listener before
// returning — so s.out sees no concurrent writers.
func (s *chatInteractiveSession) executeWithMidTurn(ctx context.Context, task string) (workflow.Result, error) {
	if s.midTurn == nil || s.lines == nil {
		return s.execFn(ctx, task)
	}

	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	stop := make(chan struct{})
	listenerDone := make(chan struct{})
	go func() {
		defer close(listenerDone)
		for {
			select {
			case <-stop:
				return
			case line, ok := <-s.lines:
				if !ok {
					return
				}
				// The turn may have completed between the pump's send
				// and this receive. Don't route stale input through a
				// finished turn — hand it back to the main prompt loop
				// (read after listenerDone, so no lock is needed).
				select {
				case <-stop:
					l := line
					s.pendingLine = &l
					return
				default:
				}
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					continue
				}
				s.routeMidTurn(turnCtx, trimmed, cancelTurn)
			}
		}
	}()

	res, err := s.execFn(turnCtx, task)
	close(stop)
	<-listenerDone
	return res, err
}

// routeMidTurn hands one mid-turn line to the cortex Router and prints
// the outcome. Runs on the listener goroutine in executeWithMidTurn.
func (s *chatInteractiveSession) routeMidTurn(ctx context.Context, line string, cancelTurn context.CancelFunc) {
	dec, err := s.midTurn.OnUserInputMidTurn(ctx, line, cancelTurn)
	if err != nil {
		fmt.Fprintf(s.out, "\n%s\n", chatWarnStyle.Render("mid-turn router: "+err.Error()))
		return
	}
	switch dec.Kind {
	case cortex.DecisionInterrupt:
		reason := ""
		if dec.Interrupt != nil {
			reason = dec.Interrupt.Reason
		}
		fmt.Fprintf(s.out, "\n%s\n", chatWarnStyle.Render("⚡ interrupting current turn: "+reason))
	case cortex.DecisionSteer:
		title := ""
		if dec.Steer != nil {
			title = dec.Steer.Title
		}
		fmt.Fprintf(s.out, "\n%s\n", chatDimStyle.Render("steer noted: "+title))
	case cortex.DecisionQueueMission:
		if dec.Queue != nil && strings.TrimSpace(dec.Queue.Brief) != "" {
			brief := strings.TrimSpace(dec.Queue.Brief)
			s.queuedMu.Lock()
			s.queued = append(s.queued, brief)
			s.queuedMu.Unlock()
			fmt.Fprintf(s.out, "\n%s\n", chatDimStyle.Render("queued for after this task: "+brief))
		}
	case cortex.DecisionJustChat:
		if dec.JustChat != nil && dec.JustChat.Reply != "" {
			fmt.Fprintf(s.out, "\n%s\n", chatDimStyle.Render(dec.JustChat.Reply))
		}
	}
}
