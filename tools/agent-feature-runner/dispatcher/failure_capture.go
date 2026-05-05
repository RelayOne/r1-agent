// failure_capture.go — write per-scenario failure context to
// .agent-failures/<scenario>/ per spec 8 §10:
//
//   > On failure, capture: snapshot (TUI or web a11y tree), bus tail
//   > since scenario start, last 20 lanes events. Write to
//   > .agent-failures/<scenario>/.
//
// The runner calls CaptureFailure when an assertion misses or a
// transport error escalates. The directory shape is:
//
//   .agent-failures/<safe-scenario-name>/
//     summary.json    — structured diagnostics (cause, step text, line)
//     snapshot.json   — TUI A11yNode tree OR web a11y dump (whichever was active)
//     bus.ndjson      — bus events since scenario start
//     lanes.ndjson    — last 20 lane events
//     view.txt        — best-effort human-readable view (from TUI Snapshot.View)
//
// CaptureFailure is idempotent on (root, scenarioName): a prior run's
// directory is removed before writing.
package dispatcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FailureContext bundles everything CaptureFailure writes. Fields that
// are nil/empty are simply omitted from disk.
type FailureContext struct {
	ScenarioName string `json:"scenario_name"`
	FeaturePath  string `json:"feature_path"`
	StepLine     int    `json:"step_line"`
	StepText     string `json:"step_text"`
	Cause        string `json:"cause"`

	// Snapshot is the TUI A11yNode tree OR web a11y dump (whichever
	// was active). The runner serializes to JSON before passing in.
	Snapshot json.RawMessage `json:"-"`
	// View is the best-effort human-readable rendering (TUI .View()
	// output OR Playwright textContent).
	View string `json:"-"`
	// BusEvents and LaneEvents are NDJSON-encoded already.
	BusEvents  []json.RawMessage `json:"-"`
	LaneEvents []json.RawMessage `json:"-"`
}

// CaptureFailure writes ctx to root/<safeName>/. Returns the directory
// path on success.
func CaptureFailure(root string, ctx FailureContext) (string, error) {
	if root == "" {
		root = ".agent-failures"
	}
	safe := safeScenarioDir(ctx.ScenarioName)
	if safe == "" {
		safe = "unnamed"
	}
	dir := filepath.Join(root, safe)
	// Idempotent: blow away the prior directory so stale artifacts
	// from earlier runs cannot mask the current failure.
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clear prior failure dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	// summary.json — always written.
	summary, err := json.MarshalIndent(struct {
		FailureContext
	}{ctx}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal summary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), summary, 0o644); err != nil {
		return "", fmt.Errorf("write summary: %w", err)
	}

	// snapshot.json — only when a Snapshot was supplied.
	if len(ctx.Snapshot) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), ctx.Snapshot, 0o644); err != nil {
			return "", fmt.Errorf("write snapshot: %w", err)
		}
	}

	// view.txt — best-effort.
	if ctx.View != "" {
		if err := os.WriteFile(filepath.Join(dir, "view.txt"), []byte(ctx.View), 0o644); err != nil {
			return "", fmt.Errorf("write view: %w", err)
		}
	}

	// bus.ndjson and lanes.ndjson — NDJSON (one event per line).
	if err := writeNDJSON(filepath.Join(dir, "bus.ndjson"), ctx.BusEvents); err != nil {
		return "", err
	}
	if err := writeNDJSON(filepath.Join(dir, "lanes.ndjson"), ctx.LaneEvents); err != nil {
		return "", err
	}

	return dir, nil
}

// safeScenarioDir converts a scenario name to a filesystem-safe slug.
// "User sends a message and sees a streamed response" ->
// "user-sends-a-message-and-sees-a-streamed-response".
func safeScenarioDir(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// writeNDJSON writes one JSON object per line. Empty input writes
// nothing (no zero-byte file).
func writeNDJSON(path string, events []json.RawMessage) error {
	if len(events) == 0 {
		return nil
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}
	defer f.Close()
	for _, e := range events {
		if _, err := f.Write(e); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
		if _, err := f.Write([]byte("\n")); err != nil {
			return fmt.Errorf("write nl: %w", err)
		}
	}
	return nil
}
