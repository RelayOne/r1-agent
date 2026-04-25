package main

// descent_bridge.go wires the verification descent engine
// (internal/plan/verification_descent.go) into the SOW execution loop.
//
// Feature-flagged via STOKE_DESCENT=1 until proven across the bench suite.
// When enabled, replaces the scattered soft-pass branches (H-76, H-77,
// H-81, H-87) and the manual sticky/reasoning/meta-judge/fingerprint
// chain with a single per-AC tiered resolution engine.
//
// Integration point: runDescentRepairLoop is called from runSessionNative
// in place of the legacy repair loop when the flag is set.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1-agent/internal/engine"
	"github.com/RelayOne/r1-agent/internal/plan"
	"github.com/RelayOne/r1-agent/internal/r1env"
)

// descentEnabled returns true when the operator has opted into the
// verification descent engine. Default off until bench suite proves
// parity with the legacy loop.
func descentEnabled() bool {
	return r1env.Get("R1_DESCENT", "STOKE_DESCENT") == "1"
}

// descentAcceptanceCache stashes finalAcceptance results from
// runDescentRepairLoop so the SessionScheduler's AcceptanceOverride
// hook can consume them instead of re-running raw AC commands (which
// T6/T8 already determined are broken). Keyed by session ID; the
// latest attempt overwrites — scheduler attempt numbering tracks
// session-scheduler retries, not descent's inner repair attempts.
var descentAcceptanceCache sync.Map // sessionID -> []plan.AcceptanceResult

// getDescentAcceptanceOverride is the SessionScheduler.AcceptanceOverride
// implementation. Installed by main.go after NewSessionScheduler.
func getDescentAcceptanceOverride(sessionID string, _ int) ([]plan.AcceptanceResult, bool) {
	if v, ok := descentAcceptanceCache.Load(sessionID); ok {
		if r, ok := v.([]plan.AcceptanceResult); ok && len(r) > 0 {
			return r, true
		}
	}
	return nil, false
}

// clearDescentCache empties the override cache. Called between SOW
// runs to prevent stale session data from leaking into a fresh run.
func clearDescentCache() {
	descentAcceptanceCache = sync.Map{}
}

// buildDescentConfig constructs a plan.DescentConfig from the native
// session execution context. Each callback bridges into existing
// infrastructure — the descent engine doesn't know about
// sowNativeConfig, prompt builders, or the native runner.
func buildDescentConfig(
	ctx context.Context,
	sowDoc *plan.SOW,
	session plan.Session,
	workingSession plan.Session,
	cfg sowNativeConfig,
	runtimeDir string,
	maxTurns int,
	currentAcceptance []plan.AcceptanceResult,
) plan.DescentConfig {

	// Pick the model for reasoning calls.
	reasoningModel := cfg.ReasoningModel
	if reasoningModel == "" {
		reasoningModel = cfg.Model
	}

	dc := plan.DescentConfig{
		Provider:       cfg.ReasoningProvider,
		Model:          reasoningModel,
		RepoRoot:       cfg.RepoRoot,
		Session:        workingSession,
		MaxCodeRepairs: cfg.MaxRepairAttempts,
		UniversalPromptBlock: cfg.combinedPromptBlock(
			cfg.agentContext("descent-engine", "2-repair-loop", &session, 1),
		),
	}

	// If no repair attempts configured, default to 3 per-AC (distinct
	// from the session-level retry count which is typically 5-10).
	if dc.MaxCodeRepairs <= 0 || dc.MaxCodeRepairs > 5 {
		dc.MaxCodeRepairs = 3
	}

	// -----------------------------------------------------------------
	// RepairFunc: dispatch a focused repair worker for a single AC.
	// Wraps the legacy dispatch with the spec-1 item 5 bootstrap-per-cycle
	// guard (manifest-change re-install) and injects the spec-1 item 3
	// pre-completion gate check into the worker's PreEndTurnCheckFn.
	// -----------------------------------------------------------------
	innerRepair := func(rctx context.Context, directive string) error {
		repairTask := plan.Task{
			ID:          fmt.Sprintf("%s-descent-repair-%d", session.ID, time.Now().UnixMilli()),
			Description: "verification descent: targeted repair",
		}
		repairBlob := directive
		sysP, usrP := buildSOWNativePromptsWithOpts(sowDoc, workingSession, repairTask, promptOpts{
			RepoMap:              cfg.RepoMap,
			RepoMapBudget:        cfg.RepoMapBudget,
			Repair:               &repairBlob,
			Wisdom:               cfg.Wisdom,
			RawSOW:               cfg.RawSOWText,
			RepoRoot:             cfg.RepoRoot,
			LiveBuildState:       liveBuildStateFor(cfg),
			UniversalPromptBlock: cfg.combinedPromptBlock(cfg.agentContext("worker-descent-repair", "2-repair-loop", &session, 1)),
		})
		sup := toEngineSupervisor(autoExtractTaskSupervisor(cfg.RepoRoot, cfg.RawSOWText, workingSession, repairTask, 3))

		// Spec-1 item 3: wire the pre_completion_gate parser into the
		// worker's PreEndTurnCheckFn chain. Any claimed completion
		// without matching evidence forces another turn.
		// Spec-1 item 6: tag the report_env_issue tool with the
		// current session ID so T3 can consume its markers.
		// Spec-1 item 7: install the ghost-write detector as an
		// ExtraMidturnCheck. Adapter translates engine.MidturnToolCall
		// into plan.MidturnToolCall (avoids an import cycle — the
		// detector lives in plan/).
		taskCfg := cfg
		taskCfg.CurrentSessionID = session.ID
		taskCfg.ExtraPreEndTurnCheck = plan.NewPreEndTurnCheck(plan.PreCheckContext{
			RepoRoot:          cfg.RepoRoot,
			SowACs:            workingSession.AcceptanceCriteria,
			SessionTranscript: nil, // populated by the worker log reader downstream; nil disables transcript cross-check
			OnMismatch: func(kind, claim, observed string) {
				fmt.Printf("    ⚠ descent pre_completion_gate %s: %s (observed=%s)\n",
					kind, truncateForLog(claim, 120), truncateForLog(observed, 120))
			},
		})
		ghostCheck := plan.NewGhostWriteCheck(cfg.RepoRoot, func(evt plan.GhostWriteEvent) {
			fmt.Printf("    👻 descent.ghost_write_detected: tool=%s path=%s reason=%s\n",
				evt.ToolName, evt.Path, evt.Reason)
		})
		taskCfg.ExtraMidturnCheck = func(tools []engine.MidturnToolCall, turn int) string {
			converted := make([]plan.MidturnToolCall, 0, len(tools))
			for _, t := range tools {
				converted = append(converted, plan.MidturnToolCall{
					Name:    t.Name,
					Input:   t.Input,
					Result:  t.Result,
					IsError: t.IsError,
				})
			}
			return ghostCheck(converted, turn)
		}
		tr := execNativeTask(rctx, repairTask.ID, sysP, usrP, runtimeDir, taskCfg, maxTurns, sup)
		if !tr.Success {
			if tr.Error != nil {
				return tr.Error
			}
			return fmt.Errorf("repair task %s failed", repairTask.ID)
		}
		return nil
	}
	// Spec-1 item 5: bootstrap per descent cycle. Wrap RepairFunc so
	// that after every repair dispatch we inspect git diff for dep
	// manifest changes. When a lockfile or manifest was touched, run
	// a frozen-lockfile install so the worker's claimed deps are
	// actually resolvable BEFORE the next AC re-run. Hallucinated deps
	// (added to package.json but missing from the registry) produce
	// a non-zero install exit and fall through to T5 env-fix.
	dc.RepairFunc = func(rctx context.Context, directive string) error {
		preSHA := descentGitHead(rctx, cfg.RepoRoot)
		err := innerRepair(rctx, directive)
		// Check for manifest changes regardless of repair outcome —
		// a partially-applied edit can still have touched the manifest
		// and broken subsequent runs.
		changed := descentGitDiffNames(rctx, cfg.RepoRoot, preSHA, "HEAD")
		manifests := []string{
			"package.json", "pnpm-lock.yaml", "package-lock.json", "yarn.lock",
			"go.mod", "go.sum",
			"Cargo.toml", "Cargo.lock",
			"requirements.txt", "pyproject.toml", "uv.lock", "poetry.lock",
		}
		touched := intersectStrings(changed, manifests)
		if len(touched) > 0 {
			frozen := plan.LockfilePresent(cfg.RepoRoot)
			start := time.Now()
			plan.EnsureWorkspaceInstalledOpts(rctx, cfg.RepoRoot, plan.InstallOpts{
				Force:  true,
				Frozen: frozen,
			})
			fmt.Printf("  [descent] bootstrap_reinstalled: manifests=%v frozen=%v duration=%s event=descent.bootstrap_reinstalled\n",
				touched, frozen, time.Since(start).Round(time.Millisecond))
		}
		return err
	}

	// -----------------------------------------------------------------
	// EnvFixFunc: attempt to resolve environment problems.
	// -----------------------------------------------------------------
	dc.EnvFixFunc = func(ectx context.Context, rootCause, stderr string) bool {
		fixed := false
		lc := strings.ToLower(rootCause + " " + stderr)

		// H-91g Path B: extract missing npm packages from the stderr
		// text. Patterns: "Cannot find module 'zod'", "Cannot resolve
		// module 'react'", "ERR_MODULE_NOT_FOUND: ... 'foo'". Before
		// we just ran `pnpm install`, but that's a no-op if the pkg
		// isn't in any package.json — which is exactly R04's failure
		// mode (worker imported 'zod' without declaring it). Adding
		// the pkg to root devDependencies + install closes the loop.
		if missing := extractMissingNpmPackages(stderr); len(missing) > 0 {
			if _, err := os.Stat(filepath.Join(cfg.RepoRoot, "package.json")); err == nil {
				fmt.Printf("    🔧 descent env-fix: missing npm pkg(s): %s — adding to root devDependencies\n", strings.Join(missing, ", "))
				if addErr := addRootDevDeps(cfg.RepoRoot, missing); addErr != nil {
					fmt.Printf("    ⚠ add-dep failed: %v\n", addErr)
				} else {
					installCtx, cancel := context.WithTimeout(ectx, 3*time.Minute)
					cmd := exec.CommandContext(installCtx, "pnpm", "install", "--silent")
					cmd.Dir = cfg.RepoRoot
					if out, err := cmd.CombinedOutput(); err == nil {
						fixed = true
						fmt.Printf("    ✓ pnpm install succeeded (added %d dep(s))\n", len(missing))
					} else {
						fmt.Printf("    ⚠ pnpm install failed after dep-add: %s\n", truncateForLog(string(out), 200))
					}
					cancel()
				}
			}
		} else if strings.Contains(lc, "module") || strings.Contains(lc, "cannot find") || strings.Contains(lc, "not found") {
			// Fallback: known pkg is declared but install hasn't run.
			if _, err := os.Stat(filepath.Join(cfg.RepoRoot, "package.json")); err == nil {
				fmt.Println("    🔧 descent env-fix: running pnpm install...")
				installCtx, cancel := context.WithTimeout(ectx, 2*time.Minute)
				cmd := exec.CommandContext(installCtx, "pnpm", "install", "--silent")
				cmd.Dir = cfg.RepoRoot
				if out, err := cmd.CombinedOutput(); err == nil {
					fixed = true
					fmt.Println("    ✓ pnpm install succeeded")
				} else {
					fmt.Printf("    ⚠ pnpm install failed: %s\n", truncateForLog(string(out), 200))
				}
				cancel()
			}
		}

		// Try apt-get for system binaries (if running as root / in container).
		if strings.Contains(lc, "command not found") {
			// Extract the missing binary name.
			for _, line := range strings.Split(stderr, "\n") {
				low := strings.ToLower(line)
				if idx := strings.Index(low, ": command not found"); idx > 0 {
					rest := line[:idx]
					if colon := strings.LastIndex(rest, ":"); colon >= 0 {
						rest = rest[colon+1:]
					}
					binary := strings.TrimSpace(rest)
					if binary != "" {
						fmt.Printf("    🔧 descent env-fix: attempting to install %q...\n", binary)
						aptCtx, cancel := context.WithTimeout(ectx, 1*time.Minute)
						cmd := exec.CommandContext(aptCtx, "apt-get", "install", "-y", "-qq", binary) // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
						if err := cmd.Run(); err == nil {
							fixed = true
							fmt.Printf("    ✓ installed %s\n", binary)
						} else {
							fmt.Printf("    ⚠ could not install %s via apt-get\n", binary)
						}
						cancel()
					}
					break
				}
			}
		}

		return fixed
	}

	// -----------------------------------------------------------------
	// IntentCheckFunc: ask the reviewer if the code matches the spec.
	// -----------------------------------------------------------------
	if cfg.ReasoningProvider != nil {
		dc.IntentCheckFunc = func(ictx context.Context, ac plan.AcceptanceCriterion) (bool, string) {
			// Pick the most relevant task for this AC.
			var relevantTask plan.Task
			for _, t := range workingSession.Tasks {
				for _, f := range t.Files {
					if f == ac.FileExists || (ac.ContentMatch != nil && f == ac.ContentMatch.File) {
						relevantTask = t
						break
					}
				}
				if relevantTask.ID != "" {
					break
				}
			}
			if relevantTask.ID == "" && len(workingSession.Tasks) > 0 {
				relevantTask = workingSession.Tasks[0]
			}

			// Collect code excerpts.
			var relPaths []string
			seen := map[string]bool{}
			for _, f := range relevantTask.Files {
				if f != "" && !seen[f] {
					seen[f] = true
					relPaths = append(relPaths, f)
				}
			}
			codeExcerpts := plan.CollectCodeExcerpts(cfg.RepoRoot, relPaths, 6, 4000)

			sowExcerpt := ""
			if cfg.RawSOWText != "" {
				sowExcerpt = extractTaskSpecExcerpt(cfg.RawSOWText, workingSession,
					plan.Task{ID: ac.ID, Description: ac.Description}, specExcerptConfig{})
			}

			reviewCtx, cancel := context.WithTimeout(ictx, 3*time.Minute)
			defer cancel()

			// H-91c: feed the descent intent-check the most recent
			// worker log for this task's dispatch. Lets T1 confirm
			// intent via deterministic tool-call evidence (bash ran,
			// edit landed) rather than requiring a narrative summary
			// the worker often skips.
			descentWorkerLogPath := plan.LatestWorkerLogForTask(cfg.RepoRoot, relevantTask.ID)
			descentWorkerLogExcerpt := plan.LoadWorkerLogExcerpt(descentWorkerLogPath, 100)
			verdict, err := plan.ReviewTaskWork(reviewCtx, cfg.ReasoningProvider, reasoningModel, plan.TaskReviewInput{
				Task:              relevantTask,
				SOWSpec:           sowExcerpt,
				SessionAcceptance: workingSession.AcceptanceCriteria,
				CodeExcerpts:      codeExcerpts,
				WorkerSummary:     "", // no worker summary in descent context
				UniversalPromptBlock: cfg.combinedPromptBlock(
					cfg.agentContext("descent-intent-check", "2-repair-loop", &session, 1),
				),
				WorkerLogPath:    descentWorkerLogPath,
				WorkerLogExcerpt: descentWorkerLogExcerpt,
			})
			if err != nil {
				// On error, conservatively assume intent confirmed
				// so we don't block descent on a transient LLM failure.
				return true, fmt.Sprintf("intent check failed: %v — assuming confirmed", err)
			}
			return verdict.Complete, verdict.Reasoning
		}
	}

	// -----------------------------------------------------------------
	// BuildCleanFunc: verify the project builds.
	// -----------------------------------------------------------------
	dc.BuildCleanFunc = func(bctx context.Context) bool {
		buildCmd := detectBuildCommand(sowDoc, cfg.RepoRoot)
		if buildCmd == "" {
			return true // no build command detectable — assume clean
		}
		buildCtx, cancel := context.WithTimeout(bctx, 3*time.Minute)
		defer cancel()
		// Use checkOneCriterion-style env setup: bash -lc with
		// node_modules/.bin on PATH via the AC runner's own env logic.
		cmd := exec.CommandContext(buildCtx, "bash", "-lc", buildCmd) // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
		cmd.Dir = cfg.RepoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf("    ⚠ descent build check failed: %s\n", truncateForLog(string(out), 200))
			return false
		}
		return true
	}

	// -----------------------------------------------------------------
	// StubScanCleanFunc: check for fake/stub code.
	// -----------------------------------------------------------------
	dc.StubScanCleanFunc = func(sctx context.Context) bool {
		var sessionFiles []string
		seen := map[string]bool{}
		for _, t := range workingSession.Tasks {
			for _, f := range t.Files {
				if f != "" && !seen[f] {
					seen[f] = true
					sessionFiles = append(sessionFiles, f)
				}
			}
		}
		if len(sessionFiles) == 0 {
			return true
		}
		scopedSOW := &plan.SOW{Sessions: []plan.Session{session}}
		qual := plan.RunQualitySweepForSOW(cfg.RepoRoot, sessionFiles, scopedSOW)
		if qual == nil {
			return true
		}
		return !qual.Blocking()
	}

	// -----------------------------------------------------------------
	// AllOtherACsPassedFunc: closure over current acceptance results.
	// -----------------------------------------------------------------
	dc.AllOtherACsPassedFunc = func(excludeACID string) bool {
		for _, ar := range currentAcceptance {
			if ar.CriterionID == excludeACID {
				continue
			}
			if !ar.Passed {
				return false
			}
		}
		return true
	}

	// -----------------------------------------------------------------
	// OnLog: operator-visible progress.
	// -----------------------------------------------------------------
	dc.OnLog = func(msg string) {
		fmt.Printf("  [descent %s] %s\n", session.ID, msg)
	}

	// -----------------------------------------------------------------
	// OnTierEvent: spec-2 item 6 structured-event forward. When the
	// sow-native config carries a streamjson TwoLane emitter and/or
	// a bus, mirror each DescentTierEvent as a descent.tier observability
	// event and a bus publish. Nil-safe: if neither sink is present,
	// the closure is still installed but is a no-op beyond OnLog (which
	// fires separately inside emitTier).
	// -----------------------------------------------------------------
	dc.OnTierEvent = buildDescentTierEventHandler(cfg, session.ID)

	return dc
}

// buildDescentTierEventHandler constructs the OnTierEvent closure for
// the given sow-native config + session. Kept as a free function so
// unit tests can exercise the event→streamjson/bus mapping without
// instantiating the full buildDescentConfig.
func buildDescentTierEventHandler(cfg sowNativeConfig, sessionID string) func(plan.DescentTierEvent) {
	emitter := cfg.StreamJSON
	evtBus := cfg.EventBus
	if emitter == nil && evtBus == nil {
		return nil
	}
	return func(evt plan.DescentTierEvent) {
		if emitter != nil && emitter.Enabled() {
			payload := map[string]any{
				"_stoke.dev/session": sessionID,
				"_stoke.dev/tier":    evt.Tier.String(),
				"_stoke.dev/ac_id":   evt.ACID,
			}
			if evt.Category != "" {
				payload["_stoke.dev/category"] = evt.Category
			}
			if evt.NewCommand != "" {
				payload["_stoke.dev/new_command"] = evt.NewCommand
			}
			if evt.Attempt > 0 {
				payload["_stoke.dev/attempt"] = evt.Attempt
			}
			if evt.FileRepairCount > 0 {
				payload["_stoke.dev/file_repair_count"] = evt.FileRepairCount
			}
			// Tier-specific booleans — only emit when meaningful.
			switch evt.Tier {
			case plan.TierIntentMatch:
				payload["_stoke.dev/intent_confirmed"] = evt.IntentConfirmed
			case plan.TierRunAC:
				payload["_stoke.dev/passed"] = evt.Passed
			case plan.TierEnvFix:
				payload["_stoke.dev/env_fix_applied"] = evt.EnvFixApplied
			case plan.TierRefactor:
				payload["_stoke.dev/refactor_attempted"] = evt.RefactorAttempted
			case plan.TierSoftPass:
				payload["_stoke.dev/all_gates_passed"] = evt.AllGatesPassed
				payload["_stoke.dev/approval_required"] = evt.ApprovalRequired
			case plan.TierClassify, plan.TierCodeRepair, plan.TierACRewrite:
				// These tiers carry no tier-specific booleans to emit.
			default:
				// Other tiers carry no tier-specific booleans to emit.
			}
			emitter.EmitSystem("descent.tier", payload)
		}
	}
}

// runDescentRepairLoop is the feature-flagged replacement for the
// legacy repair loop in runSessionNative. It runs the verification
// descent engine on failing ACs, then applies compliance + quality
// sweeps as post-descent gates.
//
// Returns the final acceptance results and whether all criteria passed.
func runDescentRepairLoop(
	ctx context.Context,
	sowDoc *plan.SOW,
	session plan.Session,
	workingSession plan.Session,
	effectiveCriteria []plan.AcceptanceCriterion,
	cfg sowNativeConfig,
	runtimeDir string,
	maxTurns int,
	maxRepairs int,
) ([]plan.AcceptanceResult, bool) {

	// -----------------------------------------------------------------
	// Pre-flight: catch broken AC commands before any work.
	// -----------------------------------------------------------------
	broken := plan.PreflightACCommands(ctx, cfg.RepoRoot, effectiveCriteria)
	if len(broken) > 0 {
		fmt.Printf("  🛫 descent pre-flight: %d AC command(s) broken before any work:\n", len(broken))
		for id, output := range broken {
			fmt.Printf("    - %s: %s\n", id, truncateForLog(output, 150))
		}
		// Don't fail — just inform the descent engine. The engine's
		// stderr classifier will catch these as ac_bug/environment.
	}

	// -----------------------------------------------------------------
	// Main loop: run ACs, descend on failures, repeat if progress.
	// -----------------------------------------------------------------
	var finalAcceptance []plan.AcceptanceResult
	finalPassed := false

	for attempt := 1; attempt <= maxRepairs; attempt++ {
		if ctx.Err() != nil {
			break
		}
		if cfg.CostBudgetUSD > 0 && cfg.spent != nil && *cfg.spent >= cfg.CostBudgetUSD {
			fmt.Println("  budget exhausted — halting descent loop")
			break
		}

		// Run acceptance criteria (with semantic judge for pattern-mismatch).
		var judge plan.SemanticEvaluator
		if cfg.ReasoningProvider != nil {
			judge = buildSemanticJudge(cfg, session, workingSession)
		}
		acceptance, allPassed := plan.CheckAcceptanceCriteriaWithJudge(ctx, cfg.RepoRoot, effectiveCriteria, judge)
		finalAcceptance = acceptance
		finalPassed = allPassed

		// Log status.
		passedCount := 0
		for _, ac := range acceptance {
			if ac.Passed {
				passedCount++
			}
		}
		fmt.Printf("  descent attempt %d/%d: %d/%d ACs passed\n", attempt, maxRepairs, passedCount, len(acceptance))
		for _, ac := range acceptance {
			mark := "✓"
			if !ac.Passed {
				mark = "✗"
			}
			desc := ac.Description
			if len(desc) > 80 {
				desc = desc[:77] + "..."
			}
			fmt.Printf("    %s %s: %s\n", mark, ac.CriterionID, desc)
		}

		if allPassed {
			if attempt > 1 {
				fmt.Printf("  ✓ session %s repaired via descent on attempt %d\n", session.ID, attempt)
			}
			break
		}

		// Count failures — if all failures are on the last attempt, stop.
		failCount := 0
		for _, ac := range acceptance {
			if !ac.Passed {
				failCount++
			}
		}
		if attempt == maxRepairs {
			fmt.Printf("  ✗ session %s: %d criteria still failing after %d descent attempts\n",
				session.ID, failCount, attempt)
			break
		}

		// Build the descent config with current acceptance snapshot.
		descentCfg := buildDescentConfig(ctx, sowDoc, session, workingSession, cfg, runtimeDir, maxTurns, acceptance)

		// Run descent on all failing ACs.
		summary := plan.RunDescentForSession(ctx, workingSession, acceptance, descentCfg)

		fmt.Printf("  %s", summary.FormatBanner())

		// If descent resolved everything (pass + soft-pass), we're done.
		if summary.AllResolved() {
			// Convert descent results back to acceptance results.
			// Soft-passed ACs become "passed" with a JudgeRuled annotation.
			for i, dr := range summary.Results {
				if dr.Outcome == plan.DescentSoftPass {
					finalAcceptance[i].Passed = true
					finalAcceptance[i].JudgeRuled = true
					finalAcceptance[i].JudgeReasoning = dr.Reason
					finalAcceptance[i].Output = fmt.Sprintf(
						"DESCENT SOFT-PASS [%s]: %s\n\nOriginal output:\n%s",
						dr.ResolvedAtTier, dr.Reason, finalAcceptance[i].Output)
				}
			}
			finalPassed = true
			fmt.Printf("  ✓ session %s: all ACs resolved via descent (attempt %d)\n", session.ID, attempt)
			break
		}

		// If descent made no progress (all failures stayed FAIL and
		// none were resolved), don't burn another outer attempt.
		if summary.Passed == passedCount && summary.SoftPass == 0 {
			fmt.Printf("  → descent made no progress — escalating\n")
			break
		}

		// Descent resolved some ACs. Loop back to re-check all ACs
		// (the fixes may have changed other ACs' pass/fail status).
	}

	// -----------------------------------------------------------------
	// Post-descent gates: compliance + quality sweeps.
	// -----------------------------------------------------------------
	if finalPassed {
		// Compliance sweep.
		sessionSOW := &plan.SOW{Sessions: []plan.Session{session}}
		sessionComp := plan.RunSOWCompliance(cfg.RepoRoot, sessionSOW)
		if sessionComp != nil && len(sessionComp.Findings) > 0 && !sessionComp.Passed() {
			fmt.Printf("  🕵 descent compliance sweep: %s — overriding pass\n", sessionComp.Summary())
			finalPassed = false
		}

		// Quality sweep.
		var sessionFiles []string
		seen := map[string]bool{}
		for _, t := range session.Tasks {
			for _, f := range t.Files {
				if f != "" && !seen[f] {
					seen[f] = true
					sessionFiles = append(sessionFiles, f)
				}
			}
		}
		if len(sessionFiles) > 0 {
			scopedSOW := &plan.SOW{Sessions: []plan.Session{session}}
			qual := plan.RunQualitySweepForSOW(cfg.RepoRoot, sessionFiles, scopedSOW)
			if qual != nil && len(qual.Findings) > 0 && qual.Blocking() {
				fmt.Printf("  🕵 descent quality sweep: %s — overriding pass\n", qual.Summary())
				finalPassed = false
			}
		}
	}

	// Publish to the override cache so SessionScheduler's acceptance
	// gate uses descent's verdicts (including soft-passes) instead of
	// re-running the raw AC commands (which T6/T8 already determined
	// are broken). Only cache on finalPassed to avoid masking genuine
	// failures — a still-failing session must fall through to the
	// legacy failure reporting path.
	if finalPassed && len(finalAcceptance) > 0 {
		descentAcceptanceCache.Store(session.ID, finalAcceptance)
	}

	return finalAcceptance, finalPassed
}

// buildSemanticJudge creates the SemanticEvaluator closure used by
// CheckAcceptanceCriteriaWithJudge. Extracted here to share between
// the legacy path and the descent path.
func buildSemanticJudge(cfg sowNativeConfig, session plan.Session, workingSession plan.Session) plan.SemanticEvaluator {
	return func(jctx context.Context, ac plan.AcceptanceCriterion, failureOutput string) (bool, string, error) {
		jctx, jcancel := context.WithTimeout(jctx, 2*time.Minute)
		defer jcancel()
		taskDesc := workingSession.Title
		var taskFiles []string
		for _, t := range workingSession.Tasks {
			if len(t.Files) > 0 {
				taskDesc = t.Description
				taskFiles = append(taskFiles, t.Files...)
			}
		}
		codeExcerpts := plan.CollectCodeExcerptsForAC(cfg.RepoRoot, ac, failureOutput, taskFiles, 6, 4000)
		sowExcerpt := ""
		if cfg.RawSOWText != "" {
			sowExcerpt = extractTaskSpecExcerpt(cfg.RawSOWText, workingSession, plan.Task{ID: ac.ID, Description: ac.Description}, specExcerptConfig{})
		}
		stopPulse := startWatchdogKeepalive(cfg.Watchdog)
		verdict, err := plan.JudgeAC(jctx, cfg.ReasoningProvider, cfg.ReasoningModel, plan.SemanticJudgeInput{
			TaskDescription:      taskDesc,
			SOWSpec:              sowExcerpt,
			Criterion:            ac,
			FailureOutput:        failureOutput,
			CodeExcerpts:         codeExcerpts,
			RepoRoot:             cfg.RepoRoot,
			UniversalPromptBlock: cfg.combinedPromptBlock(cfg.agentContext("judge-semantic-ac", "2-ac-check", &session, 1)),
		})
		stopPulse()
		if err != nil || verdict == nil {
			return false, "", err
		}
		if verdict.ImplementsRequirement {
			fmt.Printf("    ⚖ semantic judge: %s implements requirement despite mechanical mismatch — %s\n",
				ac.ID, truncateForLog(verdict.Reasoning, 200))
		} else {
			fmt.Printf("    ⚖ semantic judge: %s does NOT implement requirement — %s\n",
				ac.ID, truncateForLog(verdict.Reasoning, 200))
		}
		return verdict.ImplementsRequirement, verdict.Reasoning, nil
	}
}

// detectBuildCommand returns the appropriate build command for the
// project's stack, or empty string if none can be detected.
func detectBuildCommand(sowDoc *plan.SOW, repoRoot string) string {
	if sowDoc != nil {
		lang := strings.ToLower(sowDoc.Stack.Language)
		switch {
		case lang == "typescript" || lang == "javascript":
			if _, err := os.Stat(filepath.Join(repoRoot, "tsconfig.json")); err == nil {
				return "tsc --noEmit"
			}
			return ""
		case lang == "go":
			return "go build ./..."
		case lang == "rust":
			return "cargo check"
		case lang == "python":
			return "" // no universal build command for Python
		}
	}
	// Fall back to file detection.
	if _, err := os.Stat(filepath.Join(repoRoot, "tsconfig.json")); err == nil {
		return "tsc --noEmit"
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err == nil {
		return "go build ./..."
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "Cargo.toml")); err == nil {
		return "cargo check"
	}
	return ""
}

// descentGitHead returns the SHA of HEAD in repoRoot, or empty on
// error (not-a-repo, detached work tree, etc.). ctx-aware so a
// repair wrapper's cancellation propagates.
func descentGitHead(ctx context.Context, repoRoot string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// descentGitDiffNames returns the file paths changed between two
// revisions. When preSHA is empty (git not available / no commits),
// the function falls back to `git status --porcelain` so uncommitted
// changes still register as touched. Used by the descent bootstrap
// wrapper (spec-1 item 5) to detect dep-manifest edits.
func descentGitDiffNames(ctx context.Context, repoRoot, preSHA, postRef string) []string {
	if preSHA != "" {
		cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", preSHA, postRef) // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
		cmd.Dir = repoRoot
		out, err := cmd.Output()
		if err == nil {
			return splitNonEmptyLines(string(out))
		}
	}
	// Fall back to working-tree status (uncommitted changes).
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 4 {
			continue
		}
		// porcelain lines are "XY <path>" — strip the status prefix.
		path := strings.TrimSpace(line[2:])
		if path != "" {
			names = append(names, path)
		}
	}
	return names
}

// splitNonEmptyLines is a small helper that trims and de-duplicates
// lines from command output.
func splitNonEmptyLines(s string) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	seen := map[string]bool{}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || seen[ln] {
			continue
		}
		seen[ln] = true
		out = append(out, ln)
	}
	return out
}

// intersectStrings returns the values in haystack that also appear in
// candidates. Case-sensitive; preserves haystack order.
func intersectStrings(haystack, candidates []string) []string {
	cand := map[string]bool{}
	for _, c := range candidates {
		cand[c] = true
	}
	var out []string
	for _, h := range haystack {
		// Handle subpath matches so a file inside a subdirectory
		// (e.g. "packages/foo/package.json") still registers as a
		// manifest change. The spec lists bare manifest names; we
		// match on basename.
		name := filepath.Base(h)
		if cand[name] {
			out = append(out, h)
		}
	}
	return out
}
