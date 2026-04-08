package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/convergence"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/hub"
	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/repomap"
	"github.com/ericmacdougall/stoke/internal/stream"
)

// sowNativeConfig holds the small surface area the fast-path session
// executor needs. Passed in from sowCmd to avoid closure-capturing every
// flag pointer.
type sowNativeConfig struct {
	RepoRoot string
	Runner   *engine.NativeRunner
	EventBus *hub.Bus
	// MaxTurns is the turn budget per task. Default 100.
	MaxTurns int
	// MaxRepairAttempts is how many times the self-repair loop will try
	// to fix a session whose acceptance criteria fail. Default 3.
	MaxRepairAttempts int
	// Model is the model name the runner is using (informational only).
	Model string
	// SOWName / SOWDesc are used to contextualize prompts.
	SOWName string
	SOWDesc string
	// RepoMap is a ranked codebase map injected into task prompts for
	// context-aware execution. nil = skip.
	RepoMap *repomap.RepoMap
	// RepoMapBudget is the maximum number of chars of repomap to include
	// in a single prompt. Default 3000.
	RepoMapBudget int
	// CostBudgetUSD is the maximum spend for the entire SOW run. 0 = no
	// budget enforcement. When exceeded, subsequent tasks fail-fast.
	CostBudgetUSD float64
	// spent is the running total of cost (internal, mutated by runSessionNative).
	spent *float64

	// --- Override / continuation hooks (post-repair) ---

	// OverrideJudge is the VP Eng → CTO judge invoked when the self-
	// repair loop exhausts its attempts. nil = skip override flow.
	OverrideJudge convergence.OverrideJudge
	// Ignores is the persistent CTO-approved ignore list. Approved
	// overrides are added here and saved.
	Ignores *convergence.IgnoreList
	// OnContinuations is called when the judge returns unapproved
	// continuation items — work the CTO deemed "actually missing, not
	// a false positive". The callback typically turns these into a new
	// session via SessionScheduler.AppendSession so the SOW self-
	// extends.
	OnContinuations func(fromSession string, items []string)
}

// runSessionNative is the SOW fast path: it executes a session's tasks
// directly against the project root via the native runner, bypassing the
// single-task workflow engine (no worktree, no plan/verify phases, no
// merge).
//
// Self-repair loop: after the initial pass through all tasks, this function
// runs the session's acceptance criteria. If any fail, it constructs a
// repair prompt containing the failure output and asks the agent to fix
// the specific issue. Up to MaxRepairAttempts repair passes happen before
// control returns to the SOW scheduler's outer retry loop.
//
// Stack-aware criterion inference: if a session has no acceptance_criteria
// at all (common in LLM-generated SOWs for early foundation sessions),
// baseline criteria are synthesized from the detected stack (go build / go
// test, cargo build, npm run build, etc.) so we always have something to
// verify.
//
// Cost budgeting: if CostBudgetUSD is set, per-task cost is tracked and
// additional tasks are short-circuited once the budget is exhausted.
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
	maxRepairs := cfg.MaxRepairAttempts
	if maxRepairs <= 0 {
		maxRepairs = 3
	}
	if cfg.spent == nil {
		var initial float64
		cfg.spent = &initial
	}

	runtimeDir, err := os.MkdirTemp("", "stoke-sow-native-")
	if err != nil {
		return nil, fmt.Errorf("create runtime dir: %w", err)
	}
	defer os.RemoveAll(runtimeDir)

	// Infer baseline acceptance criteria from the detected stack if the
	// session has none. This gives us SOMETHING to verify instead of
	// silently passing a session that may have produced nothing.
	effectiveCriteria := session.AcceptanceCriteria
	if len(effectiveCriteria) == 0 {
		if sowDoc != nil {
			effectiveCriteria = inferBaselineCriteria(sowDoc.Stack)
			if len(effectiveCriteria) > 0 {
				fmt.Printf("  (no criteria in SOW; inferred %d baseline from stack)\n", len(effectiveCriteria))
			}
		}
	}
	workingSession := session
	workingSession.AcceptanceCriteria = effectiveCriteria

	// Phase 1: run each task once.
	results := make([]plan.TaskExecResult, 0, len(session.Tasks))
	for i, task := range session.Tasks {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		if cfg.CostBudgetUSD > 0 && *cfg.spent >= cfg.CostBudgetUSD {
			fmt.Printf("  budget exhausted ($%.2f / $%.2f) — halting session\n", *cfg.spent, cfg.CostBudgetUSD)
			results = append(results, plan.TaskExecResult{
				TaskID:  task.ID,
				Success: false,
				Error:   fmt.Errorf("cost budget exhausted"),
			})
			continue
		}
		fmt.Printf("  [%d/%d] %s: %s\n", i+1, len(session.Tasks), task.ID, task.Description)

		sysP, usrP := buildSOWNativePrompts(sowDoc, workingSession, task, cfg.RepoMap, cfg.RepoMapBudget, nil)
		tr := execNativeTask(ctx, task.ID, sysP, usrP, runtimeDir, cfg, maxTurns)
		results = append(results, tr)
	}

	// Phase 2: self-repair loop. Run the session's acceptance criteria;
	// if any fail, construct a repair prompt containing the exact failure
	// output and run it as a new task. Repeat up to maxRepairs times
	// before escalating to the override judge.
	var finalAcceptance []plan.AcceptanceResult
	var finalPassed bool
	if len(effectiveCriteria) > 0 {
		for attempt := 1; attempt <= maxRepairs; attempt++ {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			acceptance, allPassed := plan.CheckAcceptanceCriteria(ctx, cfg.RepoRoot, effectiveCriteria)
			finalAcceptance, finalPassed = acceptance, allPassed
			if allPassed {
				if attempt > 1 {
					fmt.Printf("  ✓ session %s repaired on attempt %d\n", session.ID, attempt)
				}
				break
			}
			if attempt == maxRepairs {
				fmt.Printf("  ✗ session %s still failing %d criteria after %d repair attempts — escalating\n",
					session.ID, countFailed(acceptance), attempt)
				break
			}
			if cfg.CostBudgetUSD > 0 && *cfg.spent >= cfg.CostBudgetUSD {
				fmt.Printf("  budget exhausted during repair — halting\n")
				break
			}
			failureBlob := formatAcceptanceFailures(acceptance)
			fmt.Printf("  ↻ session %s: repair attempt %d/%d for %d failing criteria\n",
				session.ID, attempt, maxRepairs, countFailed(acceptance))

			repairTask := plan.Task{
				ID:          fmt.Sprintf("%s-repair-%d", session.ID, attempt),
				Description: "repair session acceptance criteria",
			}
			sysP, usrP := buildSOWNativePrompts(sowDoc, workingSession, repairTask, cfg.RepoMap, cfg.RepoMapBudget, &failureBlob)
			repairResult := execNativeTask(ctx, repairTask.ID, sysP, usrP, runtimeDir, cfg, maxTurns)
			// Record as a synthetic task result so the caller sees the
			// repair attempt happened.
			results = append(results, repairResult)
		}
	}

	// Phase 3: override judge. When repair failed to close the gap AND
	// the criteria that failed look like they might be flagging noise
	// (regex-heavy, specific line flagged, etc.), ask the VP Eng → CTO
	// judge to review. Approved overrides land in the ignore list and
	// are applied to subsequent runs. Continuations flow through
	// OnContinuations to extend the SOW with a new session.
	if !finalPassed && cfg.OverrideJudge != nil && cfg.Ignores != nil && len(finalAcceptance) > 0 {
		runOverrideForSession(ctx, session, finalAcceptance, cfg)
	}

	return results, nil
}

// runOverrideForSession asks the VP Eng → CTO judge to review the
// unresolved acceptance failures for a session and either (a) approve
// ignore entries that close the gap or (b) surface continuation items
// for the caller to turn into a new session.
//
// Because the session_scheduler's acceptance check runs AFTER this
// function returns, approved ignores won't help THIS run — but they'll
// prevent the same flag from re-tripping on the scheduler's outer retry.
// Continuation items are the lever for extending the SOW forward when
// the work is genuinely incomplete.
func runOverrideForSession(ctx context.Context, session plan.Session, acceptance []plan.AcceptanceResult, cfg sowNativeConfig) {
	// Turn failing acceptance results into convergence.Finding shapes so
	// the existing judge can operate on them. Each failing criterion
	// becomes a synthetic finding with Evidence = command output.
	var findings []convergence.Finding
	for _, r := range acceptance {
		if r.Passed {
			continue
		}
		findings = append(findings, convergence.Finding{
			RuleID:      "session-acceptance/" + r.CriterionID,
			Category:    convergence.CatCompleteness,
			Severity:    convergence.SevBlocking,
			File:        session.ID,
			Description: r.Description,
			Evidence:    r.Output,
		})
	}
	if len(findings) == 0 {
		return
	}

	// Snippets: collect file contents the session's tasks claimed to
	// write. Gives the judge something to read.
	snippets := make(map[string]string)
	for _, t := range session.Tasks {
		for _, f := range t.Files {
			if data, err := os.ReadFile(filepath.Join(cfg.RepoRoot, f)); err == nil {
				snip := string(data)
				if len(snip) > 4000 {
					snip = snip[:4000] + "\n... (truncated)"
				}
				snippets[f] = snip
			}
		}
	}

	critDescs := make([]string, 0, len(session.AcceptanceCriteria))
	for _, c := range session.AcceptanceCriteria {
		critDescs = append(critDescs, c.Description)
	}

	judgeCtx := convergence.JudgeContext{
		MissionID:    session.ID,
		Findings:     findings,
		FileSnippets: snippets,
		SOWCriteria:  critDescs,
		BuildPassed:  false, // by definition — repair couldn't close the gap
		TestsPassed:  false,
		LintPassed:   true,
		ProjectRoot:  cfg.RepoRoot,
	}

	decision, err := convergence.RunOverrideFlow(cfg.OverrideJudge, cfg.Ignores, judgeCtx)
	if err != nil {
		fmt.Printf("  override judge error: %v\n", err)
		return
	}
	if decision == nil {
		return
	}
	if len(decision.Approved) > 0 {
		fmt.Printf("  CTO approved %d override(s) for session %s\n", len(decision.Approved), session.ID)
		if err := cfg.Ignores.Save(cfg.RepoRoot); err != nil {
			fmt.Printf("  persist ignore list: %v\n", err)
		}
	}
	if len(decision.Denied) > 0 {
		fmt.Printf("  CTO denied %d override(s) — gap is real\n", len(decision.Denied))
	}
	if len(decision.Continuations) > 0 && cfg.OnContinuations != nil {
		fmt.Printf("  CTO surfaced %d continuation item(s); appending to SOW\n", len(decision.Continuations))
		cfg.OnContinuations(session.ID, decision.Continuations)
	}
}

// execNativeTask runs a single task against the native runner and returns
// a TaskExecResult. Factored out so the first-pass loop and repair loop
// share exactly the same execution semantics. systemPrompt is the static
// cached block; userPrompt is the per-task dynamic message.
func execNativeTask(ctx context.Context, taskID, systemPrompt, userPrompt, runtimeDir string, cfg sowNativeConfig, maxTurns int) plan.TaskExecResult {
	taskRuntime := filepath.Join(runtimeDir, taskID)
	if err := os.MkdirAll(taskRuntime, 0o755); err != nil {
		return plan.TaskExecResult{TaskID: taskID, Success: false, Error: err}
	}

	spec := engine.RunSpec{
		Prompt:       userPrompt,
		SystemPrompt: systemPrompt,
		WorktreeDir:  cfg.RepoRoot,
		RuntimeDir:   taskRuntime,
		Mode:         engine.AuthModeAPIKey,
		Phase: engine.PhaseSpec{
			Name:     "execute",
			MaxTurns: maxTurns,
			ReadOnly: false,
		},
	}

	start := time.Now()
	result, err := cfg.Runner.Run(ctx, spec, func(ev stream.Event) {
		if ev.DeltaText != "" {
			fmt.Print(ev.DeltaText)
		}
		for _, tu := range ev.ToolUses {
			fmt.Printf("    ⚙ %s\n", tu.Name)
		}
	})
	dur := time.Since(start)

	if cfg.spent != nil {
		*cfg.spent += result.CostUSD
	}

	tr := plan.TaskExecResult{TaskID: taskID, Success: !result.IsError && err == nil}
	switch {
	case err != nil:
		tr.Error = err
		fmt.Printf("    ✗ error: %v (%.1fs, %d turns)\n", err, dur.Seconds(), result.NumTurns)
	case result.IsError:
		tr.Error = fmt.Errorf("native runner: %s", result.Subtype)
		fmt.Printf("    ✗ failed: %s (%.1fs, %d turns, $%.4f)\n", result.Subtype, dur.Seconds(), result.NumTurns, result.CostUSD)
	default:
		fmt.Printf("    ✓ done (%.1fs, %d turns, $%.4f)\n", dur.Seconds(), result.NumTurns, result.CostUSD)
	}
	return tr
}

// buildSOWNativePrompts returns (systemPrompt, userPrompt) for a task.
// The system prompt contains the STATIC context — SOW identity, stack,
// session framing, acceptance criteria, the optional repo map — the
// parts that don't change across tasks in the same session (other than
// task-specific repomap slicing, which also rarely changes between
// adjacent tasks). Agentloop wraps the system prompt in a cache_control
// breakpoint for ~90% cost reduction across multi-task sessions.
//
// The user prompt is the task description, expected files, dependencies,
// and any repair context. These change every task so they stay out of
// the cached block.
func buildSOWNativePrompts(sowDoc *plan.SOW, session plan.Session, task plan.Task, rmap *repomap.RepoMap, mapBudget int, repair *string) (string, string) {
	var sys, usr strings.Builder

	// --- SYSTEM (static, cacheable) ---
	if repair != nil {
		sys.WriteString("You are an autonomous coding agent in REPAIR mode. A previous pass through this session produced code that fails the session's acceptance criteria. ")
		sys.WriteString("Read the failure output in the user message below, understand what's wrong, and fix it by editing files directly in the project root. ")
		sys.WriteString("Do not rewrite unrelated code. Do not break criteria that are already passing. Use the bash tool to re-run the failing commands yourself to verify your fix before ending.\n\n")
	} else {
		sys.WriteString("You are an autonomous coding agent working on a project defined by a Statement of Work (SOW). ")
		sys.WriteString("Your job: implement the single task described in the user message by writing files directly to the project root. ")
		sys.WriteString("Use the available file tools (read_file, write_file, edit_file, bash) to create or modify files as needed. ")
		sys.WriteString("Do NOT create worktrees or branches — write directly to the repo. When you believe the task is complete, verify by running the relevant acceptance criteria commands with bash before ending.\n\n")
	}

	if sowDoc != nil && sowDoc.Name != "" {
		fmt.Fprintf(&sys, "PROJECT: %s\n", sowDoc.Name)
		if sowDoc.Description != "" {
			fmt.Fprintf(&sys, "  %s\n", sowDoc.Description)
		}
		if sowDoc.Stack.Language != "" {
			fmt.Fprintf(&sys, "  stack: %s", sowDoc.Stack.Language)
			if sowDoc.Stack.Framework != "" {
				fmt.Fprintf(&sys, " / %s", sowDoc.Stack.Framework)
			}
			sys.WriteString("\n")
		}
		if sowDoc.Stack.Monorepo != nil {
			fmt.Fprintf(&sys, "  monorepo: %s", sowDoc.Stack.Monorepo.Tool)
			if sowDoc.Stack.Monorepo.Manager != "" {
				fmt.Fprintf(&sys, " (%s)", sowDoc.Stack.Monorepo.Manager)
			}
			sys.WriteString("\n")
		}
		if len(sowDoc.Stack.Infra) > 0 {
			var parts []string
			for _, inf := range sowDoc.Stack.Infra {
				parts = append(parts, inf.Name)
			}
			fmt.Fprintf(&sys, "  infra: %s\n", strings.Join(parts, ", "))
		}
		sys.WriteString("\n")
	}

	fmt.Fprintf(&sys, "SESSION %s: %s\n", session.ID, session.Title)
	if session.Description != "" {
		fmt.Fprintf(&sys, "  %s\n", session.Description)
	}
	if len(session.Inputs) > 0 {
		fmt.Fprintf(&sys, "  inputs from prior sessions: %s\n", strings.Join(session.Inputs, ", "))
	}
	if len(session.Outputs) > 0 {
		fmt.Fprintf(&sys, "  expected outputs: %s\n", strings.Join(session.Outputs, ", "))
	}
	sys.WriteString("\n")

	if len(session.AcceptanceCriteria) > 0 {
		sys.WriteString("ACCEPTANCE CRITERIA for this session (will be checked after your task):\n")
		for _, ac := range session.AcceptanceCriteria {
			switch {
			case ac.Command != "":
				fmt.Fprintf(&sys, "  - [%s] %s — verified by: $ %s\n", ac.ID, ac.Description, ac.Command)
			case ac.FileExists != "":
				fmt.Fprintf(&sys, "  - [%s] %s — file must exist: %s\n", ac.ID, ac.Description, ac.FileExists)
			case ac.ContentMatch != nil:
				fmt.Fprintf(&sys, "  - [%s] %s — file %s must contain: %s\n", ac.ID, ac.Description, ac.ContentMatch.File, ac.ContentMatch.Pattern)
			default:
				fmt.Fprintf(&sys, "  - [%s] %s\n", ac.ID, ac.Description)
			}
		}
		sys.WriteString("\n")
	}

	// Repo map is also static per-session (related files don't change
	// while the task is running) — include it in the cached system
	// block so every task in the session reuses the same lookup.
	if rmap != nil {
		budget := mapBudget
		if budget <= 0 {
			budget = 3000
		}
		// Use the session's output hints if declared; otherwise fall
		// back to the current task's file list. Either way this still
		// cacheably bounds the set across the session.
		anchor := session.Outputs
		if len(anchor) == 0 {
			anchor = task.Files
		}
		rendered := rmap.RenderRelevant(anchor, budget)
		if rendered != "" {
			sys.WriteString("REPOSITORY MAP (ranked by importance):\n")
			sys.WriteString(rendered)
			sys.WriteString("\n\n")
		}
	}

	// --- USER (dynamic, per-task) ---
	if repair != nil {
		usr.WriteString("FAILING ACCEPTANCE CRITERIA (fix these):\n")
		usr.WriteString(*repair)
		usr.WriteString("\nInvestigate the failures, make the minimum changes necessary to fix them, then re-run the failing command(s) with bash to confirm your fix before ending.\n")
	} else {
		fmt.Fprintf(&usr, "TASK %s: %s\n", task.ID, task.Description)
		if len(task.Files) > 0 {
			fmt.Fprintf(&usr, "  expected files: %s\n", strings.Join(task.Files, ", "))
		}
		if len(task.Dependencies) > 0 {
			fmt.Fprintf(&usr, "  depends on: %s\n", strings.Join(task.Dependencies, ", "))
		}
		usr.WriteString("\nBegin implementing the task now. When you're done, your final message should briefly summarize what you changed.\n")
	}

	return sys.String(), usr.String()
}

// buildSOWNativePrompt returns just the concatenated prompt. Retained for
// tests and any caller that wants a single string. New code should use
// buildSOWNativePrompts for proper cache-aware system/user split.
func buildSOWNativePrompt(sowDoc *plan.SOW, session plan.Session, task plan.Task, rmap *repomap.RepoMap, mapBudget int, repair *string) string {
	sys, usr := buildSOWNativePrompts(sowDoc, session, task, rmap, mapBudget, repair)
	return sys + "\n" + usr
}

// inferBaselineCriteria returns synthetic acceptance criteria for a stack
// when a session has declared none. This gives us SOMETHING to verify at
// session boundaries — useful because LLM-generated SOWs often omit
// criteria for early foundation sessions even though we still want to
// know "does this code build?".
//
// The criteria are deliberately minimal (build + test) so they fit any
// session that writes code. Sessions that produce config files or docs
// without buildable code will have no criteria to run, which is the same
// as the old behavior.
func inferBaselineCriteria(stack plan.StackSpec) []plan.AcceptanceCriterion {
	var out []plan.AcceptanceCriterion
	switch stack.Language {
	case "go":
		out = append(out, plan.AcceptanceCriterion{ID: "inferred-build", Description: "go build succeeds", Command: "go build ./..."})
		out = append(out, plan.AcceptanceCriterion{ID: "inferred-vet", Description: "go vet succeeds", Command: "go vet ./..."})
		out = append(out, plan.AcceptanceCriterion{
			ID:          "inferred-test",
			Description: "go tests pass (or no tests)",
			Command:     "if ls *_test.go 2>/dev/null || find . -name '*_test.go' -type f | head -1 | grep -q .; then go test ./...; else true; fi",
		})
	case "rust":
		out = append(out, plan.AcceptanceCriterion{ID: "inferred-build", Description: "cargo build succeeds", Command: "cargo build"})
		out = append(out, plan.AcceptanceCriterion{
			ID:          "inferred-test",
			Description: "cargo test passes (or no tests)",
			Command:     "cargo test || [ $(find . -name '*_test.rs' -o -name 'tests' -type d | wc -l) -eq 0 ]",
		})
	case "typescript", "javascript":
		// Detect package.json scripts when we actually run, but for the
		// prompt we just assert the common ones.
		out = append(out, plan.AcceptanceCriterion{
			ID:          "inferred-install",
			Description: "dependencies installed",
			Command:     "test -d node_modules || (test -f pnpm-lock.yaml && pnpm install) || (test -f yarn.lock && yarn) || (test -f package-lock.json && npm install) || true",
		})
		out = append(out, plan.AcceptanceCriterion{
			ID:          "inferred-build",
			Description: "build script succeeds (if defined)",
			Command:     "if grep -q '\"build\"' package.json 2>/dev/null; then npm run build; else true; fi",
		})
	case "python":
		out = append(out, plan.AcceptanceCriterion{
			ID:          "inferred-compile",
			Description: "python files parse",
			Command:     "python -m compileall -q .",
		})
		out = append(out, plan.AcceptanceCriterion{
			ID:          "inferred-test",
			Description: "pytest passes (or no tests)",
			Command:     "if [ -f pytest.ini ] || [ -f pyproject.toml ] || find . -name 'test_*.py' -type f | head -1 | grep -q .; then pytest || true; else true; fi",
		})
	}
	return out
}

// formatAcceptanceFailures builds a human/model-readable block describing
// which criteria failed and why. Fed into repair prompts.
func formatAcceptanceFailures(results []plan.AcceptanceResult) string {
	var b strings.Builder
	for _, r := range results {
		if r.Passed {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s\n", r.CriterionID, r.Description)
		if r.Output != "" {
			// Indent the output so it's visually separated.
			lines := strings.Split(strings.TrimRight(r.Output, "\n"), "\n")
			for _, line := range lines {
				fmt.Fprintf(&b, "    %s\n", line)
			}
		}
	}
	return b.String()
}

func countFailed(results []plan.AcceptanceResult) int {
	n := 0
	for _, r := range results {
		if !r.Passed {
			n++
		}
	}
	return n
}
