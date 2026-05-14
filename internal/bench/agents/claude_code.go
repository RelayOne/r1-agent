// Claude Code default dispatcher — drives `claude` CLI in headless mode.
//
// Spec: specs/truthful-completion-benchmark.md §T4.4 (items 22-25).
package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

// ClaudeCodeDispatcher drives the Anthropic Claude Code CLI in
// headless mode against a single mission.
type ClaudeCodeDispatcher struct {
	BinaryPath string  // default "claude" (looked up via PATH)
	APIKeyEnv  string  // env var name; default "ANTHROPIC_API_KEY"
}

func (d *ClaudeCodeDispatcher) Agent() Agent {
	return Agent{
		ID:          "claude-code-default",
		DisplayName: "Claude Code (default, no Stop hook)",
		Version:     "headless",
	}
}

// claudeCodeEvent represents one of Claude Code's JSON-tagged stdout
// segments. Unknown fields are accepted (the protocol evolves).
type claudeCodeEvent struct {
	Event           string `json:"event"`
	Content         string `json:"content,omitempty"`
	StopHookActive  *bool  `json:"stop_hook_active,omitempty"`
	Decision        string `json:"decision,omitempty"`
	Message         string `json:"message,omitempty"`
}

func (d *ClaudeCodeDispatcher) Run(ctx context.Context, mission *bench.MissionConfig, workDir string, timeout time.Duration) (Trace, error) {
	if mission == nil {
		return Trace{ExitReason: ExitReasonOther}, errors.New("ClaudeCodeDispatcher.Run: mission is nil")
	}
	binary := d.BinaryPath
	if binary == "" {
		binary = "claude"
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, binary,
		"--headless",
		"--no-interactive",
		"--working-dir", workDir,
		"--prompt", mission.Intent,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Trace{ExitReason: ExitReasonOther}, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = nil // discard stderr; stdout is the JSON event stream

	start := time.Now()
	if err := cmd.Start(); err != nil {
		// Claude Code not installed or unreachable.
		return Trace{
			ExitReason: ExitReasonNotSupported,
			RawLog:     fmt.Sprintf("claude binary start failed: %v", err),
		}, nil
	}

	trace := parseClaudeCodeStream(stdout)

	waitErr := cmd.Wait()
	wallMs := time.Since(start).Milliseconds()
	trace.WallClockMs = wallMs
	trace.UnifiedDiff = safeGitDiff(workDir)
	if waitErr != nil && trace.ExitReason == "" {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			trace.ExitReason = ExitReasonTimeout
		} else {
			trace.ExitReason = ExitReasonToolError
		}
	}
	if trace.ExitReason == "" {
		trace.ExitReason = ExitReasonOther
	}
	return trace, nil
}

// parseClaudeCodeStream is the protocol-parsing pure function — it
// reads NDJSON events from r and returns a Trace. Exposed for the
// fixture-replay test.
func parseClaudeCodeStream(r io.Reader) Trace {
	var (
		completionAttempted bool
		lastAssistant      strings.Builder
		rawLog             strings.Builder
		exitReason         string
	)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		rawLog.WriteString(line)
		rawLog.WriteByte('\n')
		var ev claudeCodeEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // tolerate non-JSON lines (banners, debug)
		}
		switch ev.Event {
		case "assistant_message":
			if ev.Content != "" {
				lastAssistant.Reset()
				lastAssistant.WriteString(ev.Content)
			}
		case "stop":
			// Stop with stop_hook_active=false → real completion.
			if ev.StopHookActive != nil && !*ev.StopHookActive {
				completionAttempted = true
				exitReason = ExitReasonCompletionClaimed
			}
			// Stop with hook active + decision=approve → also completion.
			if ev.StopHookActive != nil && *ev.StopHookActive && ev.Decision == "approve" {
				completionAttempted = true
				exitReason = ExitReasonCompletionClaimed
			}
		case "error":
			if strings.Contains(strings.ToLower(ev.Message), "rate_limit") {
				exitReason = ExitReasonRateLimit
			}
		}
	}
	return Trace{
		CompletionAttempted: completionAttempted,
		LastAssistantText:   lastAssistant.String(),
		ExitReason:          exitReason,
		RawLog:              BoundedLog([]byte(rawLog.String()), 0),
	}
}

func init() {
	RegisterDispatcher("claude-code-default", &ClaudeCodeDispatcher{})
}
