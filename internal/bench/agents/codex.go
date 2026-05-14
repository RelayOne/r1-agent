// Codex CLI dispatcher — drives `codex exec` with one-shot prompt.
//
// Codex emits NDJSON events on stdout when run with --json. The
// terminal "task_complete" event marks completion; "rate_limited"
// event marks a rate-limit interruption.
//
// Spec: specs/truthful-completion-benchmark.md §T4.8 (items 33-34).
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

type CodexDispatcher struct {
	BinaryPath string // default "codex"
}

func (d *CodexDispatcher) Agent() Agent {
	return Agent{
		ID:          "codex-cli",
		DisplayName: "Codex CLI (cloud sandbox)",
		Version:     "exec-json",
	}
}

type codexEvent struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

func parseCodexStream(r io.Reader) Trace {
	var (
		completion bool
		last       strings.Builder
		raw        strings.Builder
		exit       string
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		raw.WriteString(line)
		raw.WriteByte('\n')
		var ev codexEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "assistant_message":
			if ev.Content != "" {
				last.Reset()
				last.WriteString(ev.Content)
			}
		case "task_complete":
			completion = true
			exit = ExitReasonCompletionClaimed
		case "rate_limited":
			exit = ExitReasonRateLimit
		case "task_failed":
			if exit == "" {
				exit = ExitReasonToolError
			}
		}
	}
	return Trace{
		CompletionAttempted: completion,
		LastAssistantText:   last.String(),
		ExitReason:          exit,
		RawLog:              BoundedLog([]byte(raw.String()), 0),
	}
}

func (d *CodexDispatcher) Run(ctx context.Context, mission *bench.MissionConfig, workDir string, timeout time.Duration) (Trace, error) {
	if mission == nil {
		return Trace{ExitReason: ExitReasonOther}, errors.New("CodexDispatcher.Run: mission is nil")
	}
	binary := d.BinaryPath
	if binary == "" {
		binary = "codex"
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary,
		"exec",
		"--json",
		"--cd", workDir,
		mission.Intent,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Trace{ExitReason: ExitReasonOther}, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = nil
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Trace{
			ExitReason: ExitReasonNotSupported,
			RawLog:     fmt.Sprintf("codex binary start failed: %v", err),
		}, nil
	}
	trace := parseCodexStream(stdout)
	waitErr := cmd.Wait()
	trace.WallClockMs = time.Since(start).Milliseconds()
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

func init() {
	RegisterDispatcher("codex-cli", &CodexDispatcher{})
}
