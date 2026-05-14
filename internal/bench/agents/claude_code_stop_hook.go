// Claude Code + R1's published Stop-hook template.
//
// Wraps the same headless claude binary as ClaudeCodeDispatcher, but
// writes a `.claude/settings.json` into workDir before launch that
// installs `r1 antitrunc --hook-mode --plan plans/build-plan.md` as
// the Stop hook. This is the only adapter that exercises Claude
// Code's `decision: block` completion-gate primitive end-to-end.
//
// Spec: specs/truthful-completion-benchmark.md §T4.5 (items 26-28).
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

// ClaudeCodeStopHookDispatcher drives `claude --headless` with R1's
// truthful-completion Stop hook wired into the workspace.
type ClaudeCodeStopHookDispatcher struct {
	BinaryPath    string // default "claude"
	R1BinaryPath  string // default "r1" (the hook calls this)
}

func (d *ClaudeCodeStopHookDispatcher) Agent() Agent {
	return Agent{
		ID:          "claude-code-stop-hook",
		DisplayName: "Claude Code (with R1 Stop-hook template)",
		Version:     "headless+r1-antitrunc-hook",
	}
}

// stopHookSettings is the minimal `.claude/settings.json` shape that
// installs a Stop hook. Only the fields we set are present; Claude
// Code tolerates additional keys.
type stopHookSettings struct {
	Hooks map[string][]stopHookEntry `json:"hooks"`
}

type stopHookEntry struct {
	Matcher string             `json:"matcher,omitempty"`
	Hooks   []stopHookCommand  `json:"hooks"`
}

type stopHookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// installStopHook writes <workDir>/.claude/settings.json so the next
// claude invocation runs `r1 antitrunc --hook-mode` at Stop time.
// Exposed for the test.
func installStopHook(workDir, r1Bin string) error {
	dir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("installStopHook: mkdir %s: %w", dir, err)
	}
	cmd := fmt.Sprintf("%s antitrunc --hook-mode --plan plans/build-plan.md", r1Bin)
	settings := stopHookSettings{
		Hooks: map[string][]stopHookEntry{
			"Stop": {{
				Hooks: []stopHookCommand{{Type: "command", Command: cmd}},
			}},
		},
	}
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("installStopHook: marshal: %w", err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("installStopHook: write %s: %w", path, err)
	}
	return nil
}

func (d *ClaudeCodeStopHookDispatcher) Run(ctx context.Context, mission *bench.MissionConfig, workDir string, timeout time.Duration) (Trace, error) {
	if mission == nil {
		return Trace{ExitReason: ExitReasonOther}, errors.New("ClaudeCodeStopHookDispatcher.Run: mission is nil")
	}
	if err := WritePlan(workDir, mission.Plan); err != nil {
		return Trace{ExitReason: ExitReasonOther}, fmt.Errorf("write plan: %w", err)
	}
	r1Bin := d.R1BinaryPath
	if r1Bin == "" {
		r1Bin = "r1"
	}
	if err := installStopHook(workDir, r1Bin); err != nil {
		return Trace{ExitReason: ExitReasonOther}, err
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
	cmd.Stderr = nil

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Trace{
			ExitReason: ExitReasonNotSupported,
			RawLog:     fmt.Sprintf("claude binary start failed: %v", err),
		}, nil
	}
	trace := parseClaudeCodeStream(stdout)
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
	RegisterDispatcher("claude-code-stop-hook", &ClaudeCodeStopHookDispatcher{})
}
