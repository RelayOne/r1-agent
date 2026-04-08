// Package baseline captures and compares the build/test/lint state of a repository.
//
// Before any mission work begins, Capture snapshots the current state of the
// project's CI gate (build, test, lint). After execution, Verify runs the same
// commands and compares. Any failure — pre-existing or introduced — is a blocking
// gap. The harness does not distinguish between "was already broken" and "we broke
// it." Both are problems the mission must solve.
//
// Design principle: if the test suite is red, the work is not done. Period.
// A mission cannot converge while any verification command fails.
//
// Usage:
//
//	snap, err := baseline.Capture(ctx, "/repo", baseline.AutoDetect("/repo"))
//	// ... agent does work ...
//	result := baseline.Verify(ctx, "/repo", snap.Commands)
//	for _, f := range result.Failures() {
//	    // create gaps for each failure
//	}
package baseline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/config"
)

// CommandResult captures the outcome of running a single verification command.
type CommandResult struct {
	Name     string        `json:"name"`      // "build", "test", or "lint"
	Command  string        `json:"command"`   // the shell command that was run
	ExitCode int           `json:"exit_code"` // 0 = pass
	Output   string        `json:"output"`    // combined stdout+stderr
	Duration time.Duration `json:"duration"`  // wall-clock time
	Pass     bool          `json:"pass"`      // convenience: exit_code == 0
}

// Snapshot is the recorded state of a repository's CI gate at a point in time.
type Snapshot struct {
	CapturedAt time.Time       `json:"captured_at"`
	RepoRoot   string          `json:"repo_root"`
	Commands   Commands        `json:"commands"`
	Results    []CommandResult `json:"results"`
	AllPass    bool            `json:"all_pass"`    // true if every command passed
	ContentHash string         `json:"content_hash"` // SHA256 of concatenated outputs for diffing
}

// Commands holds the build/test/lint commands to run.
type Commands struct {
	Build string `json:"build,omitempty"`
	Test  string `json:"test,omitempty"`
	Lint  string `json:"lint,omitempty"`
}

// Failures returns only the commands that failed.
func (s *Snapshot) Failures() []CommandResult {
	var out []CommandResult
	for _, r := range s.Results {
		if !r.Pass {
			out = append(out, r)
		}
	}
	return out
}

// FailureSummary returns a human-readable summary of failures.
func (s *Snapshot) FailureSummary() string {
	failures := s.Failures()
	if len(failures) == 0 {
		return "all checks pass"
	}
	var parts []string
	for _, f := range failures {
		lines := strings.Split(strings.TrimSpace(f.Output), "\n")
		tail := lines
		if len(tail) > 10 {
			tail = tail[len(tail)-10:]
		}
		parts = append(parts, fmt.Sprintf("[%s] %s (exit %d):\n%s",
			f.Name, f.Command, f.ExitCode, strings.Join(tail, "\n")))
	}
	return strings.Join(parts, "\n\n")
}

// PreExistingFailures compares a before-snapshot against an after-snapshot
// and returns failures that existed before the mission started.
// A failure is pre-existing if the same command failed in both snapshots.
func PreExistingFailures(before, after *Snapshot) []CommandResult {
	beforeFailed := map[string]bool{}
	for _, r := range before.Results {
		if !r.Pass {
			beforeFailed[r.Name] = true
		}
	}
	var preExisting []CommandResult
	for _, r := range after.Results {
		if !r.Pass && beforeFailed[r.Name] {
			preExisting = append(preExisting, r)
		}
	}
	return preExisting
}

// NewFailures returns failures in after that were NOT present in before.
func NewFailures(before, after *Snapshot) []CommandResult {
	beforeFailed := map[string]bool{}
	for _, r := range before.Results {
		if !r.Pass {
			beforeFailed[r.Name] = true
		}
	}
	var introduced []CommandResult
	for _, r := range after.Results {
		if !r.Pass && !beforeFailed[r.Name] {
			introduced = append(introduced, r)
		}
	}
	return introduced
}

// AutoDetect detects build/test/lint commands for the given project root.
func AutoDetect(projectRoot string) Commands {
	detected := config.DetectCommands(projectRoot)
	return Commands{
		Build: detected.Build,
		Test:  detected.Test,
		Lint:  detected.Lint,
	}
}

// Capture runs all configured commands and records their results.
// This should be called before any mission work begins to establish
// the pre-existing state of the codebase.
func Capture(ctx context.Context, repoRoot string, cmds Commands) (*Snapshot, error) {
	if repoRoot == "" {
		return nil, fmt.Errorf("baseline: repo root must not be empty")
	}

	snap := &Snapshot{
		CapturedAt: time.Now(),
		RepoRoot:   repoRoot,
		Commands:   cmds,
		AllPass:    true,
	}

	for _, item := range []struct {
		name string
		cmd  string
	}{
		{"build", cmds.Build},
		{"test", cmds.Test},
		{"lint", cmds.Lint},
	} {
		if strings.TrimSpace(item.cmd) == "" {
			continue // no command configured, skip
		}

		result := runCommand(ctx, repoRoot, item.name, item.cmd)
		snap.Results = append(snap.Results, result)
		if !result.Pass {
			snap.AllPass = false
		}
	}

	snap.ContentHash = hashResults(snap.Results)
	return snap, nil
}

// Verify runs the same commands as Capture but is semantically "after work."
// It returns a new Snapshot that can be compared against the baseline.
func Verify(ctx context.Context, repoRoot string, cmds Commands) (*Snapshot, error) {
	return Capture(ctx, repoRoot, cmds)
}

// runCommand executes a shell command and captures its result.
func runCommand(ctx context.Context, dir, name, command string) CommandResult {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return CommandResult{
		Name:     name,
		Command:  command,
		ExitCode: exitCode,
		Output:   string(out),
		Duration: time.Since(start),
		Pass:     exitCode == 0,
	}
}

// hashResults produces a deterministic hash of command outputs for change detection.
func hashResults(results []CommandResult) string {
	h := sha256.New()
	for _, r := range results {
		fmt.Fprintf(h, "%s:%d:%s\n", r.Name, r.ExitCode, r.Output)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Save persists a snapshot to disk as JSON.
func (s *Snapshot) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("baseline: marshal snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("baseline: create dir: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

// Load reads a snapshot from disk.
func Load(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("baseline: read snapshot: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("baseline: unmarshal snapshot: %w", err)
	}
	return &snap, nil
}

// Diff describes what changed between two snapshots.
type Diff struct {
	Before       *Snapshot       `json:"before"`
	After        *Snapshot       `json:"after"`
	PreExisting  []CommandResult `json:"pre_existing"`  // failed before AND after
	Introduced   []CommandResult `json:"introduced"`    // new failures
	Fixed        []CommandResult `json:"fixed"`         // failed before, pass now
	StillPassing []CommandResult `json:"still_passing"` // pass before and after
}

// Compare produces a structured diff between two snapshots.
func Compare(before, after *Snapshot) *Diff {
	d := &Diff{Before: before, After: after}

	beforeMap := map[string]CommandResult{}
	for _, r := range before.Results {
		beforeMap[r.Name] = r
	}

	for _, ar := range after.Results {
		br, existed := beforeMap[ar.Name]
		if !existed {
			if !ar.Pass {
				d.Introduced = append(d.Introduced, ar)
			} else {
				d.StillPassing = append(d.StillPassing, ar)
			}
			continue
		}

		switch {
		case !br.Pass && !ar.Pass:
			d.PreExisting = append(d.PreExisting, ar)
		case !br.Pass && ar.Pass:
			d.Fixed = append(d.Fixed, ar)
		case br.Pass && !ar.Pass:
			d.Introduced = append(d.Introduced, ar)
		default:
			d.StillPassing = append(d.StillPassing, ar)
		}
	}

	return d
}

// HasAnyFailure returns true if there are any failures (pre-existing or introduced).
func (d *Diff) HasAnyFailure() bool {
	return len(d.PreExisting) > 0 || len(d.Introduced) > 0
}

// AllFixed returns true if every pre-existing failure was resolved.
func (d *Diff) AllFixed() bool {
	return len(d.PreExisting) == 0
}

// Summary returns a human-readable summary of the diff.
func (d *Diff) Summary() string {
	var parts []string
	if n := len(d.Fixed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d fixed", n))
	}
	if n := len(d.Introduced); n > 0 {
		parts = append(parts, fmt.Sprintf("%d introduced", n))
	}
	if n := len(d.PreExisting); n > 0 {
		parts = append(parts, fmt.Sprintf("%d pre-existing (must fix)", n))
	}
	if n := len(d.StillPassing); n > 0 {
		parts = append(parts, fmt.Sprintf("%d passing", n))
	}
	if len(parts) == 0 {
		return "no verification commands configured"
	}
	return strings.Join(parts, ", ")
}
