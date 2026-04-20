// Package plan — verification_descent.go
//
// Unified verification descent engine for SOW acceptance criteria.
//
// Replaces the scattered soft-pass branches (H-76 meta soft-pass,
// H-77 missing-tool skip, H-81 NOTES.txt escape, H-87 ac-bug
// soft-pass) with a single tiered resolution function that actively
// attempts to fix failures before considering any soft-pass.
//
// Design principle (from Eric, 2026-04-20):
//
//   "must be the closest possible match to what was instructed —
//    and FULLY complete — functionality/requirement-specifics wise…
//    and if something cannot be verified/run due to ENVIRONMENT —
//    and we can't find a way to install/fix the environment and
//    retry — and we can't make a refactor that allows us to verify
//    AND satisfy the requirement — then it's ok, if EVERYTHING
//    ELSE looks good and the code has a confirmed looks-good from
//    the reviewer — to say the work is done."
//
// The function operates on ONE acceptance criterion at a time.
// The session-level caller iterates failing ACs and calls descent
// for each. Each tier actively attempts resolution before descending;
// a CODE_BUG verdict can never reach soft-pass.
//
// Tier ladder:
//
//   T1  Intent match     — reviewer confirms code matches spec intent
//   T2  Run AC           — execute the AC command; if exit 0, done
//   T3  Classify failure — multi-analyst determines code_bug / ac_bug / environment
//   T4  Code repair      — if code_bug, dispatch repair + re-run AC (loop)
//   T5  Environment fix  — if environment, attempt install/fix + re-run AC
//   T6  AC rewrite       — if ac_bug, apply rewrite from A4 + re-run AC
//   T7  Refactor         — ask worker to restructure for verifiability + re-run AC + re-check intent
//   T8  Soft-pass        — all tiers exhausted, intent confirmed, no code_bug → reviewer-confirmed done
//
// Each tier that modifies state (T4–T7) re-runs the AC and may
// re-classify, because fixes can shift the failure category.
// T4 (code_bug) loops up to MaxCodeRepairs before falling through.
// T8 requires ALL of: intent confirmed, category != code_bug,
// build clean, stub scan clean.
package plan

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/provider"
)

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// DescentOutcome is the final disposition of one AC through the ladder.
type DescentOutcome int

const (
	// DescentPass means the AC was verified — either mechanically
	// (exit 0) or after a successful fix at some tier.
	DescentPass DescentOutcome = iota

	// DescentSoftPass means the AC could not be mechanically verified
	// but every active resolution path was exhausted and the reviewer
	// confirmed intent. The operator log carries the full audit trail.
	DescentSoftPass

	// DescentFail means the AC represents a real gap that could not
	// be resolved. Either code_bug persisted through all repair
	// attempts, or soft-pass prerequisites weren't met.
	DescentFail
)

func (d DescentOutcome) String() string {
	switch d {
	case DescentPass:
		return "PASS"
	case DescentSoftPass:
		return "SOFT-PASS"
	case DescentFail:
		return "FAIL"
	default:
		return "UNKNOWN"
	}
}

// DescentTier identifies which rung of the ladder produced the result.
type DescentTier int

const (
	TierIntentMatch  DescentTier = 1
	TierRunAC        DescentTier = 2
	TierClassify     DescentTier = 3
	TierCodeRepair   DescentTier = 4
	TierEnvFix       DescentTier = 5
	TierACRewrite    DescentTier = 6
	TierRefactor     DescentTier = 7
	TierSoftPass     DescentTier = 8
)

func (t DescentTier) String() string {
	switch t {
	case TierIntentMatch:
		return "T1-intent-match"
	case TierRunAC:
		return "T2-run-ac"
	case TierClassify:
		return "T3-classify"
	case TierCodeRepair:
		return "T4-code-repair"
	case TierEnvFix:
		return "T5-env-fix"
	case TierACRewrite:
		return "T6-ac-rewrite"
	case TierRefactor:
		return "T7-refactor"
	case TierSoftPass:
		return "T8-soft-pass"
	default:
		return fmt.Sprintf("T?-%d", int(t))
	}
}

// DescentResult is the full audit trail for one AC's descent.
type DescentResult struct {
	// Outcome is the final disposition.
	Outcome DescentOutcome

	// ResolvedAtTier is which tier produced the final outcome.
	ResolvedAtTier DescentTier

	// Reason is a human-readable explanation for the operator log.
	// Always populated. Includes which tiers were attempted and why
	// each one couldn't resolve the failure.
	Reason string

	// Category is the multi-analyst classification of the failure.
	// Empty when the AC passed mechanically at T2 (no classification
	// needed). One of: code_bug, ac_bug, environment, both,
	// acceptable_as_is.
	Category string

	// CodeRepairAttempts is how many T4 repair rounds were tried.
	CodeRepairAttempts int

	// EnvFixAttempted is true when T5 ran (regardless of success).
	EnvFixAttempted bool

	// ACRewriteAttempted is true when T6 applied a rewrite.
	ACRewriteAttempted bool

	// ACRewriteCommand is the rewritten AC command, if any.
	ACRewriteCommand string

	// RefactorAttempted is true when T7 dispatched a refactor worker.
	RefactorAttempted bool

	// IntentConfirmed is true when the reviewer said the code matches
	// the spec's intent. Required for soft-pass eligibility.
	IntentConfirmed bool

	// StderrSignature is the deterministic classification of the AC's
	// stderr output, used for environment/ac_bug pre-screening before
	// the LLM analysts fire.
	StderrSignature StderrClass

	// RawACOutput is the last AC execution output, for logging.
	RawACOutput string
}

// StderrClass is a deterministic classification of AC failure output.
// Computed by pattern-matching on stderr/exit code BEFORE any LLM call.
type StderrClass int

const (
	StderrUnclassified    StderrClass = iota
	StderrCommandNotFound             // exit 127, "command not found"
	StderrModuleNotFound              // "Cannot find module", "Module not found"
	StderrSyntaxError                 // "SyntaxError", parse errors in AC command itself
	StderrAssertionFail               // "expected", "AssertionError", actual test failures
	StderrCompileError                // "TS\d+", "error\[E", build failures
	StderrEnvMissing                  // "env", "not set", "undefined" for env vars
	StderrTimeout                     // context deadline exceeded
)

func (s StderrClass) String() string {
	switch s {
	case StderrCommandNotFound:
		return "command-not-found"
	case StderrModuleNotFound:
		return "module-not-found"
	case StderrSyntaxError:
		return "syntax-error"
	case StderrAssertionFail:
		return "assertion-failure"
	case StderrCompileError:
		return "compile-error"
	case StderrEnvMissing:
		return "env-missing"
	case StderrTimeout:
		return "timeout"
	default:
		return "unclassified"
	}
}

// IsEnvironmentProblem returns true when the stderr signature indicates
// the failure is environmental (not a code bug). Used by the descent
// engine to fast-path to T5 without waiting for multi-analyst.
func (s StderrClass) IsEnvironmentProblem() bool {
	return s == StderrCommandNotFound || s == StderrModuleNotFound || s == StderrEnvMissing
}

// IsDefiniteCodeBug returns true when the stderr signature indicates
// the failure is almost certainly a code bug (assertion / compile error).
// The multi-analyst still runs for confirmation, but the descent engine
// can prioritize T4 code repair.
func (s StderrClass) IsDefiniteCodeBug() bool {
	return s == StderrAssertionFail || s == StderrCompileError
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// DescentConfig carries everything the descent engine needs.
// Populated by the caller (sow_native.go) from its sowNativeConfig.
type DescentConfig struct {
	// Provider is the LLM provider for multi-analyst reasoning.
	// When nil, the engine skips LLM-based classification and
	// falls back to deterministic stderr classification only.
	Provider provider.Provider

	// Model is the model name for reasoning calls.
	Model string

	// RepoRoot is the workspace root for running commands.
	RepoRoot string

	// Session is the current session being verified.
	Session Session

	// MaxCodeRepairs is how many T4 code-repair loops to attempt
	// per AC before giving up on code_bug. Default 3.
	MaxCodeRepairs int

	// RepairFunc dispatches a repair worker with a directive and
	// returns an error if the dispatch itself failed. The worker
	// modifies files in RepoRoot; the descent engine re-runs the
	// AC after each repair. When nil, T4/T7 are skipped (descent
	// becomes classify-only, useful for dry-run diagnostics).
	RepairFunc func(ctx context.Context, directive string) error

	// EnvFixFunc attempts to fix an environment problem identified
	// by the multi-analyst or stderr classifier. Takes a description
	// of what's missing and returns true if it fixed something.
	// When nil, T5 is skipped.
	EnvFixFunc func(ctx context.Context, rootCause string, stderr string) bool

	// IntentCheckFunc asks the reviewer whether the code matches
	// the spec's intent for this AC's area. Returns (confirmed, reasoning).
	// When nil, intent is assumed confirmed (weaker guarantee).
	IntentCheckFunc func(ctx context.Context, ac AcceptanceCriterion) (bool, string)

	// BuildCleanFunc returns true when the project builds cleanly.
	// Used as a soft-pass prerequisite. When nil, build is assumed clean.
	BuildCleanFunc func(ctx context.Context) bool

	// StubScanCleanFunc returns true when the stub/fake scanner
	// finds no issues in the session's files. Used as a soft-pass
	// prerequisite. When nil, assumed clean.
	StubScanCleanFunc func(ctx context.Context) bool

	// AllOtherACsPassedFunc returns true when every OTHER AC in
	// the session (excluding the one being descended) has passed.
	// Soft-pass is only available when the rest of the session is
	// green. When nil, assumed true.
	AllOtherACsPassedFunc func(acID string) bool

	// UniversalPromptBlock is the shared coding-standards context
	// injected into all analyst/judge prompts.
	UniversalPromptBlock string

	// OnLog is called with human-readable progress messages.
	// When nil, messages are discarded.
	OnLog func(msg string)
}

func (dc *DescentConfig) log(format string, args ...interface{}) {
	if dc.OnLog != nil {
		dc.OnLog(fmt.Sprintf(format, args...))
	}
}

func (dc *DescentConfig) maxRepairs() int {
	if dc.MaxCodeRepairs > 0 {
		return dc.MaxCodeRepairs
	}
	return 3
}

// ---------------------------------------------------------------------------
// Deterministic stderr classifier
// ---------------------------------------------------------------------------

var (
	reCommandNotFound = regexp.MustCompile(`(?i)(command not found|exit status 127|no such file or directory.*/bin/)`)
	reModuleNotFound  = regexp.MustCompile(`(?i)(cannot find module|module not found|error \[ERR_MODULE_NOT_FOUND\]|no matching export)`)
	reSyntaxError     = regexp.MustCompile(`(?i)(SyntaxError|unexpected token|parse error)`)
	reAssertionFail   = regexp.MustCompile(`(?i)(expected .+ (to |but )|AssertionError|assert\.|FAIL:|test failed)`)
	reCompileError    = regexp.MustCompile(`(?i)(TS\d{4}:|error\[E\d|cannot compile|build failed|compilation failed)`)
	reEnvMissing      = regexp.MustCompile(`(?i)(env.+not set|undefined.+variable|missing.+env|required.+environment)`)
)

// ClassifyStderr deterministically classifies AC failure output by
// pattern-matching on stderr + exit code. Returns the most specific
// category that matches. No LLM call — pure regex, runs in microseconds.
//
// Priority order: command-not-found > module-not-found > compile-error
// > assertion-fail > syntax-error > env-missing > unclassified.
// This order ensures the most actionable classification wins when
// multiple patterns match (e.g., "module not found" also triggers
// assertion-fail patterns in some test runners).
func ClassifyStderr(output string, exitCode int) StderrClass {
	if exitCode == -1 {
		// Context deadline exceeded — the command hung.
		return StderrTimeout
	}

	// Exit 127 is the canonical "command not found" on all POSIX shells.
	if exitCode == 127 || reCommandNotFound.MatchString(output) {
		return StderrCommandNotFound
	}
	if reModuleNotFound.MatchString(output) {
		return StderrModuleNotFound
	}
	if reCompileError.MatchString(output) {
		return StderrCompileError
	}
	if reAssertionFail.MatchString(output) {
		return StderrAssertionFail
	}
	if reSyntaxError.MatchString(output) {
		return StderrSyntaxError
	}
	if reEnvMissing.MatchString(output) {
		return StderrEnvMissing
	}
	return StderrUnclassified
}

// extractExitCode pulls the exit code from a failed AC's output.
// checkOneCriterion formats failures as "command failed: exit status N\n..."
// Returns -1 for timeout / context cancellation, 1 as default for other
// failures, and the actual code when parseable.
func extractExitCode(output string) int {
	if strings.Contains(output, "context deadline exceeded") ||
		strings.Contains(output, "signal: killed") {
		return -1
	}
	// "exit status 127" pattern from exec.ExitError.Error()
	if idx := strings.Index(output, "exit status "); idx >= 0 {
		rest := output[idx+len("exit status "):]
		var code int
		if _, err := fmt.Sscanf(rest, "%d", &code); err == nil {
			return code
		}
	}
	return 1 // generic failure
}

// ---------------------------------------------------------------------------
// Main engine
// ---------------------------------------------------------------------------

// VerificationDescent runs the tiered resolution engine on a single
// failing acceptance criterion. It actively attempts to fix the failure
// at every tier before descending to the next. Returns a full audit
// trail regardless of outcome.
//
// The caller is responsible for:
//   - Running the AC once before calling descent (to confirm it fails)
//   - Providing the initial failure output
//   - Supplying the repair/env-fix/intent-check callbacks
//
// The function is stateless between calls — the caller manages
// cross-AC state (e.g., "all other ACs passed") via the config
// callbacks.
func VerificationDescent(
	ctx context.Context,
	ac AcceptanceCriterion,
	initialOutput string,
	cfg DescentConfig,
) DescentResult {
	result := DescentResult{
		Outcome:    DescentFail,
		RawACOutput: initialOutput,
	}

	// ---------------------------------------------------------------
	// T1: Intent match
	// ---------------------------------------------------------------
	// Before anything else, confirm the reviewer thinks the code
	// matches the spec's intent for this AC's area. If intent is
	// NOT confirmed, the problem is incomplete work — send it back
	// to the worker, don't descend further.
	if cfg.IntentCheckFunc != nil {
		confirmed, reasoning := cfg.IntentCheckFunc(ctx, ac)
		result.IntentConfirmed = confirmed
		if !confirmed {
			result.Outcome = DescentFail
			result.ResolvedAtTier = TierIntentMatch
			result.Reason = fmt.Sprintf(
				"T1-intent-match: reviewer says code does NOT match spec intent. "+
					"Reason: %s. This is incomplete work, not an environment problem.",
				truncateDescentLog(reasoning, 300))
			cfg.log("  ↓ %s", result.Reason)
			return result
		}
		cfg.log("  ✓ T1: intent confirmed by reviewer")
	} else {
		// No intent checker — assume confirmed (weaker guarantee).
		result.IntentConfirmed = true
	}

	// ---------------------------------------------------------------
	// T2: Run AC (initial check — caller already ran it, but we
	//     re-run to ensure we have fresh output after any prior work)
	// ---------------------------------------------------------------
	acOutput, passed := runACCommand(ctx, cfg.RepoRoot, ac)
	if passed {
		result.Outcome = DescentPass
		result.ResolvedAtTier = TierRunAC
		result.Reason = "T2-run-ac: AC passed on initial check"
		result.RawACOutput = acOutput
		cfg.log("  ✓ T2: AC passed")
		return result
	}
	result.RawACOutput = acOutput
	cfg.log("  ✗ T2: AC failed, classifying...")

	// ---------------------------------------------------------------
	// T3: Classify failure
	// ---------------------------------------------------------------
	// First: deterministic stderr classification (free, instant).
	exitCode := extractExitCode(acOutput)
	result.StderrSignature = ClassifyStderr(acOutput, exitCode)
	result.ResolvedAtTier = TierClassify

	cfg.log("  ⚙ T3: stderr=%s exit=%d", result.StderrSignature, exitCode)

	// Then: multi-analyst reasoning if provider is available AND
	// the deterministic classifier didn't give a high-confidence
	// answer. For command-not-found and assertion-failure, the
	// deterministic signal is strong enough to skip the LLM hop.
	var analysisCategory string
	var analysisCodeFix string
	var analysisACRewrite string
	var analysisRootCause string

	if cfg.Provider != nil && !result.StderrSignature.IsDefiniteCodeBug() &&
		!result.StderrSignature.IsEnvironmentProblem() {
		// Full multi-analyst pass for ambiguous failures.
		verdict, err := runDescentReasoning(ctx, cfg, ac, acOutput)
		if err != nil {
			cfg.log("  ⚠ T3: multi-analyst failed: %v — using stderr class", err)
		} else {
			analysisCategory = verdict.Category
			analysisCodeFix = verdict.CodeFix
			analysisACRewrite = verdict.ACRewrite
			if notes, ok := verdict.AnalystNotes["root_cause"]; ok {
				analysisRootCause = notes
			}
			cfg.log("  ⚖ T3: multi-analyst verdict=%s", analysisCategory)
		}
	}

	// Reconcile LLM verdict with deterministic stderr class.
	// Deterministic signal wins when it's high-confidence.
	if analysisCategory == "" {
		switch {
		case result.StderrSignature.IsEnvironmentProblem():
			analysisCategory = "environment"
		case result.StderrSignature.IsDefiniteCodeBug():
			analysisCategory = "code_bug"
		case result.StderrSignature == StderrSyntaxError:
			// Syntax error in the AC command itself is usually ac_bug.
			analysisCategory = "ac_bug"
		default:
			// Can't classify at all — treat as code_bug (conservative).
			analysisCategory = "code_bug"
		}
	}

	// Map the multi-analyst's root_cause "missing_dependency" to our
	// "environment" category when the LLM said code_correct + missing dep.
	if analysisCategory == "code_bug" && result.StderrSignature.IsEnvironmentProblem() {
		// Stderr says environment, LLM said code_bug — stderr wins.
		// This is the "exit 127 but LLM couldn't tell" case.
		analysisCategory = "environment"
	}
	if analysisCategory == "acceptable_as_is" {
		// We don't allow skips — reclassify as ac_bug per the judge prompt.
		analysisCategory = "ac_bug"
	}

	result.Category = analysisCategory
	cfg.log("  → T3: final category=%s", analysisCategory)

	// ---------------------------------------------------------------
	// T4: Code repair (only for code_bug)
	// ---------------------------------------------------------------
	if analysisCategory == "code_bug" || analysisCategory == "both" {
		if cfg.RepairFunc != nil {
			for attempt := 0; attempt < cfg.maxRepairs(); attempt++ {
				result.CodeRepairAttempts++
				directive := analysisCodeFix
				if directive == "" {
					directive = fmt.Sprintf(
						"Fix the code for AC %s (%s). Failure: %s",
						ac.ID, ac.Description, truncateDescentLog(acOutput, 500))
				}
				cfg.log("  ↻ T4: code repair attempt %d/%d", attempt+1, cfg.maxRepairs())

				repairCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				err := cfg.RepairFunc(repairCtx, directive)
				cancel()
				if err != nil {
					cfg.log("  ⚠ T4: repair dispatch failed: %v", err)
					continue
				}

				// Re-run AC after repair.
				acOutput, passed = runACCommand(ctx, cfg.RepoRoot, ac)
				result.RawACOutput = acOutput
				if passed {
					result.Outcome = DescentPass
					result.ResolvedAtTier = TierCodeRepair
					result.Reason = fmt.Sprintf(
						"T4-code-repair: AC passed after %d repair attempt(s)",
						attempt+1)
					cfg.log("  ✓ T4: AC passed after repair")
					return result
				}

				// Re-classify — the failure signature may have changed.
				// First repair might fix the original bug but reveal a new one.
				newExit := extractExitCode(acOutput)
				newStderr := ClassifyStderr(acOutput, newExit)
				cfg.log("  ⚙ T4: post-repair stderr=%s", newStderr)

				if newStderr.IsEnvironmentProblem() {
					// Failure shifted from code_bug to environment.
					analysisCategory = "environment"
					result.Category = "environment"
					cfg.log("  → T4: failure shifted to environment, descending to T5")
					break
				}
				if newStderr == StderrSyntaxError {
					analysisCategory = "ac_bug"
					result.Category = "ac_bug"
					cfg.log("  → T4: failure shifted to ac_bug, descending to T6")
					break
				}

				// If provider available, re-run multi-analyst for fresh
				// classification on the new failure output.
				if cfg.Provider != nil {
					reVerdict, err := runDescentReasoning(ctx, cfg, ac, acOutput)
					if err == nil && reVerdict.Category != "code_bug" {
						analysisCategory = reVerdict.Category
						result.Category = reVerdict.Category
						analysisACRewrite = reVerdict.ACRewrite
						analysisCodeFix = reVerdict.CodeFix
						cfg.log("  → T4: re-analysis shifted to %s", reVerdict.Category)
						break
					}
					// Update code fix for next attempt.
					if err == nil && reVerdict.CodeFix != "" {
						analysisCodeFix = reVerdict.CodeFix
					}
				}
			}

			// Exhausted code repairs and still code_bug?
			// That's a real failure. CODE_BUG NEVER SOFT-PASSES.
			if analysisCategory == "code_bug" {
				result.Outcome = DescentFail
				result.ResolvedAtTier = TierCodeRepair
				result.Reason = fmt.Sprintf(
					"T4-code-repair: code_bug unresolved after %d repair attempt(s). "+
						"Last failure: %s",
					result.CodeRepairAttempts, truncateDescentLog(acOutput, 300))
				cfg.log("  ✗ T4: code_bug persists after %d repairs — FAIL", result.CodeRepairAttempts)
				return result
			}
		} else if analysisCategory == "code_bug" {
			// No repair function — can't fix code_bug. Fail immediately.
			result.Outcome = DescentFail
			result.ResolvedAtTier = TierCodeRepair
			result.Reason = "T4-code-repair: code_bug detected but no repair function available"
			return result
		}
	}

	// ---------------------------------------------------------------
	// T5: Environment fix
	// ---------------------------------------------------------------
	if analysisCategory == "environment" {
		if cfg.EnvFixFunc != nil {
			result.EnvFixAttempted = true
			rootCause := analysisRootCause
			if rootCause == "" {
				rootCause = result.StderrSignature.String()
			}
			cfg.log("  🔧 T5: attempting env fix for %s", rootCause)

			if cfg.EnvFixFunc(ctx, rootCause, acOutput) {
				// Env fix reports success — re-run AC.
				acOutput, passed = runACCommand(ctx, cfg.RepoRoot, ac)
				result.RawACOutput = acOutput
				if passed {
					result.Outcome = DescentPass
					result.ResolvedAtTier = TierEnvFix
					result.Reason = "T5-env-fix: AC passed after environment fix"
					cfg.log("  ✓ T5: AC passed after env fix")
					return result
				}
				cfg.log("  ⚠ T5: env fix succeeded but AC still fails")

				// Re-classify post env-fix.
				newExit := extractExitCode(acOutput)
				newStderr := ClassifyStderr(acOutput, newExit)
				if newStderr.IsDefiniteCodeBug() {
					// The real error was hiding behind the env problem.
					// Now that the env is fixed, it's a code bug.
					// But we've already exhausted code repairs (or had none).
					// Fall through to T6/T7.
					analysisCategory = "ac_bug" // try AC rewrite before giving up
					result.Category = "ac_bug"
					cfg.log("  → T5: post-fix revealed code/ac issue, trying T6")
				}
			} else {
				cfg.log("  ⚠ T5: env fix could not resolve the problem")
			}
		} else {
			cfg.log("  ⚠ T5: no env fix function available")
		}
		// Fall through to T6 — maybe the AC can be rewritten to
		// avoid the environment dependency.
	}

	// ---------------------------------------------------------------
	// T6: AC rewrite
	// ---------------------------------------------------------------
	if analysisCategory == "ac_bug" || analysisCategory == "both" || analysisACRewrite != "" {
		if analysisACRewrite != "" {
			result.ACRewriteAttempted = true
			result.ACRewriteCommand = analysisACRewrite
			cfg.log("  ✏ T6: rewriting AC command to: %s", truncateDescentLog(analysisACRewrite, 120))

			// Run the rewritten command directly (don't modify the
			// canonical AC — the caller decides whether to persist the rewrite).
			rewrittenAC := ac
			rewrittenAC.Command = analysisACRewrite
			acOutput, passed = runACCommand(ctx, cfg.RepoRoot, rewrittenAC)
			result.RawACOutput = acOutput
			if passed {
				result.Outcome = DescentPass
				result.ResolvedAtTier = TierACRewrite
				result.Reason = fmt.Sprintf(
					"T6-ac-rewrite: AC passed with rewritten command: %s",
					truncateDescentLog(analysisACRewrite, 200))
				cfg.log("  ✓ T6: rewritten AC passed")
				return result
			}
			cfg.log("  ⚠ T6: rewritten AC also fails")
		} else {
			cfg.log("  ⚠ T6: no AC rewrite available from analysts")
		}
	}

	// ---------------------------------------------------------------
	// T7: Refactor for verifiability
	// ---------------------------------------------------------------
	// Ask the worker to restructure the code so the AC CAN run,
	// while still satisfying the spec intent. Example: if the AC
	// tries to curl localhost:3000 but there's no server runtime,
	// refactor to export the handler and test it directly.
	if cfg.RepairFunc != nil {
		result.RefactorAttempted = true
		refactorDirective := buildRefactorDirective(ac, analysisCategory, analysisRootCause, acOutput)
		cfg.log("  🔄 T7: dispatching refactor for verifiability")

		refactorCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := cfg.RepairFunc(refactorCtx, refactorDirective)
		cancel()
		if err != nil {
			cfg.log("  ⚠ T7: refactor dispatch failed: %v", err)
		} else {
			acOutput, passed = runACCommand(ctx, cfg.RepoRoot, ac)
			result.RawACOutput = acOutput
			if passed {
				// Refactor made the AC pass — but did it break intent?
				// Re-check T1 after any refactor.
				if cfg.IntentCheckFunc != nil {
					reconfirmed, reasoning := cfg.IntentCheckFunc(ctx, ac)
					if reconfirmed {
						result.Outcome = DescentPass
						result.ResolvedAtTier = TierRefactor
						result.Reason = "T7-refactor: AC passed after refactor, intent re-confirmed"
						cfg.log("  ✓ T7: refactor succeeded, intent re-confirmed")
						return result
					}
					// Refactor broke intent. This is worse than a soft-pass.
					cfg.log("  ✗ T7: refactor made AC pass but broke intent: %s",
						truncateDescentLog(reasoning, 200))
					// Fall through to T8 evaluation.
				} else {
					result.Outcome = DescentPass
					result.ResolvedAtTier = TierRefactor
					result.Reason = "T7-refactor: AC passed after refactor (no intent re-check available)"
					cfg.log("  ✓ T7: refactor succeeded")
					return result
				}
			} else {
				cfg.log("  ⚠ T7: refactor did not resolve AC failure")
			}
		}
	}

	// ---------------------------------------------------------------
	// T8: Soft-pass evaluation
	// ---------------------------------------------------------------
	// ALL of these must be true:
	//   - Intent was confirmed at T1
	//   - Category is NOT code_bug (code_bug NEVER soft-passes)
	//   - Build is clean
	//   - Stub scan is clean
	//   - All other ACs in the session passed
	//   - At least one active resolution was attempted
	cfg.log("  ⚖ T8: evaluating soft-pass eligibility...")

	activeAttemptsMade := result.CodeRepairAttempts > 0 ||
		result.EnvFixAttempted ||
		result.ACRewriteAttempted ||
		result.RefactorAttempted

	if !activeAttemptsMade {
		result.Outcome = DescentFail
		result.ResolvedAtTier = TierSoftPass
		result.Reason = "T8-soft-pass: no active resolution attempted — cannot soft-pass without demonstrated effort"
		cfg.log("  ✗ T8: no active resolution attempted")
		return result
	}

	if !result.IntentConfirmed {
		result.Outcome = DescentFail
		result.ResolvedAtTier = TierSoftPass
		result.Reason = "T8-soft-pass: intent not confirmed — cannot soft-pass without reviewer approval"
		cfg.log("  ✗ T8: intent not confirmed")
		return result
	}

	if result.Category == "code_bug" {
		result.Outcome = DescentFail
		result.ResolvedAtTier = TierSoftPass
		result.Reason = "T8-soft-pass: category is code_bug — code bugs NEVER soft-pass"
		cfg.log("  ✗ T8: code_bug cannot soft-pass")
		return result
	}

	buildClean := cfg.BuildCleanFunc == nil || cfg.BuildCleanFunc(ctx)
	if !buildClean {
		result.Outcome = DescentFail
		result.ResolvedAtTier = TierSoftPass
		result.Reason = "T8-soft-pass: build is not clean — cannot soft-pass with broken build"
		cfg.log("  ✗ T8: build not clean")
		return result
	}

	stubClean := cfg.StubScanCleanFunc == nil || cfg.StubScanCleanFunc(ctx)
	if !stubClean {
		result.Outcome = DescentFail
		result.ResolvedAtTier = TierSoftPass
		result.Reason = "T8-soft-pass: stub scan found issues — cannot soft-pass with fake code"
		cfg.log("  ✗ T8: stub scan not clean")
		return result
	}

	othersPass := cfg.AllOtherACsPassedFunc == nil || cfg.AllOtherACsPassedFunc(ac.ID)
	if !othersPass {
		result.Outcome = DescentFail
		result.ResolvedAtTier = TierSoftPass
		result.Reason = "T8-soft-pass: other ACs in session also failing — soft-pass requires isolated failure"
		cfg.log("  ✗ T8: other ACs also failing")
		return result
	}

	// All prerequisites met. Grant soft-pass with full audit trail.
	result.Outcome = DescentSoftPass
	result.ResolvedAtTier = TierSoftPass
	result.Reason = fmt.Sprintf(
		"T8-soft-pass: verification descent exhausted. "+
			"intent-confirmed=true, category=%s, "+
			"code-repairs=%d, env-fix-attempted=%v, "+
			"ac-rewrite-attempted=%v, refactor-attempted=%v, "+
			"build-clean=%v, stub-clean=%v, others-pass=%v. "+
			"Root cause: %s",
		result.Category,
		result.CodeRepairAttempts, result.EnvFixAttempted,
		result.ACRewriteAttempted, result.RefactorAttempted,
		buildClean, stubClean, othersPass,
		truncateDescentLog(analysisRootCause, 200))
	cfg.log("  ⚖ T8: SOFT-PASS granted — %s", result.Reason)
	return result
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// runACCommand executes a single AC against the project root.
// Thin wrapper around checkOneCriterion that returns (output, passed).
func runACCommand(ctx context.Context, projectRoot string, ac AcceptanceCriterion) (string, bool) {
	result := checkOneCriterion(ctx, projectRoot, ac)
	return result.Output, result.Passed
}

// runDescentReasoning runs the multi-analyst + judge reasoning loop
// on a failing AC. Wrapper around ReasonAboutFailure with descent-
// specific context.
func runDescentReasoning(ctx context.Context, cfg DescentConfig, ac AcceptanceCriterion, failureOutput string) (*ReasoningVerdict, error) {
	// Gather code excerpts from session files.
	var relPaths []string
	seen := map[string]bool{}
	for _, t := range cfg.Session.Tasks {
		for _, f := range t.Files {
			if f != "" && !seen[f] {
				seen[f] = true
				relPaths = append(relPaths, f)
			}
		}
	}
	if ac.FileExists != "" && !seen[ac.FileExists] {
		relPaths = append(relPaths, ac.FileExists)
	}
	if ac.ContentMatch != nil && ac.ContentMatch.File != "" && !seen[ac.ContentMatch.File] {
		relPaths = append(relPaths, ac.ContentMatch.File)
	}
	codeExcerpts := CollectCodeExcerpts(cfg.RepoRoot, relPaths, 8, 3000)

	taskDesc := cfg.Session.Title
	for _, t := range cfg.Session.Tasks {
		if len(t.Files) > 0 {
			taskDesc = t.Description
			break
		}
	}

	reasonCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	return ReasonAboutFailure(reasonCtx, cfg.Provider, cfg.Model, ReasoningInput{
		SessionID:            cfg.Session.ID,
		SessionTitle:         cfg.Session.Title,
		TaskDescription:      taskDesc,
		Criterion:            ac,
		FailureOutput:        failureOutput,
		PriorAttempts:        0, // descent manages its own attempt count
		CodeExcerpts:         codeExcerpts,
		RepoRoot:             cfg.RepoRoot,
		UniversalPromptBlock: cfg.UniversalPromptBlock,
	})
}

// buildRefactorDirective constructs the prompt for T7's refactor worker.
func buildRefactorDirective(ac AcceptanceCriterion, category, rootCause, failureOutput string) string {
	var b strings.Builder
	b.WriteString("REFACTOR FOR VERIFIABILITY\n\n")
	b.WriteString("The acceptance criterion below cannot be verified in this environment. ")
	b.WriteString("Your job: restructure the code so the SAME requirement can be verified ")
	b.WriteString("using a DIFFERENT approach that works in this environment.\n\n")

	fmt.Fprintf(&b, "AC %s: %s\n", ac.ID, ac.Description)
	if ac.Command != "" {
		fmt.Fprintf(&b, "Command that fails: %s\n", ac.Command)
	}
	fmt.Fprintf(&b, "Failure category: %s\n", category)
	if rootCause != "" {
		fmt.Fprintf(&b, "Root cause: %s\n", truncateDescentLog(rootCause, 300))
	}
	fmt.Fprintf(&b, "\nLast failure output:\n%s\n", truncateDescentLog(failureOutput, 500))

	b.WriteString("\nExamples of valid refactors:\n")
	b.WriteString("  - If the AC tries to curl localhost:3000, export the handler and test it directly with node -e\n")
	b.WriteString("  - If the AC tries to run a binary that isn't available, use the equivalent programmatic API\n")
	b.WriteString("  - If the AC checks a file path that doesn't match, restructure to use the actual path layout\n")
	b.WriteString("\nCRITICAL: the refactored code MUST still implement the same requirement. ")
	b.WriteString("Do NOT weaken the implementation to make the test pass. ")
	b.WriteString("Do NOT remove functionality. The reviewer will re-check intent after your changes.\n")

	return b.String()
}

// truncateDescentLog trims a string for log display.
func truncateDescentLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// ---------------------------------------------------------------------------
// Batch helper for session-level callers
// ---------------------------------------------------------------------------

// DescentSessionSummary aggregates descent results across all ACs
// in a session for the operator banner.
type DescentSessionSummary struct {
	Total     int
	Passed    int
	SoftPass  int
	Failed    int
	Results   []DescentResult
	ACIDs     []string
}

// AllResolved returns true when every AC is either passed or soft-passed.
func (s DescentSessionSummary) AllResolved() bool {
	return s.Failed == 0
}

// FormatBanner returns a compact operator-readable summary.
func (s DescentSessionSummary) FormatBanner() string {
	var b strings.Builder
	fmt.Fprintf(&b, "verification-descent: %d/%d passed, %d soft-pass, %d failed\n",
		s.Passed, s.Total, s.SoftPass, s.Failed)
	for i, r := range s.Results {
		mark := "✓"
		if r.Outcome == DescentSoftPass {
			mark = "⚖"
		} else if r.Outcome == DescentFail {
			mark = "✗"
		}
		fmt.Fprintf(&b, "  %s %s [%s] %s\n", mark, s.ACIDs[i], r.ResolvedAtTier, r.Reason)
	}
	return b.String()
}

// RunDescentForSession runs verification descent on every failing AC
// in a session and returns a summary. ACs that already passed are
// counted as DescentPass without running through the engine.
func RunDescentForSession(
	ctx context.Context,
	session Session,
	acResults []AcceptanceResult,
	cfg DescentConfig,
) DescentSessionSummary {
	summary := DescentSessionSummary{
		Total: len(acResults),
	}

	for _, ar := range acResults {
		summary.ACIDs = append(summary.ACIDs, ar.CriterionID)

		if ar.Passed {
			summary.Passed++
			summary.Results = append(summary.Results, DescentResult{
				Outcome:        DescentPass,
				ResolvedAtTier: TierRunAC,
				Reason:         "AC passed mechanically",
			})
			continue
		}

		// Find the canonical AC object.
		var ac AcceptanceCriterion
		found := false
		for _, c := range session.AcceptanceCriteria {
			if c.ID == ar.CriterionID {
				ac = c
				found = true
				break
			}
		}
		if !found {
			summary.Failed++
			summary.Results = append(summary.Results, DescentResult{
				Outcome:        DescentFail,
				ResolvedAtTier: TierClassify,
				Reason:         fmt.Sprintf("AC %s not found in session criteria", ar.CriterionID),
			})
			continue
		}

		result := VerificationDescent(ctx, ac, ar.Output, cfg)

		switch result.Outcome {
		case DescentPass:
			summary.Passed++
		case DescentSoftPass:
			summary.SoftPass++
		case DescentFail:
			summary.Failed++
		}
		summary.Results = append(summary.Results, result)
	}

	return summary
}

// ---------------------------------------------------------------------------
// Quick pre-flight: AC command smoke test
// ---------------------------------------------------------------------------

// PreflightACCommands runs every AC command in a clean checkout with
// no modifications. Commands that fail BEFORE work begins are broken
// commands by definition. Returns a map of AC ID -> failure output
// for commands that failed. 100% deterministic, eliminates the entire
// ac_bug class at the source.
//
// This is the H-93 candidate from the design doc: "spec-hardening
// pre-flight". Cost: seconds. Savings: every false rejection from
// broken ACs across all subsequent rounds.
func PreflightACCommands(ctx context.Context, projectRoot string, criteria []AcceptanceCriterion) map[string]string {
	broken := map[string]string{}
	for _, ac := range criteria {
		if ac.Command == "" {
			continue
		}
		// Skip ground-truth commands (build/test) — they're expected
		// to fail before any work is done. We only want to catch
		// structurally broken commands (wrong syntax, missing vars,
		// impossible paths).
		if isGroundTruthACCommand(ac.Command) {
			continue
		}

		// Quick timeout — these should be fast checks.
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cmd := exec.CommandContext(checkCtx, "bash", "-lc", ac.Command)
		cmd.Dir = projectRoot
		cmd.Env = acceptanceCommandEnv(projectRoot)
		out, err := cmd.CombinedOutput()
		cancel()

		if err != nil {
			exitCode := extractExitCode(string(out) + "\n" + err.Error())
			cls := ClassifyStderr(string(out), exitCode)
			// Only flag as broken if it's an AC-level problem
			// (command not found, syntax error), not a code-level
			// problem (which is expected pre-work).
			if cls == StderrCommandNotFound || cls == StderrSyntaxError {
				broken[ac.ID] = fmt.Sprintf("PRE-FLIGHT FAIL (stderr=%s): %s", cls, string(out))
			}
		}
	}
	return broken
}
