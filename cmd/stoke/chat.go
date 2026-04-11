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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ericmacdougall/stoke/internal/chat"
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
func (d *stokeDispatcher) SOW(filePath string) (string, error) {
	if strings.TrimSpace(filePath) == "" {
		return "", fmt.Errorf("dispatch_sow needs a file_path")
	}
	if _, err := os.Stat(filePath); err != nil {
		return "", fmt.Errorf("SOW file not readable: %w", err)
	}
	d.announce("▸ Dispatching to /sow: %s", filePath)
	args := append(
		[]string{"--repo", d.absRepo, "--file", filePath},
		d.nativeArgs()...,
	)
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
func buildChatSession(defaults SmartDefaults) (*chat.Session, error) {
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
	session, err := chat.NewSession(p, chat.Config{
		Model:     model,
		MaxTokens: 2048,
		Tools:     chat.DispatcherTools(),
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
		return chat.RunToolCall(disp, name, input)
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
		return chat.RunToolCall(disp, name, input)
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
