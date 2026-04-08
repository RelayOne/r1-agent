package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/hub"
	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/stream"
)

// sowNativeConfig holds the small surface area the fast-path session
// executor needs. Passed in from sowCmd to avoid closure-capturing every
// flag pointer.
type sowNativeConfig struct {
	RepoRoot      string
	Runner        *engine.NativeRunner
	EventBus      *hub.Bus
	MaxTurns      int // per-task turn budget (default 100)
	Model         string
	SOWName       string
	SOWDesc       string
}

// runSessionNative is the SOW fast path: it executes a session's tasks
// directly against the project root via the native runner, bypassing the
// single-task workflow engine (no worktree, no plan/verify phases, no
// merge). Acceptance criteria are still evaluated by the SOW session
// scheduler after this function returns.
//
// Motivation (Bug 5): the old sessionExecFn delegated to runBuild → app.New
// → workflow.Engine which:
//   - Requires cfg.Task to be set (but SOW passes task descriptions, not
//     a single Task field)
//   - Creates a git worktree per task (heavy for greenfield builds)
//   - Runs plan/execute/verify phases with prompts tuned for modifying an
//     existing codebase, not generating new code from a SOW
//   - Merges back to main (meaningless when the repo has only an initial
//     commit seeded by EnsureRepo)
//
// The fast path does what the user actually wants: for each task, give the
// native agentloop the task description + session context + acceptance
// criteria, let it write files directly in the repo, and report success
// based on whether the agentloop ran to completion without erroring out.
// The session scheduler then runs the acceptance criteria and decides
// whether to move on or retry.
func runSessionNative(ctx context.Context, session plan.Session, sowDoc *plan.SOW, cfg sowNativeConfig) ([]plan.TaskExecResult, error) {
	if cfg.Runner == nil {
		return nil, fmt.Errorf("runSessionNative: native runner is nil (check --runner / --native-api-key)")
	}
	if cfg.RepoRoot == "" {
		return nil, fmt.Errorf("runSessionNative: empty repo root")
	}
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 100
	}

	runtimeDir, err := os.MkdirTemp("", "stoke-sow-native-")
	if err != nil {
		return nil, fmt.Errorf("create runtime dir: %w", err)
	}
	defer os.RemoveAll(runtimeDir)

	results := make([]plan.TaskExecResult, 0, len(session.Tasks))
	for i, task := range session.Tasks {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		fmt.Printf("  [%d/%d] %s: %s\n", i+1, len(session.Tasks), task.ID, task.Description)

		prompt := buildSOWNativePrompt(sowDoc, session, task)

		// Per-task runtime subdir so tool output paths don't collide
		// between tasks running in sequence.
		taskRuntime := filepath.Join(runtimeDir, task.ID)
		if err := os.MkdirAll(taskRuntime, 0o755); err != nil {
			results = append(results, plan.TaskExecResult{TaskID: task.ID, Success: false, Error: err})
			continue
		}

		spec := engine.RunSpec{
			Prompt:      prompt,
			WorktreeDir: cfg.RepoRoot,
			RuntimeDir:  taskRuntime,
			Mode:        engine.AuthModeAPIKey,
			Phase: engine.PhaseSpec{
				Name:     "execute",
				MaxTurns: maxTurns,
				ReadOnly: false,
			},
		}

		start := time.Now()
		result, err := cfg.Runner.Run(ctx, spec, func(ev stream.Event) {
			// Minimal streaming output: echo tool use and text deltas to
			// stdout so the user sees progress. Full event routing
			// happens via the EventBus hub.
			if ev.DeltaText != "" {
				fmt.Print(ev.DeltaText)
			}
			for _, tu := range ev.ToolUses {
				fmt.Printf("    ⚙ %s\n", tu.Name)
			}
		})
		dur := time.Since(start)

		taskResult := plan.TaskExecResult{TaskID: task.ID, Success: !result.IsError && err == nil}
		if err != nil {
			taskResult.Error = err
			fmt.Printf("    ✗ error: %v (%.1fs, %d turns)\n", err, dur.Seconds(), result.NumTurns)
		} else if result.IsError {
			taskResult.Error = fmt.Errorf("native runner: %s", result.Subtype)
			fmt.Printf("    ✗ failed: %s (%.1fs, %d turns, $%.4f)\n",
				result.Subtype, dur.Seconds(), result.NumTurns, result.CostUSD)
		} else {
			fmt.Printf("    ✓ done (%.1fs, %d turns, $%.4f)\n",
				dur.Seconds(), result.NumTurns, result.CostUSD)
		}
		results = append(results, taskResult)
	}
	return results, nil
}

// buildSOWNativePrompt constructs the task prompt sent to the native
// agentloop. It includes SOW context, session context, the specific task,
// and the session's acceptance criteria so the agent knows what "done"
// looks like for this session.
func buildSOWNativePrompt(sowDoc *plan.SOW, session plan.Session, task plan.Task) string {
	var b strings.Builder

	b.WriteString("You are an autonomous coding agent working on a project defined by a Statement of Work (SOW). ")
	b.WriteString("Your job: implement the single task described below by writing files directly to the project root. ")
	b.WriteString("Use the available file tools (read_file, write_file, edit_file, bash) to create or modify files as needed. ")
	b.WriteString("Do NOT create worktrees or branches — write directly to the repo.\n\n")

	if sowDoc != nil && sowDoc.Name != "" {
		fmt.Fprintf(&b, "PROJECT: %s\n", sowDoc.Name)
		if sowDoc.Description != "" {
			fmt.Fprintf(&b, "  %s\n", sowDoc.Description)
		}
		if sowDoc.Stack.Language != "" {
			fmt.Fprintf(&b, "  stack: %s", sowDoc.Stack.Language)
			if sowDoc.Stack.Framework != "" {
				fmt.Fprintf(&b, " / %s", sowDoc.Stack.Framework)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "SESSION %s: %s\n", session.ID, session.Title)
	if session.Description != "" {
		fmt.Fprintf(&b, "  %s\n", session.Description)
	}
	if len(session.Inputs) > 0 {
		fmt.Fprintf(&b, "  inputs from prior sessions: %s\n", strings.Join(session.Inputs, ", "))
	}
	if len(session.Outputs) > 0 {
		fmt.Fprintf(&b, "  expected outputs: %s\n", strings.Join(session.Outputs, ", "))
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "TASK %s: %s\n", task.ID, task.Description)
	if len(task.Files) > 0 {
		fmt.Fprintf(&b, "  expected files: %s\n", strings.Join(task.Files, ", "))
	}
	if len(task.Dependencies) > 0 {
		fmt.Fprintf(&b, "  depends on: %s\n", strings.Join(task.Dependencies, ", "))
	}
	b.WriteString("\n")

	if len(session.AcceptanceCriteria) > 0 {
		b.WriteString("ACCEPTANCE CRITERIA for this session (will be checked after your task):\n")
		for _, ac := range session.AcceptanceCriteria {
			switch {
			case ac.Command != "":
				fmt.Fprintf(&b, "  - [%s] %s — verified by: $ %s\n", ac.ID, ac.Description, ac.Command)
			case ac.FileExists != "":
				fmt.Fprintf(&b, "  - [%s] %s — file must exist: %s\n", ac.ID, ac.Description, ac.FileExists)
			case ac.ContentMatch != nil:
				fmt.Fprintf(&b, "  - [%s] %s — file %s must contain: %s\n", ac.ID, ac.Description, ac.ContentMatch.File, ac.ContentMatch.Pattern)
			default:
				fmt.Fprintf(&b, "  - [%s] %s\n", ac.ID, ac.Description)
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("Begin implementing the task now. When you're done, your final message should briefly summarize what you changed.\n")
	return b.String()
}
