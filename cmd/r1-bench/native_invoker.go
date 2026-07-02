package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/bench"
	"github.com/RelayOne/r1/internal/bench/agents"
	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// SOTA gap #1: make r1 self-benchmarkable end-to-end.
//
// The R1Dispatcher had only notWiredR1Invoker, so the truthful-completion
// benchmark — r1's headline claim — could never actually run r1; every
// score was an inert stub. NativeInvoker is the production R1ModelInvoker:
// it drives the same native agentloop (engine.NativeRunner) the real
// `r1 build` uses, against the mission's working tree, and maps the run
// outcome into the Trace fields the scorer consumes.
//
// It is CI-testable without model credentials or docker via
// NativeRunner.ProviderOverride (a mock provider), which is exactly why
// that seam exists — see native_invoker_test.go.
type NativeInvoker struct {
	// Model is the native model id (e.g. "sonnet", "opus", or a
	// provider-specific slug).
	Model string
	// APIKey and BaseURL configure the real provider. Empty APIKey +
	// no ProviderOverride means the run will fail at the first model
	// call — the invoker never fakes a completion.
	APIKey  string
	BaseURL string
	// ProviderOverride, when set, replaces the auto-detected provider.
	// Tests inject a mock here; production leaves it nil (or sets a
	// claude-code:// / codex:// worker as `r1 build` does).
	ProviderOverride provider.Provider
	// MaxTurns caps the agentloop; 0 -> a sane default.
	MaxTurns int
}

// Ensure NativeInvoker satisfies the dispatcher seam.
var _ agents.R1ModelInvoker = (*NativeInvoker)(nil)

const defaultBenchMaxTurns = 50

// Invoke drives one mission through the native agentloop in workDir and
// returns the outcome. enforce toggles the anti-truncation gate (the
// r1-antitrunc variant); the plan checklist is already written to the
// working tree by the dispatcher before this is called.
func (in *NativeInvoker) Invoke(ctx context.Context, mission *bench.MissionConfig, workDir string, enforce bool) (agents.R1InvocationResult, error) {
	if mission == nil {
		return agents.R1InvocationResult{ExitReason: agents.ExitReasonOther}, fmt.Errorf("NativeInvoker: nil mission")
	}

	runner := engine.NewNativeRunner(in.APIKey, in.Model)
	runner.BaseURL = in.BaseURL
	if in.ProviderOverride != nil {
		runner.ProviderOverride = in.ProviderOverride
	}

	maxTurns := in.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultBenchMaxTurns
	}

	// A harness-owned runtime dir OUTSIDE the working tree so the runner's
	// settings/logs never pollute the graded diff.
	runtimeDir, err := os.MkdirTemp("", "r1-bench-native-")
	if err != nil {
		return agents.R1InvocationResult{ExitReason: agents.ExitReasonOther}, fmt.Errorf("NativeInvoker: runtime dir: %w", err)
	}
	defer os.RemoveAll(runtimeDir)

	spec := engine.RunSpec{
		Prompt:      missionPrompt(mission),
		WorktreeDir: workDir,
		RuntimeDir:  runtimeDir,
		Phase: engine.PhaseSpec{
			Name:         "execute",
			MaxTurns:     maxTurns,
			BuiltinTools: []string{"read_file", "list_dir", "grep", "edit_file", "write_file", "bash"},
		},
	}

	// Capture the streamed events verbatim into a bounded trajectory log
	// so the reward-hacking auditor (SOTA gap #5) can inspect what the
	// agent actually did.
	var log boundedLog
	onEvent := func(ev stream.Event) {
		log.add(eventLine(ev))
	}

	result, runErr := runner.Run(ctx, spec, onEvent)

	out := agents.R1InvocationResult{
		LastAssistantText:   result.ResultText,
		WallClockMs:         result.DurationMs,
		RawLog:              log.String(),
		EstimatedBytes:      int64(len(result.ResultText)),
		CompletionAttempted: !result.IsError && strings.TrimSpace(result.ResultText) != "",
		ExitReason:          mapExitReason(result, runErr),
		CostUSD:             result.CostUSD,
		TokensUsed:          int64(result.Tokens.Input + result.Tokens.Output),
	}
	if runErr != nil {
		return out, runErr
	}
	return out, nil
}

// missionPrompt renders the mission's task into an execute prompt. The
// plan checklist is already on disk (plans/build-plan.md); the prompt
// points the agent at the task, its acceptance criteria, and that
// checklist. Intent is the primary task statement (matching the other
// dispatchers, e.g. aider uses mission.Intent); Description is a fallback.
func missionPrompt(m *bench.MissionConfig) string {
	var b strings.Builder
	task := strings.TrimSpace(m.Intent)
	if task == "" {
		task = strings.TrimSpace(m.Description)
	}
	b.WriteString(task)
	if len(m.Acceptance) > 0 {
		b.WriteString("\n\nAcceptance criteria:")
		for _, a := range m.Acceptance {
			b.WriteString("\n- " + a)
		}
	}
	if len(m.Plan) > 0 {
		b.WriteString("\n\nA checklist for this task is in plans/build-plan.md. Complete every item, verify your work, then state that you are done.")
	}
	return b.String()
}

// mapExitReason translates the native RunResult + error into the bench
// ExitReason vocabulary.
func mapExitReason(r engine.RunResult, err error) string {
	if err != nil {
		if ctxErr := errContains(err, "context deadline exceeded", "context canceled"); ctxErr {
			return agents.ExitReasonTimeout
		}
		return agents.ExitReasonToolError
	}
	switch r.Subtype {
	case "rate_limited":
		return agents.ExitReasonRateLimit
	case "error_max_turns":
		return agents.ExitReasonOther
	case "error_during_execution":
		return agents.ExitReasonToolError
	}
	if r.IsError {
		return agents.ExitReasonToolError
	}
	if strings.TrimSpace(r.ResultText) != "" {
		return agents.ExitReasonCompletionClaimed
	}
	return agents.ExitReasonOther
}

func errContains(err error, subs ...string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range subs {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// eventLine renders one stream event as a single trajectory line. Tool
// calls (which the reward-hacking auditor keys on) are rendered with their
// command/args so `git log`-style tells are visible.
func eventLine(ev stream.Event) string {
	if ev.Type == "" {
		return ""
	}
	line := ev.Type
	if ev.DeltaText != "" {
		line += ": " + oneLine(ev.DeltaText)
	}
	for _, tu := range ev.ToolUses {
		cmd := ""
		if c, ok := tu.Input["command"].(string); ok {
			cmd = c
		}
		line += fmt.Sprintf(" tool=%s %s", tu.Name, oneLine(cmd))
	}
	return line
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// boundedLog accumulates trajectory lines up to a byte cap so a runaway
// agent cannot balloon memory; the tail is what matters for auditing.
type boundedLog struct {
	buf strings.Builder
	max int
}

func (l *boundedLog) add(line string) {
	if line == "" {
		return
	}
	if l.max == 0 {
		l.max = 256 * 1024
	}
	if l.buf.Len() >= l.max {
		return
	}
	l.buf.WriteString(line)
	l.buf.WriteByte('\n')
}

func (l *boundedLog) String() string { return l.buf.String() }

// resolveNativeInvoker builds the production invoker from the same env the
// rest of cmd/r1-bench uses, so `--agent r1` runs r1 for real.
func resolveNativeInvoker(model string) *NativeInvoker {
	if model == "" {
		model = os.Getenv("R1_NATIVE_MODEL")
	}
	inv := &NativeInvoker{
		Model:   model,
		APIKey:  firstNonEmptyEnv("ANTHROPIC_API_KEY", "R1_API_KEY"),
		BaseURL: os.Getenv("R1_NATIVE_BASE_URL"),
	}
	// claude-code:// / codex:// worker shorthands, matching `r1 build`.
	if strings.HasPrefix(inv.BaseURL, "claude-code") {
		repo, _ := filepath.Abs(".")
		inv.ProviderOverride = provider.NewClaudeCodeWorker("claude", repo, model)
	} else if strings.HasPrefix(inv.BaseURL, "codex") {
		repo, _ := filepath.Abs(".")
		inv.ProviderOverride = provider.NewCodexProvider("codex", repo, "")
	}
	return inv
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
