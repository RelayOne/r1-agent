// Cline dispatcher — drives the Cline VS Code extension via its
// experimental `cline --headless` task-runner mode.
//
// Cline's agent loop in /tmp/cline-dev/src/core/task/index.ts:1456-1466
// terminates when the model calls `attempt_completion`. That call
// surfaces in headless mode as a JSON line:
//
//	{"event":"attempt_completion","result":"<final assistant text>"}
//
// Spec: specs/truthful-completion-benchmark.md §T4.6 (items 29-30).
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

type ClineDispatcher struct {
	BinaryPath string // default "cline"
}

func (d *ClineDispatcher) Agent() Agent {
	return Agent{
		ID:          "cline",
		DisplayName: "Cline (VS Code extension, headless)",
		Version:     "headless",
	}
}

type clineEvent struct {
	Event   string `json:"event"`
	Result  string `json:"result,omitempty"`
	Message string `json:"message,omitempty"`
	Text    string `json:"text,omitempty"`
}

func parseClineStream(r io.Reader) Trace {
	var (
		completionAttempted bool
		last                string
		raw                 strings.Builder
		exitReason          string
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		raw.WriteString(line)
		raw.WriteByte('\n')
		var ev clineEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Event {
		case "attempt_completion":
			completionAttempted = true
			if ev.Result != "" {
				last = ev.Result
			}
			exitReason = ExitReasonCompletionClaimed
		case "say":
			if ev.Text != "" {
				last = ev.Text
			}
		case "error":
			if strings.Contains(strings.ToLower(ev.Message), "rate") {
				exitReason = ExitReasonRateLimit
			} else if exitReason == "" {
				exitReason = ExitReasonToolError
			}
		}
	}
	return Trace{
		CompletionAttempted: completionAttempted,
		LastAssistantText:   last,
		ExitReason:          exitReason,
		RawLog:              BoundedLog([]byte(raw.String()), 0),
	}
}

func (d *ClineDispatcher) Run(ctx context.Context, mission *bench.MissionConfig, workDir string, timeout time.Duration) (Trace, error) {
	if mission == nil {
		return Trace{ExitReason: ExitReasonOther}, errors.New("ClineDispatcher.Run: mission is nil")
	}
	binary := d.BinaryPath
	if binary == "" {
		binary = "cline"
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary,
		"--headless",
		"--cwd", workDir,
		"--task", mission.Intent,
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
			RawLog:     fmt.Sprintf("cline binary start failed: %v", err),
		}, nil
	}
	trace := parseClineStream(stdout)
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
	RegisterDispatcher("cline", &ClineDispatcher{})
}
