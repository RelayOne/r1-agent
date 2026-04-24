package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/stream"
)

// Runner prints structured text updates. Zero external dependencies.
// Used for `stoke build --headless` and CI/CD environments.
type Runner struct {
	mu          sync.Mutex
	tasksDone   int
	tasksFailed int
	totalCost   float64
	startedAt   time.Time
	activeTask  string
}

// NewRunner creates a headless text-mode runner.
func NewRunner() *Runner {
	return &Runner{startedAt: time.Now()}
}

// TaskStart prints a task dispatch line.
func (r *Runner) TaskStart(taskID, description string, poolID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeTask = taskID
	fmt.Printf("▸ [%s] %s (pool: %s)\n", taskID, trunc(description, 60), poolID)
}

// Event prints live progress for tool use and results.
func (r *Runner) Event(taskID string, ev stream.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch ev.Type {
	case "assistant":
		for _, tu := range ev.ToolUses {
			fmt.Printf("  ⚙ %s %s\n", tu.Name, toolInput(tu))
		}
	case "user":
		for _, tr := range ev.ToolResults {
			icon := "✓"
			if tr.IsError {
				icon = "✗"
			}
			fmt.Printf("  %s %s\n", icon, trunc(tr.Content, 80))
		}
		// cost is tracked in TaskComplete, not here (avoids double-counting)
	}
}

// TaskComplete prints a task completion line and accumulates cost.
func (r *Runner) TaskComplete(taskID string, success bool, durationSec float64, costUSD float64, attempt int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalCost += costUSD // accumulate here, not in Event handler
	if success {
		r.tasksDone++
		fmt.Printf("✓ [%s] done (%.1fs, $%.4f, attempt %d)\n", taskID, durationSec, costUSD, attempt)
	} else {
		r.tasksFailed++
		fmt.Printf("✗ [%s] failed (%.1fs, $%.4f, attempt %d)\n", taskID, durationSec, costUSD, attempt)
	}
}

// TaskRetry prints a retry line.
func (r *Runner) TaskRetry(taskID string, attempt int, reason string) {
	fmt.Printf("↻ [%s] retry %d: %s\n", taskID, attempt, reason)
}

// TaskEscalate prints an escalation line.
func (r *Runner) TaskEscalate(taskID string, reason string) {
	fmt.Printf("⚠ [%s] ESCALATED: %s\n", taskID, reason)
}

// Status prints a periodic status line.
func (r *Runner) Status(totalTasks int, activePools int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	elapsed := time.Since(r.startedAt).Round(time.Second)
	fmt.Printf("  [%s] %d/%d tasks | %d active | $%.2f\n",
		elapsed, r.tasksDone, totalTasks, activePools, r.totalCost)
}

// Summary prints the final session summary.
func (r *Runner) Summary(totalTasks int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	elapsed := time.Since(r.startedAt).Round(time.Second)
	fmt.Printf("\n✓ Done: %d done, %d failed out of %d tasks, $%.2f, %s\n",
		r.tasksDone, r.tasksFailed, totalTasks, r.totalCost, elapsed)
}

// PoolStatus prints subscription pool utilization bars.
func PoolStatus(pools []PoolInfo) {
	for _, p := range pools {
		reset := ""
		if !p.ResetAt.IsZero() {
			reset = fmt.Sprintf("  resets in %s", time.Until(p.ResetAt).Round(time.Minute))
		}
		fmt.Printf("  [%s] %s %s %.0f%%%s\n", p.ID, p.Label, bar(p.Utilization, 15), p.Utilization, reset)
	}
}

// PoolInfo is a snapshot for display.
type PoolInfo struct {
	ID          string
	Label       string
	Utilization float64
	ResetAt     time.Time
}

func bar(pct float64, w int) string {
	n := int(pct / 100 * float64(w))
	if n > w {
		n = w
	}
	if n < 0 {
		n = 0
	}
	return strings.Repeat("█", n) + strings.Repeat("░", w-n)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func toolInput(tu stream.ToolUse) string {
	switch tu.Name {
	case "Read", "Edit", "Write":
		if fp, ok := tu.Input["file_path"].(string); ok {
			return fp
		}
	case "Bash":
		if cmd, ok := tu.Input["command"].(string); ok {
			return trunc(cmd, 50)
		}
	case "Glob":
		if p, ok := tu.Input["pattern"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := tu.Input["pattern"].(string); ok {
			return p
		}
	}
	return ""
}
