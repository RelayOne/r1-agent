package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1-agent/internal/engine"
	"github.com/RelayOne/r1-agent/internal/stream"
)

// mockRunner is a configurable CommandRunner for testing the full workflow
// pipeline without real AI backends. It produces realistic RunResults and
// can optionally write files in the worktree to simulate code generation.
type mockRunner struct {
	// PlanOutput is returned as ResultText for plan phases.
	PlanOutput string
	// ExecuteOutput is returned as ResultText for execute phases.
	ExecuteOutput string
	// VerifyOutput is returned as ResultText for verify phases (review JSON).
	VerifyOutput string
	// FilesToWrite maps relative paths to content; written during execute phase.
	FilesToWrite map[string]string
	// FailExecute makes the execute phase return an error.
	FailExecute bool
	// ExecuteSubtype overrides the Subtype field on execute results.
	ExecuteSubtype string
	// Calls tracks how many times Run was called per phase name.
	Calls map[string]int
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		PlanOutput: `{
  "id": "plan-test",
  "description": "test plan",
  "tasks": [
    {"id": "TASK-1", "description": "implement feature", "files": ["main.go"], "verification": ["builds successfully"]}
  ],
  "cross_phase_verification": ["integration works"],
  "ship_blockers": []
}`,
		ExecuteOutput: "I implemented the requested changes.",
		VerifyOutput: `{
  "pass": true,
  "severity": "clean",
  "verification_results": [{"item": "builds successfully", "pass": true, "note": "verified"}],
  "findings": []
}`,
		FilesToWrite: map[string]string{},
		Calls:        map[string]int{},
	}
}

func (m *mockRunner) Prepare(spec engine.RunSpec) (engine.PreparedCommand, error) {
	return engine.PreparedCommand{
		Binary: "mock",
		Args:   []string{spec.Phase.Name},
		Dir:    spec.WorktreeDir,
	}, nil
}

func (m *mockRunner) Run(ctx context.Context, spec engine.RunSpec, onEvent engine.OnEventFunc) (engine.RunResult, error) {
	phase := spec.Phase.Name
	m.Calls[phase]++

	// Emit a realistic start event
	if onEvent != nil {
		onEvent(stream.Event{
			Type:      "system",
			SessionID: fmt.Sprintf("mock-%s-%d", phase, m.Calls[phase]),
		})
	}

	switch {
	case phase == "plan":
		return engine.RunResult{
			Prepared:   engine.PreparedCommand{Binary: "mock", Args: []string{"plan"}},
			ResultText: m.PlanOutput,
			DurationMs: 100,
			NumTurns:   3,
			Subtype:    "success",
		}, nil

	case phase == "execute":
		if m.FailExecute {
			return engine.RunResult{
				IsError:    true,
				Subtype:    firstNonEmpty(m.ExecuteSubtype, "error_during_execution"),
				ResultText: "execution failed",
				DurationMs: 50,
			}, nil
		}

		// Write files in the worktree to simulate code generation.
		for relPath, content := range m.FilesToWrite {
			absPath := filepath.Join(spec.WorktreeDir, relPath)
			if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
				return engine.RunResult{}, err
			}
			if err := os.WriteFile(absPath, []byte(content), 0o600); err != nil {
				return engine.RunResult{}, err
			}
		}
		// Stage written files so git diff sees them.
		if len(m.FilesToWrite) > 0 {
			cmd := exec.Command("git", "-C", spec.WorktreeDir, "add", "-A")
			cmd.Run()
		}

		// Emit tool use events (for review quality tracking)
		if onEvent != nil {
			for range m.FilesToWrite {
				onEvent(stream.Event{
					Type: "assistant",
					ToolUses: []stream.ToolUse{
						{Name: "Write", Input: map[string]interface{}{"file": "test.go"}},
					},
				})
			}
		}

		return engine.RunResult{
			Prepared:   engine.PreparedCommand{Binary: "mock", Args: []string{"execute"}},
			ResultText: m.ExecuteOutput,
			DurationMs: 500,
			NumTurns:   10,
			Subtype:    firstNonEmpty(m.ExecuteSubtype, "success"),
		}, nil

	case phase == "verify":
		// Emit Read tool uses to satisfy review quality gate
		if onEvent != nil {
			onEvent(stream.Event{
				Type: "assistant",
				ToolUses: []stream.ToolUse{
					{Name: "Read", Input: map[string]interface{}{"file": "main.go"}},
				},
			})
		}

		return engine.RunResult{
			Prepared:   engine.PreparedCommand{Binary: "mock", Args: []string{"verify"}},
			ResultText: m.VerifyOutput,
			DurationMs: 200,
			NumTurns:   5,
			Subtype:    "success",
		}, nil

	default:
		return engine.RunResult{
			ResultText: "unknown phase",
			DurationMs: 10,
		}, nil
	}
}

// mockRunnerWithDelay wraps mockRunner to add configurable delay for timeout tests.
type mockRunnerWithDelay struct {
	*mockRunner
	delay time.Duration
}

func (m *mockRunnerWithDelay) Run(ctx context.Context, spec engine.RunSpec, onEvent engine.OnEventFunc) (engine.RunResult, error) {
	select {
	case <-time.After(m.delay):
		return m.mockRunner.Run(ctx, spec, onEvent)
	case <-ctx.Done():
		return engine.RunResult{IsError: true, Subtype: "timeout"}, ctx.Err()
	}
}
