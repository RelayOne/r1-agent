// Aider dispatcher — drives `aider --message --yes-always`.
//
// Aider has NO completion-attempt event surface. It edits files, prints
// a summary to stdout, and exits. We treat "process exited cleanly with
// at least one edit applied" as the completion signal.
//
// Spec: specs/truthful-completion-benchmark.md §T4.7 (items 31-32).
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

type AiderDispatcher struct {
	BinaryPath string // default "aider"
}

func (d *AiderDispatcher) Agent() Agent {
	return Agent{
		ID:          "aider",
		DisplayName: "Aider (--yes-always, no completion gate)",
		Version:     "cli",
	}
}

// parseAiderOutput inspects raw aider stdout/stderr and returns a
// CompletionAttempted bit + the trailing assistant text.
//
// Heuristics:
//   - `Applied edit to ` (case-sensitive) is aider's standard success
//     marker; presence means it touched files.
//   - `Committing 1 file with message: ...` (or N files) likewise.
//   - The "trailing assistant text" is everything after the LAST
//     `Aider response:` line, falling back to the whole output trimmed.
func parseAiderOutput(out string) (completion bool, last string) {
	completion = strings.Contains(out, "Applied edit to ") ||
		strings.Contains(out, "Committing 1 file with message:") ||
		strings.Contains(out, "Committed change.")
	last = ExtractLastAssistantTurn(out, "Aider response:")
	return completion, last
}

func (d *AiderDispatcher) Run(ctx context.Context, mission *bench.MissionConfig, workDir string, timeout time.Duration) (Trace, error) {
	if mission == nil {
		return Trace{ExitReason: ExitReasonOther}, errors.New("AiderDispatcher.Run: mission is nil")
	}
	binary := d.BinaryPath
	if binary == "" {
		binary = "aider"
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary,
		"--yes-always",
		"--no-auto-commits",
		"--message", mission.Intent,
	)
	cmd.Dir = workDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	runErr := cmd.Run()
	wallMs := time.Since(start).Milliseconds()
	rawLog := buf.String()

	if runErr != nil {
		// Distinguish "process never started" (binary missing /
		// unexecutable) from "ran and errored". When ProcessState is
		// nil, the OS rejected the exec call before the binary ever
		// produced output.
		if cmd.ProcessState == nil {
			return Trace{
				ExitReason: ExitReasonNotSupported,
				RawLog:     fmt.Sprintf("aider binary start failed: %v", runErr),
			}, nil
		}
		exitReason := ExitReasonToolError
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			exitReason = ExitReasonTimeout
		}
		completionAttempted, last := parseAiderOutput(rawLog)
		return Trace{
			CompletionAttempted: completionAttempted,
			LastAssistantText:   last,
			UnifiedDiff:         safeGitDiff(workDir),
			WallClockMs:         wallMs,
			ExitReason:          exitReason,
			RawLog:              BoundedLog([]byte(rawLog), 0),
		}, nil
	}

	completionAttempted, last := parseAiderOutput(rawLog)
	exitReason := ExitReasonCompletionClaimed
	if !completionAttempted {
		exitReason = ExitReasonOther
	}
	return Trace{
		CompletionAttempted: completionAttempted,
		LastAssistantText:   last,
		UnifiedDiff:         safeGitDiff(workDir),
		WallClockMs:         wallMs,
		ExitReason:          exitReason,
		RawLog:              BoundedLog([]byte(rawLog), 0),
	}, nil
}

func init() {
	RegisterDispatcher("aider", &AiderDispatcher{})
}
