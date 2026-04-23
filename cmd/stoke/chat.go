// Chat-driven interactive front door for Stoke.
//
// The user's #1 complaint about `./stoke` was "if I say hello, it just
// poops raw requests at me and doesn't talk back." This file is the
// fix: a smart chat session that talks conversationally, and when the
// user agrees ("ya build it", "make that a scope"), the model emits a
// dispatcher tool call that routes to the real Stoke pipeline.
//
// Wire-up:
//   launchREPL (line mode) → attachChat() → chat.Session → stokeDispatcher
//   launchShell (TUI mode) → attachChatShell() → same Session, same dispatcher
//
// The dispatcher runs synchronously on the caller's goroutine. That
// preserves the "> tool running..." ↔ "< tool done" linear feel that
// the REPL UX depends on.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ericmacdougall/stoke/internal/chat"
	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/tui"
)

// stokeDispatcher implements chat.Dispatcher by wrapping the existing
// command entry points in cmd/stoke/main.go. The methods re-use the
// smart defaults detected at startup so the chat dispatch path uses
// the same runner/model the user configured for the REPL.
//
// Each method prints a "▸ Dispatching to /X" banner (in line REPL mode)
// or appends it to the shell log (in TUI mode) before running the
// underlying command, so the user always sees which tool is firing.
type stokeDispatcher struct {
	absRepo  string
	defaults SmartDefaults

	// sh is the optional TUI shell handle. When non-nil, dispatch
	// banners go to sh.Append instead of fmt.Println, so the full-screen
	// layout is preserved. When nil, dispatches write to stdout.
	sh *tui.Shell

	// turnImages holds the image paths attached to the chat turn that
	// triggered the current dispatch. Set via SetTurnImages (from the
	// chat.RunToolCallWithImages path) immediately before the
	// underlying Scope/Build/Ship/... method runs; cleared on the next
	// dispatch so images never bleed across turns.
	turnImages []string
}

// SetTurnImages records the chat turn's image attachments so subsequent
// dispatch methods can weave the paths into the downstream prompt. A
// nil or empty slice clears the set. Implements
// chat.ImageAwareDispatcher.
func (d *stokeDispatcher) SetTurnImages(paths []string) {
	if len(paths) == 0 {
		d.turnImages = nil
		return
	}
	d.turnImages = append([]string(nil), paths...)
}

// decorateWithImages appends a short "user attached these screenshots"
// section to the description so the downstream worker's prompt
// references the files on disk. Returns description unchanged when no
// images were attached to the triggering turn.
func (d *stokeDispatcher) decorateWithImages(description string) string {
	if len(d.turnImages) == 0 {
		return description
	}
	// Log the paths so the operator can audit what visual context the
	// worker was handed. Goes through the shell log when in TUI mode,
	// stdout otherwise.
	d.announce("▸ forwarding %d image(s) to worker: %s", len(d.turnImages), strings.Join(d.turnImages, ", "))
	if strings.TrimSpace(description) == "" {
		return "User attached these screenshots when requesting this work: " + strings.Join(d.turnImages, ", ")
	}
	return description + "\n\nUser attached these screenshots when requesting this work: " + strings.Join(d.turnImages, ", ")
}

// announce writes a short "dispatching..." banner so the user sees what
// the chat just triggered. Works in both REPL and TUI modes.
func (d *stokeDispatcher) announce(format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)
	if d.sh != nil {
		d.sh.Append("%s", line)
		return
	}
	fmt.Printf("\n  %s\n\n", line)
}

// nativeArgs returns the runner flags that come from smart defaults.
// These are appended to every dispatch so the chat-driven pipeline
// uses the same runner the REPL was started with.
func (d *stokeDispatcher) nativeArgs() []string {
	var a []string
	a = append(a, "--runner", d.defaults.RunnerMode)
	if d.defaults.NativeBaseURL != "" {
		a = append(a, "--native-base-url", d.defaults.NativeBaseURL)
	}
	if d.defaults.NativeAPIKey != "" {
		a = append(a, "--native-api-key", d.defaults.NativeAPIKey)
	}
	if d.defaults.NativeModel != "" {
		a = append(a, "--native-model", d.defaults.NativeModel)
	}
	return a
}

// runCaptured runs fn. In the TUI shell path the caller (launchShell's
// handler) already wraps the whole turn in captureStdoutTo, so we do
// NOT re-wrap here — nested captures would swap os.Stdout mid-stream
// and split output across two reader goroutines. In line-REPL mode
// there is no capture; fn writes directly to the terminal.
//
// Kept as a named helper because I may want to re-introduce capture
// here in the future (e.g. if the REPL path ever gains a log sink).
func (d *stokeDispatcher) runCaptured(fn func()) {
	fn()
}

func (d *stokeDispatcher) Scope(description string) (string, error) {
	description = d.decorateWithImages(description)
	d.announce("▸ Dispatching to /scope: %s", truncOne(description, 80))
	args := []string{"--repo", d.absRepo}
	if strings.TrimSpace(description) != "" {
		// Pass the chat-agreed description through --task so the
		// scope session's CLAUDE.md seeds the interactive Claude
		// Code instance with the brief. Without this, the user
		// would have to restate the context they just agreed on
		// with Stoke.
		args = append(args, "--task", description)
	}
	d.runCaptured(func() {
		scopeCmd(args)
	})
	return fmt.Sprintf("Started /scope session for: %s", description), nil
}

func (d *stokeDispatcher) Build(description string) (string, error) {
	if strings.TrimSpace(description) == "" {
		return "", fmt.Errorf("build needs a description")
	}
	description = d.decorateWithImages(description)
	d.announce("▸ Dispatching to /run: %s", truncOne(description, 80))
	args := append([]string{"--task", description, "--repo", d.absRepo}, d.nativeArgs()...)
	d.runCaptured(func() {
		runCmdSafe(args)
	})
	return fmt.Sprintf("/run pipeline finished for: %s. See the output above for the build/verify result.", description), nil
}

func (d *stokeDispatcher) Ship(description string) (string, error) {
	if strings.TrimSpace(description) == "" {
		return "", fmt.Errorf("ship needs a description")
	}
	description = d.decorateWithImages(description)
	d.announce("▸ Dispatching to /ship: %s", truncOne(description, 80))
	d.runCaptured(func() {
		shipCmd([]string{"--task", description, "--repo", d.absRepo})
	})
	return fmt.Sprintf("/ship loop finished for: %s. See the output above for the ship-ready result.", description), nil
}

func (d *stokeDispatcher) Plan(description string) (string, error) {
	if strings.TrimSpace(description) == "" {
		return "", fmt.Errorf("plan needs a description")
	}
	description = d.decorateWithImages(description)
	d.announce("▸ Dispatching to /plan: %s", truncOne(description, 80))
	d.runCaptured(func() {
		planCmd([]string{"--task", description, "--repo", d.absRepo})
	})
	return fmt.Sprintf("/plan generated for: %s. See the plan output above.", description), nil
}

func (d *stokeDispatcher) Audit() (string, error) {
	d.announce("▸ Dispatching to /audit")
	d.runCaptured(func() {
		auditCmd([]string{"--repo", d.absRepo})
	})
	return "/audit finished. See the persona review above.", nil
}

func (d *stokeDispatcher) Scan(securityOnly bool) (string, error) {
	label := "/scan"
	args := []string{"--repo", d.absRepo}
	if securityOnly {
		label = "/scan --security"
		args = append(args, "--security")
	}
	d.announce("▸ Dispatching to %s", label)
	d.runCaptured(func() {
		scanCmd(args)
	})
	return fmt.Sprintf("%s finished. See the scan results above.", label), nil
}

func (d *stokeDispatcher) Status() (string, error) {
	d.announce("▸ Dispatching to /status")
	d.runCaptured(func() {
		statusCmd([]string{"--repo", d.absRepo})
	})
	return "/status shown above.", nil
}

// SOW dispatches an on-disk Statement of Work file through the
// multi-session SOW pipeline. The chat-mode entry point: the user has
// a structured spec they want stoke to build, the model agrees, and
// chat hands off to sowCmd with the same runner config the chat is
// using. The pipeline takes over from there — sessions, acceptance
// gates, repair attempts, the lot.
// chatStdinClarifyResponder returns a ChatResponder that renders
// clarify questions to stdout (or the TUI shell log) and reads the
// user's answer from stdin on a background goroutine. Used by the
// chat-mode SOW dispatch path so dispatched workers can surface
// questions to the user instead of synthesizing answers from a
// supervisor LLM.
//
// The responder runs one stdin-reader goroutine for the lifetime of
// the SOW dispatch; the goroutine exits when stop() is returned by
// the caller (via the returned cleanup).
func (d *stokeDispatcher) chatStdinClarifyResponder() (*chat.ChatResponder, func()) {
	resp := &chat.ChatResponder{
		Prompter: func(req plan.ClarifyRequest) {
			banner := chat.FormatClarifyPrompt(req)
			if d.sh != nil {
				for _, line := range strings.Split(banner, "\n") {
					d.sh.Append("%s", line)
				}
				return
			}
			fmt.Print(banner)
		},
	}

	// In TUI mode (d.sh != nil) Bubble Tea owns os.Stdin and we
	// MUST NOT spawn a parallel reader — it would either steal
	// keystrokes from the TUI or sit blocked forever. The clarify
	// question renders into the shell log via the Prompter above;
	// the user's typed answer comes in via the TUI's normal input
	// path, which must call resp.Submit() when it sees a message
	// starting with "/clarify " (or equivalent). If that wiring
	// isn't present, clarify times out gracefully to the supervisor
	// LLM fallback — that's the honest failure mode, not a
	// stolen-keystroke bug.
	if d.sh != nil {
		// TUI mode: the Bubble Tea shell owns input and does not
		// yet route typed lines into resp.Submit(). Returning nil
		// here is critical — the call site (SOW dispatch) installs
		// the returned responder via SetActiveClarifyResponder,
		// and resolveClarifyResponder PREFERS the active responder
		// over the supervisor-LLM fallback. If we returned a live
		// responder with no input wiring, every worker clarify
		// would block for the full 5-min DefaultChatClarifyTimeout
		// before falling through to UNKNOWN. Returning nil lets
		// resolveClarifyResponder fall straight through to the
		// supervisor responder for headless-equivalent behavior.
		// Operator gets a one-time warning so they know clarifies
		// won't reach them in TUI mode until input routing lands.
		d.sh.Append("%s", "  ⚠ clarify: TUI input routing isn't wired yet — worker clarifications will fall through to supervisor-LLM. Use `stoke chat --no-tui` if you want to answer directly.")
		return nil, func() {}
	}

	// Line-REPL mode: the REPL input loop isn't running during SOW
	// dispatch (we're inside the dispatcher call), so we can safely
	// own stdin for the dispatch window. Poll Pending() at a low
	// frequency; when a question is waiting, block on ReadString;
	// discard orphaned reads after shutdown via the `drained` flag
	// so a late-arriving keystroke can't leak into a subsequent
	// REPL command.
	stopCh := make(chan struct{})
	var drained atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reader := bufio.NewReader(os.Stdin)
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			_, pending := resp.Pending()
			if !pending {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			line, err := reader.ReadString('\n')
			if drained.Load() {
				// Cleanup fired while we were blocked in ReadString.
				// The line the user typed belonged to nothing — the
				// clarify already timed out. Print a visible notice
				// to stderr so the user knows their input was
				// discarded and can re-enter the intended REPL
				// command, rather than silently losing it.
				if err == nil && strings.TrimSpace(line) != "" {
					fmt.Fprintf(os.Stderr, "  ⚠ discarded after clarify timeout: %q — re-enter if you meant this for the REPL.\n", strings.TrimSpace(line))
				}
				return
			}
			if err != nil {
				return
			}
			if err := resp.Submit(line); err != nil {
				continue
			}
		}
	}()

	cleanup := func() {
		drained.Store(true)
		close(stopCh)
		// Wait briefly for the goroutine to actually exit. Without
		// this, a second SOW dispatch could install a new responder
		// while the OLD goroutine is still blocked in ReadString,
		// stealing the next REPL input. We give it 1.5s — enough
		// for a Pending() poll to come around AND see drained,
		// short enough that a wedged terminal doesn't hang the
		// REPL. After the timeout we proceed regardless; the
		// drained flag still discards any late-arriving line.
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(1500 * time.Millisecond):
		}
	}
	return resp, cleanup
}

func (d *stokeDispatcher) SOW(filePath string) (string, error) {
	if strings.TrimSpace(filePath) == "" {
		return "", fmt.Errorf("dispatch_sow needs a file_path")
	}
	if _, err := os.Stat(filePath); err != nil {
		return "", fmt.Errorf("SOW file not readable: %w", err)
	}
	d.announce("▸ Dispatching to /sow: %s", filePath)
	if len(d.turnImages) > 0 {
		// SOW dispatches by file path, so we surface the image paths
		// to the operator via the log. The native SOW runner does not
		// yet accept image attachments directly; forwarding requires a
		// second pass (adding --image flags to sowCmd).
		d.announce("▸ user attached %d image(s) with SOW dispatch: %s", len(d.turnImages), strings.Join(d.turnImages, ", "))
	}
	args := append(
		[]string{"--repo", d.absRepo, "--file", filePath},
		d.nativeArgs()...,
	)

	// Install a chat-mode clarification responder so any worker the
	// SOW spawns routes request_clarification to the user instead of
	// to a synthetic supervisor LLM. Cleared when the dispatch
	// returns. Note: a nil responder MUST NOT be installed — Go's
	// typed-nil-interface gotcha would make resolveClarifyResponder
	// see the responder as non-nil and prefer it over the supervisor
	// fallback (which is the bug we're avoiding in TUI mode).
	responder, stopReader := d.chatStdinClarifyResponder()
	if responder != nil {
		SetActiveClarifyResponder(responder)
		defer SetActiveClarifyResponder(nil)
	}
	defer stopReader()

	d.runCaptured(func() {
		sowCmd(args)
	})
	return fmt.Sprintf("/sow pipeline finished for: %s. See the session-by-session output above.", filePath), nil
}

// truncOne truncates s to max runes on a single line with an ellipsis.
func truncOne(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// buildChatSession is the common path that REPL and shell use to stand
// up a chat session from the detected smart defaults. Returns nil, nil
// (session AND error both nil) in the "no provider available" case —
// the caller falls back to the old "free text runs as a task" behavior
// with a warning.
//
// repoRoot is the absolute path of the working repo. It anchors the
// post-turn descent gate (CDC-1..CDC-5): the gate captures
// `git rev-parse HEAD` here at session construction and uses that SHA
// as the baseline for "has this turn dirtied source files?". Pass ""
// (or a non-git path) to disable the gate — useful for tests and
// no-git chat targets.
func buildChatSession(defaults SmartDefaults, repoRoot string) (*chat.Session, error) {
	p, err := chat.NewProviderFromOptions(chat.ProviderOptions{
		BaseURL: defaults.NativeBaseURL,
		APIKey:  defaults.NativeAPIKey,
		Model:   defaults.NativeModel,
	})
	if err != nil {
		// No provider is a soft failure — the caller decides what to
		// do. Return nil so the REPL banner can print a "chat is
		// unavailable" note instead of crashing.
		return nil, err
	}
	model := defaults.NativeModel
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	// CDC-5: capture the start commit so the gate can detect turn-level
	// edits. Empty SHA disables the gate (no-git is a valid chat
	// target). STOKE_CHAT_DESCENT=0 lets operators flip the gate off
	// without rebuilding.
	var gate *chat.DescentGate
	if repoRoot != "" && os.Getenv("STOKE_CHAT_DESCENT") != "0" {
		startCommit := chat.CaptureStartCommit(context.Background(), repoRoot)
		if startCommit != "" {
			gate = &chat.DescentGate{
				Repo:        repoRoot,
				StartCommit: startCommit,
				// OnLog is nil — Session.Send streams gate lines
				// through onDelta directly so they land in the
				// REPL/shell stream alongside the assistant text.
				// RepairFunc and Ask are filled by later CDC tasks
				// (chat repair-once + Operator wiring CDC-16).
			}
		} else {
			log.Printf("chat: descent gate disabled (no git start commit found at %s)", repoRoot)
		}
	}

	session, err := chat.NewSession(p, chat.Config{
		Model:     model,
		MaxTokens: 6000,
		Tools:     chat.DispatcherTools(),
		Gate:      gate,
		RepoRoot:  repoRoot,
	})
	if err != nil {
		return nil, fmt.Errorf("build chat session: %w", err)
	}
	return session, nil
}

// chatOnceREPL runs one chat turn for the line REPL. It streams the
// reply to stdout with a subtle prefix, handles dispatcher tool calls
// via stokeDispatcher, and prints a friendly error if anything fails.
//
// Called from launchREPL.OnChat. Keeps the line REPL's linear feel
// intact: "> user typed this", "assistant replies", repeat.
func chatOnceREPL(ctx context.Context, session *chat.Session, disp *stokeDispatcher, input string) {
	if session == nil {
		// No provider. Fall back to a single-shot /run, the old
		// behavior, and explain why.
		fmt.Println("  (chat unavailable — no provider detected. Dispatching as a /run task instead.)")
		fmt.Println()
		_, _ = disp.Build(input)
		return
	}

	// Visual: one blank line, then the assistant line (monochrome is
	// kinder to colored terminals than a fixed ANSI color).
	fmt.Print("\n  \033[90m●\033[0m ")
	firstChunk := true
	onDelta := func(delta string) {
		if firstChunk {
			firstChunk = false
		}
		// Indent subsequent lines so the reply visually hangs under
		// the "● " marker.
		fmt.Print(strings.ReplaceAll(delta, "\n", "\n    "))
	}

	onDispatch := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		// Announce the dispatch on its own line so streaming text
		// and the dispatch banner don't interleave visually.
		fmt.Println()
		// Forward the turn's image attachments to the dispatcher so
		// Build/Ship/Plan/Scope can fold the paths into the worker's
		// prompt context.
		return chat.RunToolCallWithImages(disp, name, input, session.LastTurnImages())
	}

	result, err := session.Send(ctx, input, onDelta, onDispatch)
	fmt.Println()
	if err != nil {
		fmt.Printf("  \033[31mchat error:\033[0m %v\n", err)
		return
	}
	if result != nil && len(result.DispatchedTools) > 0 {
		// Show a small summary line so the user knows what fired.
		var names []string
		for _, d := range result.DispatchedTools {
			names = append(names, strings.TrimPrefix(d.Name, "dispatch_"))
		}
		fmt.Printf("  \033[90m(dispatched: %s)\033[0m\n", strings.Join(names, ", "))
	}
	fmt.Println()
}

// chatOnceShell runs one chat turn from the full-screen TUI shell. The
// reply text streams into the shell's log pane via shell.StreamChunk
// (buffered per line) and dispatcher tool calls route through
// stokeDispatcher with sh wired in so output goes to the log pane.
func chatOnceShell(ctx context.Context, sh *tui.Shell, session *chat.Session, disp *stokeDispatcher, input string) {
	if session == nil {
		sh.Append("  (chat unavailable — no provider. Treating as a /run task.)")
		_, _ = disp.Build(input)
		return
	}
	// The dispatcher uses sh.Append for its banner, but the
	// runCaptured helper also redirects stdout/stderr into the shell
	// log while commands run — so subcommand output lands in the log
	// pane automatically.
	onDelta := func(delta string) {
		sh.StreamChunk(delta)
	}
	onDispatch := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		sh.StreamFinish() // flush any in-progress text line first
		return chat.RunToolCallWithImages(disp, name, input, session.LastTurnImages())
	}

	// Mark the start of the chat reply with a leading bullet so it's
	// visually distinct from command output.
	sh.Append("● (chat)")
	result, err := session.Send(ctx, input, onDelta, onDispatch)
	sh.StreamFinish()
	if err != nil {
		sh.Append("  chat error: %v", err)
		return
	}
	if result != nil && len(result.DispatchedTools) > 0 {
		var names []string
		for _, d := range result.DispatchedTools {
			names = append(names, strings.TrimPrefix(d.Name, "dispatch_"))
		}
		sh.Append("  (dispatched: %s)", strings.Join(names, ", "))
	}
}

// --- provider diag: print a one-line hint when chat cannot start ---

// describeChatFailure turns a provider-build error into a terse,
// actionable explanation for the banner.
func describeChatFailure(err error) string {
	if err == nil {
		return ""
	}
	if err == chat.ErrNoProvider {
		return "chat mode: unavailable — set ANTHROPIC_API_KEY or run a LiteLLM proxy"
	}
	return "chat mode: " + err.Error()
}

// providerHint returns a short human-readable description of where
// chat will get its responses from. Used by the REPL startup banner.
func providerHint(defaults SmartDefaults) string {
	if defaults.NativeBaseURL != "" {
		return fmt.Sprintf("via %s", defaults.NativeBaseURL)
	}
	if defaults.NativeAPIKey != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "via api.anthropic.com"
	}
	return "(no provider — set ANTHROPIC_API_KEY or run LiteLLM)"
}

// compile-time assertion that stokeDispatcher satisfies chat.Dispatcher.
var _ chat.Dispatcher = (*stokeDispatcher)(nil)

// compile-time assertion that provider.Provider is the type we return.
var _ provider.Provider = (*provider.AnthropicProvider)(nil)
