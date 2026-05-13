// Cursor dispatcher — drives `cursor-agent` CLI in headless mode.
//
// Cursor's CLI surface is narrow: no Stop-equivalent hook, no
// completion event. We anchor on the `[cursor-agent] task finished`
// stdout sentinel that the CLI emits on clean exit (verified in
// cursor 1.7+).
//
// Spec: specs/truthful-completion-benchmark.md §T4.9 (items 35-36).
package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

type CursorDispatcher struct {
	BinaryPath string // default "cursor-agent"
}

func (d *CursorDispatcher) Agent() Agent {
	return Agent{
		ID:          "cursor",
		DisplayName: "Cursor (cursor-agent CLI, no completion gate)",
		Version:     "cli",
	}
}

const cursorFinishMarker = "[cursor-agent] task finished"

// parseCursorOutput detects the cursor-agent completion sentinel and
// returns the trailing assistant turn.
func parseCursorOutput(out string) (completion bool, last string) {
	completion = strings.Contains(out, cursorFinishMarker)
	last = ExtractLastAssistantTurn(out, "[cursor-agent] assistant:")
	return completion, last
}

func (d *CursorDispatcher) Run(ctx context.Context, mission *bench.MissionConfig, workDir string, timeout time.Duration) (Trace, error) {
	if mission == nil {
		return Trace{ExitReason: ExitReasonOther}, errors.New("CursorDispatcher.Run: mission is nil")
	}
	binary := d.BinaryPath
	if binary == "" {
		binary = "cursor-agent"
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary,
		"--headless",
		"--workspace", workDir,
		"--prompt", mission.Intent,
	)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	runErr := cmd.Run()
	wallMs := time.Since(start).Milliseconds()
	rawLog := buf.String()

	if runErr != nil {
		if cmd.ProcessState == nil {
			return Trace{
				ExitReason: ExitReasonNotSupported,
				RawLog:     fmt.Sprintf("cursor-agent binary start failed: %v", runErr),
			}, nil
		}
		exitReason := ExitReasonToolError
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			exitReason = ExitReasonTimeout
		}
		completionAttempted, last := parseCursorOutput(rawLog)
		return Trace{
			CompletionAttempted: completionAttempted,
			LastAssistantText:   last,
			UnifiedDiff:         safeGitDiff(workDir),
			WallClockMs:         wallMs,
			ExitReason:          exitReason,
			RawLog:              BoundedLog([]byte(rawLog), 0),
		}, nil
	}

	completionAttempted, last := parseCursorOutput(rawLog)
	exit := ExitReasonCompletionClaimed
	if !completionAttempted {
		exit = ExitReasonOther
	}
	return Trace{
		CompletionAttempted: completionAttempted,
		LastAssistantText:   last,
		UnifiedDiff:         safeGitDiff(workDir),
		WallClockMs:         wallMs,
		ExitReason:          exit,
		RawLog:              BoundedLog([]byte(rawLog), 0),
	}, nil
}

func init() {
	RegisterDispatcher("cursor", &CursorDispatcher{})
}
