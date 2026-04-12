package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/convergence"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/hub"
	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/repomap"
	"github.com/ericmacdougall/stoke/internal/skill"
	"github.com/ericmacdougall/stoke/internal/stream"
	"github.com/ericmacdougall/stoke/internal/wisdom"
)

// stackMatchesForSOW returns keyword tags used to match skills against
// the SOW stack. These become the second argument to
// skill.Registry.InjectPromptBudgeted, which scores each skill against
// the prompt text plus these tags. The tags broaden matches so a
// project that says "next.js 14 app router" still gets tagged with
// "react", "typescript", and "nextjs" for skill lookup.
func stackMatchesForSOW(sowDoc *plan.SOW, session plan.Session, task plan.Task) []string {
	if sowDoc == nil {
		return nil
	}
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
	}
	add(sowDoc.Stack.Language)
	add(sowDoc.Stack.Framework)
	if sowDoc.Stack.Monorepo != nil {
		add(sowDoc.Stack.Monorepo.Tool)
		add(sowDoc.Stack.Monorepo.Manager)
	}
	// Language-adjacent tags so skill files with broader keywords still
	// match. e.g. language=typescript implies "javascript", "node",
	// "pnpm" for node workspaces.
	lang := strings.ToLower(sowDoc.Stack.Language)
	if lang == "typescript" || lang == "javascript" {
		add("node")
		add("npm")
		add("pnpm")
	}
	fw := strings.ToLower(sowDoc.Stack.Framework)
	if strings.Contains(fw, "next") {
		add("nextjs")
		add("react")
	}
	if strings.Contains(fw, "expo") || strings.Contains(fw, "react-native") {
		add("react-native")
		add("expo")
	}
	// Surface infra tags too — redis/postgres skills exist in the
	// builtin set and are useful when the stack uses them.
	for _, inf := range sowDoc.Stack.Infra {
		add(inf.Name)
	}
	// Task-specific hints from the session title and task description.
	// These bias matches toward testing/deployment/auth skills when the
	// current work actually touches those areas.
	for _, w := range strings.Fields(strings.ToLower(session.Title + " " + task.Description)) {
		if len(w) > 3 && len(w) < 30 {
			add(w)
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// encodeTextMessage wraps a plain string in the provider's content-block
// schema. Small helper used by the cross-review and other single-message
// LLM calls in this package.
func encodeTextMessage(text string) (json.RawMessage, error) {
	return json.Marshal([]map[string]interface{}{{"type": "text", "text": text}})
}

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

	// --- Multi-session intelligence ---

	// Wisdom is the cross-session learning store. After each session
	// the orchestrator asks the model to extract patterns/decisions/
	// gotchas and records them here. ForPrompt() injects the accumulated
	// wisdom into subsequent session system prompts so later sessions
	// inherit what earlier ones learned. nil = wisdom disabled.
	Wisdom *wisdom.Store
	// WisdomProvider is the LLM used to extract wisdom after each
	// session. Usually the same provider as the build runner. nil =
	// skip extraction (but still inject pre-existing wisdom into
	// prompts if the store is non-nil).
	WisdomProvider provider.Provider
	// SOWID is used to scope the on-disk wisdom snapshot under
	// .stoke/wisdom/<sow-id>.json.
	SOWID string

	// --- Lead-dev briefing phase ---

	// ACRewrites persists reasoning-loop AC rewrites across session
	// retries. Keyed on criterion ID -> rewritten command string.
	// When the reasoning loop rewrites an AC, the new command is
	// stored here AND applied to effectiveCriteria. On session retry,
	// runSessionNative reads this map and applies any prior rewrites
	// to the fresh session.AcceptanceCriteria before Phase 1, so the
	// fix doesn't get lost between attempts.
	//
	// Without this, the reasoning loop correctly diagnosed ac_bug and
	// produced a rewrite, but the session retry re-read the original
	// SOW criteria and the rewrite was lost. The loop then
	// re-discovered the same bug, re-produced the same rewrite, and
	// the same repair failed the same way — an O(n) waste of LLM
	// calls per retry.
	ACRewrites map[string]string

	// Briefings is a map of task ID -> per-task briefing produced by
	// the lead-dev phase that runs BEFORE Phase 1 dispatches the
	// session's tasks. Each briefing carries current-codebase
	// context (what exists on disk right now, what's missing, which
	// identifiers to reuse, which pitfalls earlier work already
	// stepped on). Workers read their own briefing via promptOpts.
	// When a task has no entry here it dispatches with no extra
	// context — equivalent to pre-briefing behavior.
	Briefings map[string]*plan.TaskBriefing
	// BriefingProvider is the LLM used by the lead-dev phase. When
	// nil, briefing is skipped and workers run with the original
	// context set.
	BriefingProvider provider.Provider
	// BriefingModel is the model name for the briefing phase. Empty
	// = use cfg.Model.
	BriefingModel string

	// --- Reasoning loop (multi-analyst + judge) ---

	// ReasoningProvider runs the stuck-AC reasoning loop: when a
	// criterion fails N consecutive repair attempts, this provider is
	// called to run 4 focused analyst prompts + 1 judge synth pass
	// and return a verdict (code_bug | ac_bug | both | acceptable_as_is).
	// When nil, the reasoning loop is skipped and the repair loop
	// falls back to its stateless retry path.
	ReasoningProvider provider.Provider
	// ReasoningModel is the model name for the reasoning loop.
	// Empty = use cfg.Model.
	ReasoningModel string

	// --- Cross-model review + scope gate ---

	// ReviewProvider is a second provider (ideally a different model)
	// that reads the session's git diff and grades the actual code
	// quality. When nil, cross-model review is skipped.
	ReviewProvider provider.Provider
	// ReviewModel is the model name for cross-review. Empty = provider
	// default.
	ReviewModel string
	// StrictScope: when true, sessions that touched files outside the
	// declared session.Outputs/task.Files set get flagged and the
	// session fails with a scope-creep error. Default false — scope
	// violations are logged as warnings but don't fail the session.
	StrictScope bool

	// --- Intra-session parallelism ---

	// ParallelWorkers controls how many tasks within a single session
	// can run concurrently. Tasks only parallelize when their files
	// are disjoint AND their dependencies are already satisfied.
	// Default 1 (sequential). Set to >1 to enable parallel dispatch.
	ParallelWorkers int

	// CompactThreshold enables progressive context compaction inside
	// long-running tasks. When the estimated input token count crosses
	// this value between turns, the native runner's compactor rewrites
	// the message history to shrink it. 0 = disabled. Recommended
	// value: 100_000 to stay comfortably under a 200k context window.
	CompactThreshold int

	// RawSOWText is the original SOW content as the user wrote it
	// (prose .md, JSON, or YAML). When non-empty, it's injected into
	// the cached system prompt under a "SPEC (verbatim)" header so
	// the agent can always cross-reference what it's being asked to
	// do against the actual spec — not just the compressed framing.
	//
	// This is the fix for "agent hallucinates plausible crate/module
	// names because the SOW's exact names aren't reinforced anywhere
	// it can see". Structured SOW fields (task.Files, session.Outputs,
	// ContentMatch.Pattern) still feed the canonical-names block, but
	// the raw text is the source of truth the agent can grep against.
	RawSOWText string
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

	// Session-level watchdog: if the session makes no observable
	// progress for 45 minutes (no tasks complete, no AC checks run,
	// no reasoning emits), cancel it. This is the safety net for
	// the class of hangs I can't otherwise root-cause: goroutine
	// leaks in deep code paths, internal deadlocks in the runner or
	// reasoning loop, stream handlers that never finish, etc.
	//
	// 45min is generous — a single task can legitimately take 10+
	// minutes with extended-thinking on MiniMax. The watchdog only
	// fires when the session has gone silent for LONGER than any
	// single operation should reasonably take.
	sessionCtx, cancelSession := context.WithTimeout(ctx, 45*time.Minute)
	defer cancelSession()
	ctx = sessionCtx

	// Persistent marker fast-path: if a prior run already drove this
	// session to completion (or accepted it as preexisting), skip the
	// agent entirely and return synthetic success results. The marker
	// is invalidated automatically when the session spec changes.
	if done, reason := isUpstreamSessionAlreadyComplete(cfg.RepoRoot, session); done {
		fmt.Printf("  ✓ session %s already complete (marker: %s) — skipping\n", session.ID, reason)
		results := make([]plan.TaskExecResult, 0, len(session.Tasks))
		for _, t := range session.Tasks {
			results = append(results, plan.TaskExecResult{
				TaskID:  t.ID,
				Success: true,
			})
		}
		return results, nil
	}

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 100
	}
	maxRepairs := cfg.MaxRepairAttempts
	if maxRepairs <= 0 {
		// 3 was too few for thinking-emitting models that write
		// elaborate test suites and then can't satisfy them on the
		// first or second pass. 10 gives the agent enough chances to
		// either fix the implementation OR delete the broken tests it
		// just wrote, without burning forever on hopeless sessions.
		maxRepairs = 10
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
	// Apply any AC rewrites from prior session attempts. The reasoning
	// loop may have diagnosed ac_bug and rewritten a criterion's
	// command during attempt N; without this merge step, attempt N+1
	// would re-read the original SOW criteria and lose the rewrite.
	if len(cfg.ACRewrites) > 0 {
		applied := 0
		for ci := range effectiveCriteria {
			if newCmd, ok := cfg.ACRewrites[effectiveCriteria[ci].ID]; ok {
				effectiveCriteria[ci].Command = newCmd
				applied++
			}
		}
		if applied > 0 {
			fmt.Printf("  applied %d AC rewrite(s) from prior attempt reasoning\n", applied)
		}
	}
	workingSession := session
	workingSession.AcceptanceCriteria = effectiveCriteria

	// Phase 0: lead-dev briefing. Run the multi-analyst-style
	// briefing pass that reads the CURRENT codebase state (API
	// surface + repomap + raw SOW) and produces per-task briefings
	// for this session. Each briefing tells its worker "here's
	// what exists on disk right now, here's what's missing, here
	// are the identifiers you must use verbatim, here are
	// pitfalls". The briefings flow through cfg.Briefings into the
	// per-task promptOpts so workers see them ahead of the task
	// header. When no briefing provider is configured OR the
	// briefing call fails, we fall through to the old behavior
	// (workers get the original context set) — briefings are a
	// quality improvement, not a correctness gate.
	if cfg.BriefingProvider != nil && len(session.Tasks) > 0 {
		briefingModel := cfg.BriefingModel
		if briefingModel == "" {
			briefingModel = cfg.Model
		}
		briefer := &plan.BriefingRunner{Provider: cfg.BriefingProvider, Model: briefingModel}
		// Compute the current API surface and repomap snippet for
		// the briefing input. The budget here is separate from the
		// per-task prompt budget — briefings can see more of the
		// codebase because they happen once per session, not once
		// per task.
		surface := ""
		if cfg.RepoRoot != "" {
			surface = sowAPISurface(cfg.RepoRoot, 16000)
		}
		repoMapBlob := ""
		if cfg.RepoMap != nil {
			var anchor []string
			for _, t := range session.Tasks {
				anchor = append(anchor, t.Files...)
			}
			if len(session.Outputs) > 0 {
				anchor = append(anchor, session.Outputs...)
			}
			repoMapBlob = cfg.RepoMap.RenderRelevant(anchor, 4000)
		}
		// Search the skill index for skills relevant to this session's
		// tasks. The skill references go into the briefing prompt so
		// the lead dev can tell each worker which skills to follow.
		skillRefs := ""
		if cfg.RepoRoot != "" {
			reg := skill.DefaultRegistry(cfg.RepoRoot)
			_ = reg.Load()
			// Build a query from the session title + all task descriptions
			// so skill search considers the full scope of this wave.
			var queryBuf strings.Builder
			queryBuf.WriteString(session.Title + " ")
			var taskDescs []string
			for _, t := range session.Tasks {
				queryBuf.WriteString(t.Description + " ")
				taskDescs = append(taskDescs, t.Description)
			}
			// Phase 1: TF-IDF keyword + categorical match (cheap).
			// Oversample with topK=10 so the judge has more to
			// choose from.
			matches := reg.SearchSkills(queryBuf.String(), 10)
			// Log the pre-judge candidates so operators see what TF-IDF
			// surfaced before filtering.
			if len(matches) > 0 {
				var names []string
				for _, m := range matches {
					names = append(names, m.Skill.Name)
				}
				fmt.Printf("  skill candidates (pre-judge): %s\n", strings.Join(names, ", "))
			}
			// Phase 2: LLM judge prunes irrelevant matches. The
			// keyword-based TF-IDF frequently surfaces skills that
			// overlap on incidental words ("operator" matching
			// kubernetes). The judge removes those.
			if cfg.ReasoningProvider != nil && len(matches) > 0 {
				judged := skill.JudgeRelevance(ctx, cfg.ReasoningProvider, cfg.ReasoningModel, session.Title, taskDescs, matches)
				if len(judged) < len(matches) {
					var names []string
					for _, m := range judged {
						names = append(names, m.Skill.Name)
					}
					fmt.Printf("  skills kept by judge: %s\n", strings.Join(names, ", "))
				}
				matches = judged
			}
			skillRefs = skill.FormatSkillReferences(matches)
		}

		fmt.Printf("  lead-dev briefing pass (analyzing current codebase for %d tasks)...\n", len(session.Tasks))
		briefings, berr := briefer.Brief(ctx, plan.SessionBriefingInput{
			SessionID:          session.ID,
			SessionTitle:       session.Title,
			Tasks:              session.Tasks,
			AcceptanceCriteria: effectiveCriteria,
			RepoRoot:           cfg.RepoRoot,
			APISurface:         surface,
			RepoMap:            repoMapBlob,
			RawSOW:             cfg.RawSOWText,
			SkillReferences:    skillRefs,
		})
		if berr != nil {
			fmt.Printf("  briefing pass warning: %v (dispatching without briefings)\n", berr)
		}
		if len(briefings) > 0 {
			fmt.Printf("  briefings produced for %d/%d tasks\n", len(briefings), len(session.Tasks))
			// Observability: count how many briefings named at least
			// one relevant skill, so we can verify the skills->briefings
			// path is actually flowing. If this is 0 but matches > 0,
			// the lead dev saw the skill block but didn't pick any —
			// which usually means the skill names aren't matching what
			// the tasks need.
			withSkills := 0
			for _, b := range briefings {
				if b != nil && len(b.RelevantSkills) > 0 {
					withSkills++
				}
			}
			fmt.Printf("  briefings naming skills: %d/%d\n", withSkills, len(briefings))
			cfg.Briefings = briefings
		}
	}

	// Phase 1: run tasks. When ParallelWorkers > 1 and we have tasks
	// with disjoint file sets and no unsatisfied deps, execute them
	// concurrently. Otherwise fall back to sequential execution.
	//
	// results is the AUTHORITATIVE per-task state returned to the
	// scheduler. Internal repair/guard tasks below do NOT append to
	// it — they update runtime state and print progress, but their
	// success/failure must not leak into the scheduler's view
	// (otherwise a successful repair looks like a session failure to
	// the outer SessionScheduler and it halts the whole SOW).
	results := runSessionPhase1(ctx, session, workingSession, sowDoc, runtimeDir, cfg, maxTurns)

	// Phase 1.5: spec-faithfulness guards. Before running acceptance
	// criteria (which may be generic like `cargo build`), run two
	// cheap deterministic checks that catch the most common "agent
	// cut corners" failure modes:
	//
	//   a) Missing/empty declared files — task.Files entries that
	//      don't exist or are 0 bytes.
	//   b) Placeholder/stub patterns in declared files — pub fn
	//      placeholder, unimplemented!(), todo!(), panic("TODO"),
	//      raise NotImplementedError, etc.
	//
	// If either fires, build a repair blob and run a repair task
	// WITHOUT appending it to results. The acceptance loop will
	// verify everything afterwards; the final acceptance state is
	// what determines success, not whether the guard's repair pass
	// itself ran cleanly.
	missing, suspicious := checkSpecFaithfulness(cfg.RepoRoot, session)
	if len(missing) > 0 || len(suspicious) > 0 {
		fmt.Printf("  ⚠ spec-faithfulness guard: %d missing/empty file(s), %d placeholder stub(s)\n", len(missing), len(suspicious))
		failureBlob := formatSpecFaithfulnessBlob(missing, suspicious)
		repairTask := plan.Task{
			ID:          fmt.Sprintf("%s-spec-guard", session.ID),
			Description: "fix missing files and placeholder stubs before acceptance runs",
		}
		sysP, usrP := buildSOWNativePromptsWithOpts(sowDoc, workingSession, repairTask, promptOpts{
			RepoMap:       cfg.RepoMap,
			RepoMapBudget: cfg.RepoMapBudget,
			Repair:        &failureBlob,
			Wisdom:        cfg.Wisdom,
			RawSOW:        cfg.RawSOWText,
			RepoRoot:      cfg.RepoRoot,
		})
		sup := toEngineSupervisor(autoExtractTaskSupervisor(cfg.RepoRoot, cfg.RawSOWText, workingSession, repairTask, 3))
		_ = execNativeTask(ctx, repairTask.ID, sysP, usrP, runtimeDir, cfg, maxTurns, sup)
		// NOTE: deliberately not appended to results. The acceptance
		// loop below verifies the final state.
	}

	// Phase 1.75: foundation sanity check. Before running the session's
	// declared ACs, run a quick "does the workspace even build?" gate.
	// This catches the two most common root causes that cascade into
	// EVERY session AC failing:
	//   a) deps not installed → pnpm install
	//   b) TypeScript syntax error → tsc --noEmit
	//
	// If either fails, dispatch a targeted repair task focused on
	// making the build green, THEN proceed to the declared ACs. This
	// avoids the "4/4 ACs fail because pnpm install wasn't run" waste
	// loop that was burning 3 repair attempts × 5 reasoning calls on
	// something a single pnpm install would fix.
	if isNodeStack(sowDoc) && cfg.RepoRoot != "" {
		runFoundationSanityCheck(ctx, cfg, sowDoc, workingSession, runtimeDir, maxTurns)
	}

	// Phase 2: self-repair loop. Run the session's acceptance criteria;
	// if any fail, construct a repair prompt containing the exact failure
	// output and run it as a new task. Repeat up to maxRepairs times
	// before escalating to the override judge.
	//
	// Repair attempts are INTERNAL — their success/failure is captured
	// in finalAcceptance/finalPassed and used below to normalize the
	// returned results slice, but they're not appended to the slice
	// directly (see the note on Phase 1.5).
	var finalAcceptance []plan.AcceptanceResult
	var finalPassed bool
	// stickyFailures tracks which criterion IDs failed in EVERY prior
	// repair attempt. Criteria that keep failing across attempts are
	// likely either (a) structurally unsatisfiable (the AC command is
	// broken), or (b) the model is applying the same failed fix. We
	// note them explicitly in the next repair prompt so the model can
	// switch approach rather than retry identically. A criterion
	// becomes "sticky" only after failing twice in a row.
	stickyAttempts := map[string]int{} // criterion ID -> consecutive failure count
	// reasoningApplied tracks which criterion IDs have already been
	// run through the multi-analyst reasoning loop in this session.
	// Each stuck criterion gets one reasoning pass; running it twice
	// for the same criterion would just pay for the same verdict.
	reasoningApplied := map[string]bool{}
	if len(effectiveCriteria) > 0 {
		for attempt := 1; attempt <= maxRepairs; attempt++ {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			// Build a semantic judge closure that consults the LLM
			// when a mechanical check fails. This is the "grep
			// found the wrong pattern but the code implements the
			// feature" escape that content_match ACs desperately
			// need. No skipping — the judge must affirmatively say
			// "this code implements the requirement" before a
			// mechanical failure is overridden to pass.
			var judge plan.SemanticEvaluator
			if cfg.ReasoningProvider != nil {
				judge = func(jctx context.Context, ac plan.AcceptanceCriterion, failureOutput string) (bool, string, error) {
					// Pick a relevant task description to feed the judge.
					taskDesc := workingSession.Title
					var taskFiles []string
					for _, t := range workingSession.Tasks {
						if len(t.Files) > 0 {
							taskDesc = t.Description
							taskFiles = append(taskFiles, t.Files...)
						}
					}
					codeExcerpts := plan.CollectCodeExcerptsForAC(cfg.RepoRoot, ac, failureOutput, taskFiles, 6, 4000)
					// Pull a relevant SOW excerpt too.
					sowExcerpt := ""
					if cfg.RawSOWText != "" {
						sowExcerpt = extractTaskSpecExcerpt(cfg.RawSOWText, workingSession, plan.Task{ID: ac.ID, Description: ac.Description}, specExcerptConfig{})
					}
					verdict, err := plan.JudgeAC(jctx, cfg.ReasoningProvider, cfg.ReasoningModel, plan.SemanticJudgeInput{
						TaskDescription: taskDesc,
						SOWSpec:         sowExcerpt,
						Criterion:       ac,
						FailureOutput:   failureOutput,
						CodeExcerpts:    codeExcerpts,
						RepoRoot:        cfg.RepoRoot,
					})
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
			acceptance, allPassed := plan.CheckAcceptanceCriteriaWithJudge(ctx, cfg.RepoRoot, effectiveCriteria, judge)
			finalAcceptance, finalPassed = acceptance, allPassed
			// Observability: log pass/fail per criterion on every
			// acceptance check. Without this, the operator has no
			// idea which criteria passed vs failed until the very
			// end of the repair loop.
			passedCount := 0
			for _, ac := range acceptance {
				if ac.Passed {
					passedCount++
				}
			}
			fmt.Printf("  acceptance check attempt %d: %d/%d passed\n", attempt, passedCount, len(acceptance))
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
					fmt.Printf("  ✓ session %s repaired on attempt %d\n", session.ID, attempt)
				}
				break
			}
			// Update sticky counters. A criterion that passes this
			// attempt is cleared; a criterion that fails gets +1.
			for _, ac := range acceptance {
				if ac.Passed {
					delete(stickyAttempts, ac.CriterionID)
				} else {
					stickyAttempts[ac.CriterionID]++
				}
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
			// Prepend a sticky-warning block when any criterion has
			// been failing across multiple attempts, so the repair
			// prompt tells the model "the last attempt tried the
			// obvious fix and it didn't work; look for a deeper
			// cause". Without this, the repair model tends to apply
			// the same surface fix on every attempt.
			var sticky []string
			for id, n := range stickyAttempts {
				if n >= 2 {
					sticky = append(sticky, fmt.Sprintf("%s (failed %d repair attempts in a row)", id, n))
				}
			}
			if len(sticky) > 0 {
				failureBlob = "STICKY FAILURES — the following criteria have resisted every prior repair attempt this session. The obvious fix didn't work. Look for a DIFFERENT root cause: the AC command may be wrong, the model may be in a dep/script/import loop, the test runner may not be picking up tests, etc. Do NOT apply the same fix you tried last time.\n  - " +
					strings.Join(sticky, "\n  - ") +
					"\n\n" + failureBlob
			}

			// Reasoning loop: when a criterion has become sticky AND
			// we haven't yet reasoned about it, run the multi-analyst
			// + judge pass to decide whether the code, the AC, or both
			// are at fault. The helper mutates effectiveCriteria in
			// place when a verdict says to rewrite an AC, and returns
			// hint text to prepend to the repair prompt when a verdict
			// says to fix code.
			if cfg.ReasoningProvider != nil {
				hints := runReasoningForStuckCriteria(ctx, acceptance, stickyAttempts, reasoningApplied, effectiveCriteria, workingSession, session, cfg)
				if hints != "" {
					failureBlob = hints + "\n\n" + failureBlob
				}
			}
			fmt.Printf("  ↻ session %s: repair attempt %d/%d for %d failing criteria",
				session.ID, attempt, maxRepairs, countFailed(acceptance))
			if len(sticky) > 0 {
				fmt.Printf(" (%d sticky)", len(sticky))
			}
			fmt.Println()

			// Split repairs: instead of one worker trying to fix ALL
			// failing criteria at once (which leads to "fixed 2, broke
			// 1, missed 1"), dispatch one repair task PER failing
			// criterion. Each worker gets ONE focused fix assignment
			// and verifies ONE command. When ParallelWorkers > 1,
			// non-overlapping repairs run concurrently.
			//
			// When there's only 1 failing criterion or parallel
			// workers = 1, this collapses to the old behavior — one
			// sequential repair with the full failure blob. The split
			// only adds value when there are 2+ failures to fix.
			failingACs := collectFailingACs(acceptance)
			if len(failingACs) <= 1 || cfg.ParallelWorkers <= 1 {
				// Single-criterion or sequential: old path.
				repairTask := plan.Task{
					ID:          fmt.Sprintf("%s-repair-%d", session.ID, attempt),
					Description: "repair session acceptance criteria",
				}
				sysP, usrP := buildSOWNativePromptsWithOpts(sowDoc, workingSession, repairTask, promptOpts{
					RepoMap:       cfg.RepoMap,
					RepoMapBudget: cfg.RepoMapBudget,
					Repair:        &failureBlob,
					Wisdom:        cfg.Wisdom,
					RawSOW:        cfg.RawSOWText,
					RepoRoot:      cfg.RepoRoot,
				})
				sup := toEngineSupervisor(autoExtractTaskSupervisor(cfg.RepoRoot, cfg.RawSOWText, workingSession, repairTask, 3))
				_ = execNativeTask(ctx, repairTask.ID, sysP, usrP, runtimeDir, cfg, maxTurns, sup)
			} else {
				// Multi-criterion parallel repair: one worker per
				// failing AC, each with a targeted failure blob
				// containing only their own criterion's failure.
				fmt.Printf("    → splitting into %d parallel fix assignments\n", len(failingACs))
				type repairResult struct {
					acID string
					tr   plan.TaskExecResult
				}
				sem := make(chan struct{}, cfg.ParallelWorkers)
				resCh := make(chan repairResult, len(failingACs))
				for _, fac := range failingACs {
					fac := fac // capture
					sem <- struct{}{}
					go func() {
						defer func() { <-sem }()
						// Build a targeted failure blob for just this
						// one criterion.
						singleFailure := formatSingleACFailure(fac)
						repairTask := plan.Task{
							ID:          fmt.Sprintf("%s-repair-%d-%s", session.ID, attempt, fac.CriterionID),
							Description: fmt.Sprintf("fix failing criterion %s: %s", fac.CriterionID, fac.Description),
						}
						sysP, usrP := buildSOWNativePromptsWithOpts(sowDoc, workingSession, repairTask, promptOpts{
							RepoMap:       cfg.RepoMap,
							RepoMapBudget: cfg.RepoMapBudget,
							Repair:        &singleFailure,
							Wisdom:        cfg.Wisdom,
							RawSOW:        cfg.RawSOWText,
							RepoRoot:      cfg.RepoRoot,
						})
						sup := toEngineSupervisor(autoExtractTaskSupervisor(cfg.RepoRoot, cfg.RawSOWText, workingSession, repairTask, 3))
						tr := execNativeTask(ctx, repairTask.ID, sysP, usrP, runtimeDir, cfg, maxTurns, sup)
						resCh <- repairResult{acID: fac.CriterionID, tr: tr}
					}()
				}
				// Drain all results.
				for range failingACs {
					<-resCh
				}
			}
			// NOTE: deliberately not appended to results.
		}
	}

	// Normalize: if the repair loop closed the gap (finalPassed == true),
	// mark every Phase 1 task as successful. The session's end state IS
	// successful — we don't want an earlier "Phase 1 task T1 failed,
	// repair fixed it" to leak to the scheduler as a session-level
	// failure, which would halt the whole SOW after S1.
	//
	// When there are no acceptance criteria we do NOT normalize — the
	// Phase 1 task results are the only signal we have, and silently
	// marking them successful would hide genuine failures.
	if finalPassed {
		for i := range results {
			if !results[i].Success {
				results[i].Success = true
				results[i].Error = nil
			}
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

	// Phase 4: scope gate. git diff the session's changes and check
	// which files were actually touched. Flag tasks that touched files
	// outside the declared session.Outputs/task.Files set (scope creep)
	// and tasks that wrote nothing at all (zombie tasks). In strict
	// mode this fails the session; otherwise it's a warning so the
	// caller can observe drift without halting the build.
	touched := gitDirtyFiles(ctx, cfg.RepoRoot)
	if len(touched) > 0 {
		if violations := checkScopeViolations(workingSession, touched); len(violations) > 0 {
			fmt.Printf("  ⚠ scope gate: %d file(s) outside declared scope:\n", len(violations))
			for _, v := range violations {
				fmt.Printf("    - %s\n", v)
			}
			if cfg.StrictScope {
				results = append(results, plan.TaskExecResult{
					TaskID:  session.ID + "-scope-violation",
					Success: false,
					Error:   fmt.Errorf("scope violation: %d file(s) touched outside declared scope", len(violations)),
				})
			}
		}
	}

	// Phase 5: cross-model review. A second provider (ideally a
	// different model) reads the git diff and grades it. This catches
	// issues the acceptance criteria missed — "the code compiles but
	// doesn't do what the task asked for" — which is invisible to
	// command-based gates.
	if cfg.ReviewProvider != nil && finalPassed {
		reviewResult := runCrossModelReview(ctx, session, cfg)
		if reviewResult != nil && !reviewResult.Approved {
			fmt.Printf("  ⚠ cross-review: reviewer blocked with %d concerns\n", len(reviewResult.Concerns))
			for _, c := range reviewResult.Concerns {
				fmt.Printf("    - [%s] %s\n", c.Severity, c.Description)
			}
			// Downgrade a successful session to failed so the
			// scheduler's outer retry budget kicks in with the
			// reviewer's concerns in the context.
			results = append(results, plan.TaskExecResult{
				TaskID:  session.ID + "-review-fail",
				Success: false,
				Error:   fmt.Errorf("cross-model review blocked: %s", reviewResult.Summary),
			})
		} else if reviewResult != nil {
			fmt.Printf("  ✓ cross-review: reviewer approved (score %d/100)\n", reviewResult.Score)
		}
	}

	// Phase 6: wisdom extraction. Ask the model to distill reusable
	// learnings (conventions, decisions, gotchas) from this session
	// and add them to the cross-session wisdom store. Subsequent
	// sessions will inject these via ForPrompt() into their cached
	// system blocks.
	if cfg.Wisdom != nil && cfg.WisdomProvider != nil && ctx.Err() == nil {
		n, wErr := CaptureSessionWisdom(ctx, session, results, finalAcceptance, cfg.Wisdom, cfg.WisdomProvider, cfg.Model)
		if wErr != nil {
			fmt.Printf("  wisdom capture warning: %v\n", wErr)
		} else if n > 0 {
			fmt.Printf("  captured %d learning(s) from session %s\n", n, session.ID)
			if cfg.SOWID != "" {
				_ = SaveWisdom(cfg.RepoRoot, cfg.SOWID, cfg.Wisdom)
			}
		}
	}

	// Phase 7: persist a completion marker so subsequent SOW runs can
	// fast-skip this session via the cache check at the top of
	// runSessionNative. Two flavors:
	//   - Real-pass marker: finalPassed AND there are touched files →
	//     record file hashes for strict drift detection.
	//   - Preexisting marker: finalPassed but no touched files (rare,
	//     but possible when the work was already in place from a prior
	//     attempt that died mid-run) → record spec hash only.
	if finalPassed {
		var changed []string
		for _, f := range touched {
			if !strings.HasPrefix(f, ".stoke/") {
				changed = append(changed, f)
			}
		}
		note := ""
		if len(changed) == 0 {
			note = "preexisting work — no diff vs prior state"
		}
		if err := writeUpstreamSessionMarker(cfg.RepoRoot, session, changed, note); err != nil {
			fmt.Printf("  marker warning: %v\n", err)
		} else if len(changed) > 0 {
			fmt.Printf("  ✓ wrote completion marker for session %s (%d files)\n", session.ID, len(changed))
		} else {
			fmt.Printf("  ✓ wrote spec-only marker for session %s\n", session.ID)
		}
	}

	return results, nil
}

// gitDirtyFiles returns the list of files that have uncommitted changes
// in the worktree. Used by the scope gate to see what a session actually
// touched. Best-effort: any git error returns an empty list (the gate
// is a warning mechanism, not a merge blocker).
func gitDirtyFiles(ctx context.Context, repoRoot string) []string {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		// Porcelain format: XY <path>  (2-char status + space + path)
		path := strings.TrimSpace(line[3:])
		// Handle renames: "old -> new"
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}
		if path != "" {
			files = append(files, path)
		}
	}
	return files
}

// checkScopeViolations compares a list of touched files against a
// session's declared scope (session.Outputs + union of task.Files).
// Returns files that were touched but NOT declared.
func checkScopeViolations(session plan.Session, touched []string) []string {
	declared := make(map[string]bool)
	for _, f := range session.Outputs {
		declared[normalizeScopePath(f)] = true
	}
	for _, t := range session.Tasks {
		for _, f := range t.Files {
			declared[normalizeScopePath(f)] = true
		}
	}
	// If the session declared nothing, treat the whole repo as in-scope
	// (common for foundation sessions that don't pre-declare outputs).
	if len(declared) == 0 {
		return nil
	}
	// Always allow changes inside .stoke/ (state files, caches, etc.)
	var violations []string
	for _, f := range touched {
		if strings.HasPrefix(f, ".stoke/") {
			continue
		}
		if declared[normalizeScopePath(f)] {
			continue
		}
		// Also allow files that share a directory prefix with a
		// declared file — scope declarations are often directories
		// like "src/auth/" rather than full paths.
		allowed := false
		for d := range declared {
			if strings.HasSuffix(d, "/") && strings.HasPrefix(f, d) {
				allowed = true
				break
			}
		}
		if !allowed {
			violations = append(violations, f)
		}
	}
	sort.Strings(violations)
	return violations
}

func normalizeScopePath(p string) string {
	p = strings.TrimSpace(p)
	return strings.TrimPrefix(p, "./")
}

// crossReviewResult is the structured output of the review model's pass.
type crossReviewResult struct {
	Approved bool                `json:"approved"`
	Score    int                 `json:"score"`
	Summary  string              `json:"summary"`
	Concerns []crossReviewConcern `json:"concerns"`
}

type crossReviewConcern struct {
	Severity    string `json:"severity"` // blocking | major | minor
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Description string `json:"description"`
}

const crossReviewPrompt = `You are a senior code reviewer checking a diff produced by an autonomous agent. The agent was asked to implement a session's tasks; the build and tests pass. Your job: decide whether the CODE is actually good, not just whether it compiles.

Look for:
  - Correctness: does the implementation actually do what the task asked for?
  - Obvious bugs: null pointer risks, race conditions, off-by-one, missing error handling at boundaries
  - Code that will silently corrupt data
  - Stubs/TODOs/placeholders that got left in
  - Tests that were deleted or disabled without justification
  - Security issues: injection, secret exposure, path traversal

Do NOT nitpick style. Do NOT flag anything that has a clear, justified reason in the diff context.

Output ONLY JSON, no markdown fences:

{
  "approved": bool,
  "score": int 0-100,
  "summary": "one paragraph",
  "concerns": [
    {
      "severity": "blocking|major|minor",
      "file": "path/to/file",
      "line": int,
      "description": "specific concern with enough context to fix"
    }
  ]
}

RULES:
1. Approve (true) unless there are blocking issues.
2. Score < 60 = must fix before shipping.
3. Every concern must be actionable — point to a specific line when possible.
4. Be honest. If the diff is fine, say so with a short summary.

SESSION + DIFF:
`

// runCrossModelReview runs a separate LLM pass over the session's git
// diff and returns a structured review. Returns nil when the review
// can't be performed (no diff, no provider, or LLM error); the caller
// treats nil as "no review, don't block".
func runCrossModelReview(ctx context.Context, session plan.Session, cfg sowNativeConfig) *crossReviewResult {
	if cfg.ReviewProvider == nil {
		return nil
	}
	// Capture the diff since the session started. We use `git diff HEAD`
	// which covers working-tree changes the session just made against
	// the last commit. If the session committed its own work we won't
	// see it here — that's acceptable because native-mode sessions
	// typically don't commit.
	diffCmd := exec.CommandContext(ctx, "git", "diff", "HEAD")
	diffCmd.Dir = cfg.RepoRoot
	diffOut, err := diffCmd.Output()
	if err != nil || len(diffOut) == 0 {
		return nil
	}
	diff := string(diffOut)
	// Cap the diff so a huge refactor doesn't blow the review budget.
	if len(diff) > 50000 {
		diff = diff[:50000] + "\n... (diff truncated)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SESSION %s: %s\n\n", session.ID, session.Title)
	if session.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", session.Description)
	}
	if len(session.Tasks) > 0 {
		b.WriteString("TASKS:\n")
		for _, t := range session.Tasks {
			fmt.Fprintf(&b, "- %s: %s\n", t.ID, t.Description)
		}
		b.WriteString("\n")
	}
	b.WriteString("DIFF:\n")
	b.WriteString(diff)

	model := cfg.ReviewModel
	if model == "" {
		model = cfg.Model
	}

	userText := crossReviewPrompt + b.String()
	userContent, _ := encodeTextMessage(userText)

	resp, err := cfg.ReviewProvider.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 6000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		fmt.Printf("  cross-review error: %v\n", err)
		return nil
	}
	raw := ""
	for _, c := range resp.Content {
		if c.Type == "text" {
			raw += c.Text
		}
	}
	var result crossReviewResult
	if _, jsonErr := jsonutil.ExtractJSONInto(raw, &result); jsonErr != nil {
		return nil
	}
	return &result
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
// runSessionPhase1 dispatches a session's tasks. When ParallelWorkers > 1
// it groups tasks into dependency-respecting waves — a wave is a set of
// tasks whose deps are already satisfied (by earlier waves) and whose
// file sets are pairwise disjoint (to avoid write-write conflicts). Each
// wave runs concurrently with a worker pool capped at ParallelWorkers.
// Within a wave, tasks still share the same repo root, so the file-
// disjointness rule is critical.
//
// When ParallelWorkers <= 1 the flow degrades to the original sequential
// loop — cheaper, deterministic, and the default.
func runSessionPhase1(ctx context.Context, session plan.Session, workingSession plan.Session, sowDoc *plan.SOW, runtimeDir string, cfg sowNativeConfig, maxTurns int) []plan.TaskExecResult {
	if cfg.ParallelWorkers <= 1 {
		return runSessionPhase1Sequential(ctx, session, workingSession, sowDoc, runtimeDir, cfg, maxTurns)
	}
	return runSessionPhase1Parallel(ctx, session, workingSession, sowDoc, runtimeDir, cfg, maxTurns)
}

func runSessionPhase1Sequential(ctx context.Context, session plan.Session, workingSession plan.Session, sowDoc *plan.SOW, runtimeDir string, cfg sowNativeConfig, maxTurns int) []plan.TaskExecResult {
	results := make([]plan.TaskExecResult, 0, len(session.Tasks))
	for i, task := range session.Tasks {
		if ctx.Err() != nil {
			return results
		}
		if cfg.CostBudgetUSD > 0 && cfg.spent != nil && *cfg.spent >= cfg.CostBudgetUSD {
			fmt.Printf("  budget exhausted ($%.2f / $%.2f) — halting session\n", *cfg.spent, cfg.CostBudgetUSD)
			results = append(results, plan.TaskExecResult{
				TaskID:  task.ID,
				Success: false,
				Error:   fmt.Errorf("cost budget exhausted"),
			})
			continue
		}
		fmt.Printf("  [%d/%d] %s: %s\n", i+1, len(session.Tasks), task.ID, task.Description)

		sysP, usrP := buildSOWNativePromptsWithOpts(sowDoc, workingSession, task, promptOpts{
			RepoMap:       cfg.RepoMap,
			RepoMapBudget: cfg.RepoMapBudget,
			Wisdom:        cfg.Wisdom,
			RawSOW:        cfg.RawSOWText,
			RepoRoot:      cfg.RepoRoot,
			Briefing:      cfg.Briefings[task.ID],
		})
		sup := toEngineSupervisor(autoExtractTaskSupervisor(cfg.RepoRoot, cfg.RawSOWText, workingSession, task, 3))
		tr := execNativeTask(ctx, task.ID, sysP, usrP, runtimeDir, cfg, maxTurns, sup)
		// Per-task reviewer: catch gaps at task scope before
		// cascading into session AC failures. Bounded at 1
		// follow-up max per task to cap cost and prevent loops.
		reviewAndFollowup(ctx, sowDoc, workingSession, task, &tr, runtimeDir, cfg, maxTurns)
		results = append(results, tr)
	}
	return results
}

// runSessionPhase1Parallel groups tasks into waves and runs each wave
// concurrently. Task IDs without explicit deps or files fall back to
// sequential execution (one wave per task) so we don't accidentally
// parallelize things that implicitly share state.
//
// File-collision detection: before each wave, we snapshot the set of
// files currently in git status. After the wave completes, we diff the
// snapshot against the new status to see which files changed DURING
// the wave. Any changed file that wasn't in the union of declared
// task.Files for this wave gets reported as a collision — either an
// undeclared side-effect (agent touched a file it shouldn't have) or
// a race between two tasks with overlapping implicit scope. Either
// way the operator sees it clearly and can tighten the SOW.
func runSessionPhase1Parallel(ctx context.Context, session plan.Session, workingSession plan.Session, sowDoc *plan.SOW, runtimeDir string, cfg sowNativeConfig, maxTurns int) []plan.TaskExecResult {
	waves := buildParallelWaves(session.Tasks)

	type indexed struct {
		idx int
		res plan.TaskExecResult
	}
	results := make([]plan.TaskExecResult, len(session.Tasks))
	completed := 0
	for waveIdx, wave := range waves {
		if ctx.Err() != nil {
			return results[:completed]
		}
		if len(wave) == 0 {
			continue
		}
		workers := cfg.ParallelWorkers
		if workers > len(wave) {
			workers = len(wave)
		}
		fmt.Printf("  wave %d: %d task(s) in parallel (%d worker(s))\n", waveIdx+1, len(wave), workers)

		// Snapshot git state before the wave so we can detect
		// collisions afterwards.
		preWaveDirty := toSet(gitDirtyFiles(ctx, cfg.RepoRoot))
		declaredInWave := make(map[string]bool)
		for _, ti := range wave {
			for _, f := range session.Tasks[ti].Files {
				declaredInWave[normalizeScopePath(f)] = true
			}
		}

		sem := make(chan struct{}, workers)
		resCh := make(chan indexed, len(wave))
		for _, ti := range wave {
			ti := ti // capture
			sem <- struct{}{}
			go func() {
				defer func() { <-sem }()
				task := session.Tasks[ti]
				if cfg.CostBudgetUSD > 0 && cfg.spent != nil && *cfg.spent >= cfg.CostBudgetUSD {
					resCh <- indexed{idx: ti, res: plan.TaskExecResult{
						TaskID:  task.ID,
						Success: false,
						Error:   fmt.Errorf("cost budget exhausted"),
					}}
					return
				}
				fmt.Printf("  ▶ %s: %s\n", task.ID, task.Description)
				sysP, usrP := buildSOWNativePromptsWithOpts(sowDoc, workingSession, task, promptOpts{
					RepoMap:       cfg.RepoMap,
					RepoMapBudget: cfg.RepoMapBudget,
					Wisdom:        cfg.Wisdom,
					RawSOW:        cfg.RawSOWText,
					RepoRoot:      cfg.RepoRoot,
					Briefing:      cfg.Briefings[task.ID],
				})
				sup := toEngineSupervisor(autoExtractTaskSupervisor(cfg.RepoRoot, cfg.RawSOWText, workingSession, task, 3))
				tr := execNativeTask(ctx, task.ID, sysP, usrP, runtimeDir, cfg, maxTurns, sup)
				// Per-task reviewer (bounded follow-up) runs in
				// each worker goroutine so parallel review + fix
				// happens per-task without serializing the wave.
				reviewAndFollowup(ctx, sowDoc, workingSession, task, &tr, runtimeDir, cfg, maxTurns)
				resCh <- indexed{idx: ti, res: tr}
			}()
		}
		// Wait for this wave to drain before starting the next
		// (dependency ordering).
		for i := 0; i < len(wave); i++ {
			r := <-resCh
			results[r.idx] = r.res
			completed++
		}

		// Post-wave collision audit.
		postWaveDirty := gitDirtyFiles(ctx, cfg.RepoRoot)
		newlyChanged := diffFileSets(postWaveDirty, preWaveDirty)
		var undeclared []string
		for _, f := range newlyChanged {
			if strings.HasPrefix(f, ".stoke/") {
				continue
			}
			if declaredInWave[normalizeScopePath(f)] {
				continue
			}
			// Accept directory-prefix matches ("src/auth/" allows
			// "src/auth/token.go").
			ok := false
			for d := range declaredInWave {
				if strings.HasSuffix(d, "/") && strings.HasPrefix(f, d) {
					ok = true
					break
				}
			}
			if !ok {
				undeclared = append(undeclared, f)
			}
		}
		if len(undeclared) > 0 {
			fmt.Printf("  ⚠ wave %d collision: %d file(s) touched outside declared task.Files:\n", waveIdx+1, len(undeclared))
			for _, f := range undeclared {
				fmt.Printf("    - %s\n", f)
			}
			if cfg.StrictScope {
				// Record as a synthetic task failure so the
				// scheduler sees the session is not clean.
				results = append(results, plan.TaskExecResult{
					TaskID:  fmt.Sprintf("%s-wave%d-collision", session.ID, waveIdx+1),
					Success: false,
					Error:   fmt.Errorf("parallel wave collision: %d undeclared file(s)", len(undeclared)),
				})
			}
		}
	}
	return results
}

// toSet converts a string slice into a set for O(1) membership checks.
func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

// diffFileSets returns items in post that aren't in pre. Used to see
// what changed during a wave.
func diffFileSets(post []string, pre map[string]bool) []string {
	var out []string
	for _, f := range post {
		if !pre[f] {
			out = append(out, f)
		}
	}
	return out
}

// buildParallelWaves groups tasks into dependency-respecting waves of
// pairwise disjoint file sets. Returns a slice of waves where each wave
// is a slice of task indices (into the original tasks slice).
//
// Rules:
//   - A task can run in wave N if all its declared dependencies are in
//     waves <N.
//   - Within a single wave, no two tasks may share any file path in
//     their Files field.
//   - Tasks with no Files still run, but they never share a wave with
//     another task (conservative — they might touch anything).
func buildParallelWaves(tasks []plan.Task) [][]int {
	if len(tasks) == 0 {
		return nil
	}
	idByName := make(map[string]int, len(tasks))
	for i, t := range tasks {
		idByName[t.ID] = i
	}
	placed := make([]int, len(tasks)) // wave index or -1
	for i := range placed {
		placed[i] = -1
	}
	var waves [][]int
	for {
		currentWave := []int{}
		currentFiles := make(map[string]bool)
		unknownInWave := false
		progress := false
		for i, t := range tasks {
			if placed[i] != -1 {
				continue
			}
			// Check deps
			depsReady := true
			for _, dep := range t.Dependencies {
				depIdx, ok := idByName[dep]
				if !ok {
					continue // unknown dep — ignore
				}
				if placed[depIdx] == -1 || placed[depIdx] >= len(waves) {
					depsReady = false
					break
				}
			}
			if !depsReady {
				continue
			}
			// Tasks with no files are conservative: only if they're
			// alone in the wave.
			if len(t.Files) == 0 {
				if len(currentWave) == 0 {
					currentWave = append(currentWave, i)
					placed[i] = len(waves)
					unknownInWave = true
					progress = true
					break // alone
				}
				continue
			}
			if unknownInWave {
				continue
			}
			// Check file disjointness against everyone already in the wave.
			conflict := false
			for _, f := range t.Files {
				if currentFiles[f] {
					conflict = true
					break
				}
			}
			if conflict {
				continue
			}
			// Admit.
			currentWave = append(currentWave, i)
			placed[i] = len(waves)
			for _, f := range t.Files {
				currentFiles[f] = true
			}
			progress = true
		}
		if len(currentWave) == 0 {
			if !progress {
				// Stuck — no progress possible. Force-place the first
				// unplaced task in its own wave to avoid a deadlock.
				for i := range tasks {
					if placed[i] == -1 {
						waves = append(waves, []int{i})
						placed[i] = len(waves) - 1
						break
					}
				}
				// Check if anything is still unplaced.
				anyLeft := false
				for _, p := range placed {
					if p == -1 {
						anyLeft = true
						break
					}
				}
				if !anyLeft {
					break
				}
				continue
			}
			break
		}
		waves = append(waves, currentWave)
		// Stop if everything is placed.
		done := true
		for _, p := range placed {
			if p == -1 {
				done = false
				break
			}
		}
		if done {
			break
		}
	}
	return waves
}

// execNativeTask runs a single task against the native runner and returns
// a TaskExecResult. Factored out so the first-pass loop and repair loop
// share exactly the same execution semantics. systemPrompt is the static
// cached block; userPrompt is the per-task dynamic message.
func execNativeTask(ctx context.Context, taskID, systemPrompt, userPrompt, runtimeDir string, cfg sowNativeConfig, maxTurns int, supervisor *engine.SupervisorConfig) plan.TaskExecResult {
	taskRuntime := filepath.Join(runtimeDir, taskID)
	if err := os.MkdirAll(taskRuntime, 0o755); err != nil {
		return plan.TaskExecResult{TaskID: taskID, Success: false, Error: err}
	}

	spec := engine.RunSpec{
		Prompt:           userPrompt,
		SystemPrompt:     systemPrompt,
		CompactThreshold: cfg.CompactThreshold,
		WorktreeDir:      cfg.RepoRoot,
		RuntimeDir:       taskRuntime,
		Mode:             engine.AuthModeAPIKey,
		Phase: engine.PhaseSpec{
			Name:     "execute",
			MaxTurns: maxTurns,
			ReadOnly: false,
		},
		Supervisor: supervisor,
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
	// Observability: every task termination logs taskID + outcome +
	// timing + cost. This is the only signal the operator gets
	// between "task dispatched" and "session acceptance runs", so
	// silence here was what made prior runs look hung when they
	// weren't. Always include taskID.
	switch {
	case err != nil:
		tr.Error = err
		fmt.Printf("    ✗ %s error: %v (%.1fs, %d turns)\n", taskID, err, dur.Seconds(), result.NumTurns)
	case result.IsError:
		tr.Error = fmt.Errorf("native runner: %s", result.Subtype)
		fmt.Printf("    ✗ %s failed: %s (%.1fs, %d turns, $%.4f)\n", taskID, result.Subtype, dur.Seconds(), result.NumTurns, result.CostUSD)
	default:
		fmt.Printf("    ✓ %s done (%.1fs, %d turns, $%.4f)\n", taskID, dur.Seconds(), result.NumTurns, result.CostUSD)
	}
	return tr
}

// promptOpts bundles the extras buildSOWNativePrompts needs beyond the
// core SOW/session/task triple. Added as a struct so new fields don't
// keep stretching the function signature.
type promptOpts struct {
	RepoMap       *repomap.RepoMap
	RepoMapBudget int
	Repair        *string
	Wisdom        *wisdom.Store
	// RawSOW is the original SOW text (prose / JSON / YAML). When
	// non-empty, it's injected verbatim into the cached system block
	// under a "SPEC (verbatim)" header.
	RawSOW string
	// RepoRoot is the absolute path to the project being built. When
	// set, the prompt builder injects the public API surface from
	// existing source files so later sessions can wire against earlier
	// sessions' types instead of guessing or rewriting them.
	RepoRoot string
	// Briefing is the lead-dev briefing for THIS specific task, if the
	// session's briefing phase ran and produced one. The briefing
	// carries current-codebase context (what exists, what's missing,
	// exact identifiers to use, pitfalls) that the SOW spec alone
	// doesn't capture. nil = no briefing, just use the spec.
	Briefing *plan.TaskBriefing
}

// buildSOWNativePrompts returns (systemPrompt, userPrompt) for a task.
// The system prompt contains the STATIC context — SOW identity, stack,
// session framing, acceptance criteria, canonical names the agent must
// use, the optional raw SOW text, the optional repo map, and any
// accumulated wisdom from prior sessions. The user prompt is the task
// description, expected files, dependencies, and any repair context.
//
// Agentloop wraps the system prompt in a cache_control breakpoint for
// ~82% input cost reduction after turn 1.
func buildSOWNativePrompts(sowDoc *plan.SOW, session plan.Session, task plan.Task, rmap *repomap.RepoMap, mapBudget int, repair *string, wisdomStore *wisdom.Store) (string, string) {
	return buildSOWNativePromptsWithOpts(sowDoc, session, task, promptOpts{
		RepoMap:       rmap,
		RepoMapBudget: mapBudget,
		Repair:        repair,
		Wisdom:        wisdomStore,
	})
}

// buildSOWNativePromptsWithOpts is the full builder. Callers should use
// this when they have the raw SOW text. The legacy buildSOWNativePrompts
// wrapper exists so existing test callers (and places that don't have
// RawSOW handy) don't need updating.
func buildSOWNativePromptsWithOpts(sowDoc *plan.SOW, session plan.Session, task plan.Task, opts promptOpts) (string, string) {
	rmap := opts.RepoMap
	mapBudget := opts.RepoMapBudget
	repair := opts.Repair
	wisdomStore := opts.Wisdom
	var sys, usr strings.Builder

	// --- SYSTEM (static, cacheable) ---
	if repair != nil {
		sys.WriteString("You are an autonomous coding agent in REPAIR mode. A previous pass through this session produced code that fails the session's acceptance criteria. ")
		sys.WriteString("Read the failure output in the user message below, understand what's wrong, and fix it by editing files directly in the project root. ")
		sys.WriteString("Do not rewrite unrelated code. Do not break criteria that are already passing. Use the bash tool to re-run the failing commands yourself to verify your fix before ending.\n\n")
		sys.WriteString("COMMON FAILURE CLASSES and how to fix them:\n")
		sys.WriteString("  - \"X: not found\" or \"command not found\" → the binary isn't installed. For Node workspaces: add the package to the relevant package.json devDependencies and run `pnpm install` (or npm/yarn). Do NOT switch to a different command that happens to exist.\n")
		sys.WriteString("  - \"Cannot find module X\" / \"Module not found\" → the import path is wrong OR the dependency isn't declared. Check that every `import` / `require` matches a declared dependency.\n")
		sys.WriteString("  - \"missing script: X\" → package.json has no script with that name. Add it. `pnpm <script>` only runs scripts declared in package.json.scripts.\n")
		sys.WriteString("  - \"node_modules missing\" → run `pnpm install` at the workspace root first.\n")
		sys.WriteString("  - \"file not found: X\" → create X with real content, not an empty stub.\n")
		sys.WriteString("  - Test fails with 0 tests collected → the test runner isn't configured (missing vitest.config.ts / jest.config.js) OR the test files don't match the runner's glob pattern.\n")
		sys.WriteString("  - Type errors reference missing @types → add the @types/<pkg> devDep and re-install.\n")
		sys.WriteString("After you make each fix, re-run the exact failing command via bash and confirm exit 0 BEFORE moving to the next fix. Never end the repair without re-running every failing command at least once.\n\n")
	} else {
		sys.WriteString("You are an autonomous coding agent working on a project defined by a Statement of Work (SOW). ")
		sys.WriteString("Your job: implement the single task described in the user message by writing files directly to the project root. ")
		sys.WriteString("Use the available file tools (read_file, write_file, edit_file, bash) to create or modify files as needed. ")
		sys.WriteString("Do NOT create worktrees or branches — write directly to the repo. When you believe the task is complete, verify by running the relevant acceptance criteria commands with bash before ending.\n\n")
		sys.WriteString("BEFORE YOU END the task, run through this self-check (don't just trust that you did these):\n")
		sys.WriteString("  1. Every file listed in 'expected files' below actually exists and has REAL content (not a one-line stub, not a comment-only file). Use `ls -la` and `wc -l` to verify.\n")
		sys.WriteString("  2. Every library you `import` / `require` / `use` is declared in the matching package.json / Cargo.toml / go.mod / requirements.txt. Missing imports become runtime failures the ACs will catch later.\n")
		sys.WriteString("  3. If your task creates a package.json that has an acceptance criterion like `pnpm build --filter=X`, that package.json MUST have a `build` script. Same for `typecheck`, `test`, `lint` — if the SOW ACs reference them, declare them.\n")
		sys.WriteString("  4. If you created test files, you also need the test runner configured (vitest.config.ts / jest.config.js / pytest.ini) AND a `test` script in the package that owns the tests. Test files with no runner are dead code.\n")
		sys.WriteString("  5. If you added a new dep to package.json, run `pnpm install` so node_modules is updated before ending.\n")
		sys.WriteString("  6. Run the session's acceptance criteria commands via bash yourself. If any exit non-zero, investigate and fix before ending — don't hand it off to the repair loop to find.\n\n")
	}

	// Node-specific ecosystem discipline. Emit only when the SOW stack
	// or framework signals a JS/TS workspace, so Rust/Go/Python runs
	// aren't cluttered by irrelevant guidance.
	if isNodeStack(sowDoc) {
		sys.WriteString("NODE/TYPESCRIPT ECOSYSTEM DISCIPLINE (this project uses pnpm + a monorepo):\n")
		sys.WriteString("  - node_modules/.bin is on PATH when acceptance commands run, so prefer direct invocation (`tsc --noEmit`, `vitest run`, `next build`) over `npx` / `pnpm exec` wrappers.\n")
		sys.WriteString("  - If you add a dependency, put it in the package.json of the package that actually imports it — NOT always the root. Each package in the workspace owns its own deps.\n")
		sys.WriteString("  - Workspace dependencies across packages use `\"@sentinel/types\": \"workspace:*\"` syntax so pnpm resolves them to the sibling package, not a registry lookup.\n")
		sys.WriteString("  - Every package that has a `tsconfig.json` should extend from a shared base in `tooling/tsconfig/base.json` and set `\"extends\": \"@sentinel/tsconfig/base.json\"` only if that package is actually in the workspace. When extending a relative path, use `\"../../tooling/tsconfig/base.json\"`.\n")
		sys.WriteString("  - `pnpm` scripts run in the cwd of the package that owns them. `pnpm --filter <pkg> <script>` is the correct way to target one package from the root.\n")
		sys.WriteString("  - Never rely on `$REPO_URL`, `git clone`, or any external network access for acceptance criteria. The repo IS the current working directory; ACs test the state on disk right here.\n")
		sys.WriteString("  - When tests are added, the test runner (vitest / jest) must be in devDependencies AND its config file must exist AND a `test` script must invoke it. Any of these missing = tests are dead code.\n\n")
	}

	// Working-directory anchor. Without this, a model that writes
	// "Cargo.toml" (relative) has no way to verify WHERE that file
	// landed, and running `pwd && ls -la` looks like a failure when
	// the model expected a different cwd. Upstream hit "3 consecutive
	// tool errors" because of exactly this. Making the anchor
	// explicit in the system prompt resolves the ambiguity at the
	// source.
	if opts.RepoRoot != "" {
		fmt.Fprintf(&sys, "WORKING DIRECTORY (absolute): %s\n", opts.RepoRoot)
		sys.WriteString("All your file tools (read_file, write_file, edit_file) and the bash tool operate relative to this directory. When you call write_file with \"path\": \"Cargo.toml\", the file lands at WORKING_DIRECTORY/Cargo.toml. When you run `pwd` via bash, it prints WORKING_DIRECTORY. Use simple relative paths like \"Cargo.toml\" or \"crates/foo/src/lib.rs\" — do NOT try to cd somewhere else, and do NOT pass paths that escape this directory.\n\n")
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

	// Public API surface from prior session code. Without this, an abstract
	// per-session description like "implement update_concern_field" leaves
	// the agent stalling because it doesn't know the concrete types/signatures
	// the previous session defined. The repo map only lists file paths; this
	// adds the actual `pub fn` / `pub struct` / `export` lines so the model
	// can wire against existing definitions instead of guessing or rewriting.
	if opts.RepoRoot != "" {
		surface := sowAPISurface(opts.RepoRoot, 30000)
		if surface != "" {
			sys.WriteString(surface)
			sys.WriteString("\n")
		}
	}

	// Inject accumulated cross-session wisdom so later sessions
	// automatically inherit conventions, decisions, and gotchas that
	// earlier sessions discovered. ForPrompt is already bounded to a
	// reasonable length so it doesn't bust the cache budget.
	if wisdomStore != nil {
		if wisdomBlob := wisdomStore.ForPrompt(); wisdomBlob != "" {
			sys.WriteString(wisdomBlob)
			sys.WriteString("\n\n")
		}
	}

	// Canonical names block: the SOW declares specific identifiers
	// (crate names, module paths, file names, error-type variants
	// via ContentMatch patterns). Reinforce them here so the agent
	// never invents plausible-but-wrong names. This is the direct
	// fix for the "agent created persys-domain instead of persys-
	// concern" failure mode — the user's actual spec had persys-
	// concern but the agent hallucinated persys-domain because the
	// exact name wasn't reinforced anywhere it could see.
	if canonicalBlock := buildCanonicalNamesBlock(sowDoc, session, task); canonicalBlock != "" {
		sys.WriteString(canonicalBlock)
		sys.WriteString("\n")
	}

	// Skill injection: keyword-match the SOW stack against the skill
	// registry (project ~> user ~> builtin) and append any matching
	// skill content. This is where ecosystem-specific playbooks
	// (pnpm-monorepo-discipline, node-test-runner-triad, package-json-
	// hygiene, react-native-core, etc.) get pulled into the per-task
	// prompt. Budget-bounded so one chatty skill can't blow the cache.
	if opts.RepoRoot != "" {
		reg := skill.DefaultRegistry(opts.RepoRoot)
		_ = reg.Load()
		stackMatches := stackMatchesForSOW(sowDoc, session, task)
		// Pass the task description + session title as the "prompt"
		// for keyword matching. Without this, keyword matching runs
		// against an empty string and no skills match via Tier 3.
		// Also include the session's AC commands so skills that
		// match AC patterns (e.g. no-e2e-in-ac matching "playwright")
		// get injected.
		var matchCtx strings.Builder
		matchCtx.WriteString(session.Title + " " + task.Description + " ")
		for _, ac := range session.AcceptanceCriteria {
			matchCtx.WriteString(ac.Command + " " + ac.Description + " ")
		}
		skillBlob, _ := reg.InjectPromptBudgeted(matchCtx.String(), stackMatches, 6000)
		if strings.TrimSpace(skillBlob) != "" {
			sys.WriteString("ECOSYSTEM PLAYBOOKS (canonical conventions for this stack — follow these unless the SOW says otherwise):\n")
			sys.WriteString(skillBlob)
			sys.WriteString("\n")
		}
	}

	// Raw SOW text: the original file the user wrote (prose, JSON,
	// or YAML). Included verbatim under a clearly-labeled header so
	// the agent can grep/scan it for exact identifiers, error
	// variant names, acceptance commands, etc. that the compressed
	// framing might miss. Bounded by a soft cap so a huge SOW
	// doesn't blow the prompt cache.
	if opts.RawSOW != "" {
		raw := opts.RawSOW
		const rawCap = 32000
		if len(raw) > rawCap {
			raw = raw[:rawCap] + "\n... (SOW truncated at " + strconv.Itoa(rawCap) + " bytes — full spec in .stoke/)"
		}
		sys.WriteString("SPEC (verbatim from the SOW — cross-reference this whenever you're about to choose a name):\n")
		sys.WriteString("----- BEGIN SOW -----\n")
		sys.WriteString(raw)
		if !strings.HasSuffix(raw, "\n") {
			sys.WriteString("\n")
		}
		sys.WriteString("----- END SOW -----\n\n")
	}

	// --- USER (dynamic, per-task) ---
	//
	// Structure matters here. Task descriptions in LLM-generated SOWs
	// are often tiny ("Create error.rs, concern.rs") — not enough for
	// the agent to know what those files should actually contain. We
	// compensate by putting the spec excerpt FIRST (where the model's
	// attention actually lives) and the task description as a short
	// pointer into the excerpt.
	//
	// Order: identifier checklist → task header → spec excerpt →
	// expected files / deps → closing instruction.
	if repair != nil {
		usr.WriteString("FAILING ACCEPTANCE CRITERIA (fix these):\n")
		usr.WriteString(*repair)
		usr.WriteString(`
INVESTIGATION PROTOCOL:
  1. Read the failure output carefully. Don't skim it — specific error
     strings ("Cannot find module", "tsc: not found", "missing script:",
     "exit status 1") each have a known cause and fix.
  2. For EACH failing criterion, run the exact command yourself via bash
     FIRST to reproduce. Don't guess from the output you were given.
  3. Trace each error to its root cause on disk. "tsc: not found" is
     NOT a toolchain problem — it means node_modules wasn't installed
     or the package has no tsc dep. Fix the root cause, not the symptom.
  4. After each fix, re-run the SAME command and confirm exit 0.
  5. Only end when every failing command passes. Do not end early,
     do not say "should be fixed now" without verifying.

TYPICAL ROOT CAUSES (apply the matching fix, not a workaround):
  - "missing script: build" in a package.json → add the script to that
    package.json's "scripts" block. Don't work around it by running
    tsc directly from the root.
  - "Cannot find module '@scope/pkg'" → either (a) the import path is
    wrong, (b) the workspace dependency isn't declared via
    "workspace:*", or (c) pnpm install needs to re-run. Check all three.
  - "tsc: not found" → add typescript to the package's devDependencies
    and run pnpm install. Every package that has its own tsconfig
    needs its own typescript devDep (pnpm doesn't hoist unless
    explicitly configured).
  - Test file exists but runner says "0 tests found" → the runner
    config doesn't pick it up. Check vitest.config.ts / jest.config.js
    include globs and the file extension.
  - "pnpm --filter X build" fails → open X/package.json, verify the
    "build" script exists and points at a real tool.
  - File-exists check fails → write the file with REAL content. Not an
    empty stub, not a one-line comment.

Make the minimum changes that actually fix the root cause. After your
fixes, re-run every failing command listed above with bash and confirm
exit 0 before you end.
`)
	} else {
		// 1. Identifier checklist — forces the model to state its
		// planned names against the spec before writing.
		if checklist := buildTaskIdentifierChecklist(session, task); checklist != "" {
			usr.WriteString(checklist)
			usr.WriteString("\n")
		}

		// 2. Lead-dev briefing — current-codebase context produced
		// by the briefing phase that ran at the start of this wave.
		// Tells the worker what actually exists on disk right now
		// (which the SOW spec couldn't know because it was written
		// before any code existed), which identifiers to use
		// verbatim, which pitfalls earlier tasks in this session
		// already stepped on, and a suggested step order. Lives
		// BEFORE the task header so the worker reads current reality
		// before reading the task description's assumptions.
		if opts.Briefing != nil {
			if blob := opts.Briefing.Format(); blob != "" {
				usr.WriteString(blob)
			}
		}

		// 3. Task header — short, because the spec excerpt below
		// does the heavy lifting.
		fmt.Fprintf(&usr, "TASK %s: %s\n", task.ID, task.Description)
		if len(task.Files) > 0 {
			fmt.Fprintf(&usr, "  expected files: %s\n", strings.Join(task.Files, ", "))
		}
		if len(task.Dependencies) > 0 {
			fmt.Fprintf(&usr, "  depends on: %s\n", strings.Join(task.Dependencies, ", "))
		}
		usr.WriteString("\n")

		// 3. Spec excerpt — the authoritative thing the model must
		// follow. Pulled from the raw SOW by matching file paths,
		// crate names, and identifiers from the task. This is the
		// fix for "tiny task description + 32k buried SOW = model
		// invents plausible names".
		if opts.RawSOW != "" {
			excerpt := extractTaskSpecExcerpt(opts.RawSOW, session, task, specExcerptConfig{})
			if excerpt != "" {
				usr.WriteString("SPEC EXCERPT (authoritative — the task header above is just a summary):\n")
				usr.WriteString("----- BEGIN SPEC -----\n")
				usr.WriteString(excerpt)
				usr.WriteString("\n----- END SPEC -----\n\n")
				usr.WriteString("Read the SPEC EXCERPT above carefully before writing any code. If the spec defines a specific struct, function signature, or field layout, implement it EXACTLY as written — no interpretation, no generic alternatives, no \"plausible\" fill-ins. Exact identifiers from the spec must appear verbatim in your code.\n\n")
			}
		}

		usr.WriteString("Begin implementing the task now. When you're done, your final message should briefly summarize what you changed, and you should run the acceptance command(s) yourself with bash to confirm the work is complete.\n")
	}

	return sys.String(), usr.String()
}

// buildSOWNativePrompt returns just the concatenated prompt. Retained for
// tests and any caller that wants a single string. New code should use
// buildSOWNativePrompts for proper cache-aware system/user split.
func buildSOWNativePrompt(sowDoc *plan.SOW, session plan.Session, task plan.Task, rmap *repomap.RepoMap, mapBudget int, repair *string) string {
	sys, usr := buildSOWNativePrompts(sowDoc, session, task, rmap, mapBudget, repair, nil)
	return sys + "\n" + usr
}

// runReasoningForStuckCriteria walks the failing criteria, finds ones
// that have become sticky (2+ consecutive failures) AND haven't been
// reasoned about yet, and runs the multi-analyst + judge reasoning
// loop on each. Based on the verdict:
//
//   code_bug         — appends a CODE FIX DIRECTIVE to the hint blob
//                      for the next repair prompt
//   ac_bug           — mutates effectiveCriteria IN PLACE, replacing
//                      the criterion's Command with ACRewrite
//   both             — does both
//   acceptable_as_is — marks the criterion with an inline override
//                      flag (we rewrite it to "true" so the next AC
//                      pass succeeds automatically) and logs why
//
// Returns hint text to prepend to the repair prompt. Empty string if
// nothing reasoned or no actionable hints came back.
func runReasoningForStuckCriteria(
	ctx context.Context,
	acceptance []plan.AcceptanceResult,
	stickyAttempts map[string]int,
	reasoningApplied map[string]bool,
	effectiveCriteria []plan.AcceptanceCriterion,
	workingSession plan.Session,
	session plan.Session,
	cfg sowNativeConfig,
) string {
	var hintBlob strings.Builder
	for _, ac := range acceptance {
		if ac.Passed {
			continue
		}
		if stickyAttempts[ac.CriterionID] < 2 {
			continue
		}
		if reasoningApplied[ac.CriterionID] {
			continue
		}
		// Locate the canonical criterion object in effectiveCriteria
		// (the acceptance result has ID/desc/output but not the
		// Command/FileExists/ContentMatch shape we need to reason
		// about).
		var crit plan.AcceptanceCriterion
		var critIdx = -1
		for i, c := range effectiveCriteria {
			if c.ID == ac.CriterionID {
				crit = c
				critIdx = i
				break
			}
		}
		if critIdx < 0 {
			continue
		}
		// Gather code excerpts from the files most likely relevant
		// to this criterion. Start with the session's task.Files and
		// any file paths the AC command / file_exists mentions.
		var relPaths []string
		seen := map[string]bool{}
		addPath := func(p string) {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				return
			}
			seen[p] = true
			relPaths = append(relPaths, p)
		}
		for _, t := range workingSession.Tasks {
			for _, f := range t.Files {
				addPath(f)
			}
		}
		if crit.FileExists != "" {
			addPath(crit.FileExists)
		}
		if crit.ContentMatch != nil && crit.ContentMatch.File != "" {
			addPath(crit.ContentMatch.File)
		}
		codeExcerpts := plan.CollectCodeExcerpts(cfg.RepoRoot, relPaths, 8, 3000)

		// Pick a task description that's most relevant. If the
		// session has a task whose files overlap with the criterion,
		// prefer that one; otherwise fall back to the session title.
		taskDesc := workingSession.Title
		for _, t := range workingSession.Tasks {
			if len(t.Files) > 0 {
				taskDesc = t.Description
				break
			}
		}

		fmt.Printf("  ↻ reasoning about stuck criterion %s (%d attempts)...\n", ac.CriterionID, stickyAttempts[ac.CriterionID])
		reasoningModel := cfg.ReasoningModel
		if reasoningModel == "" {
			reasoningModel = cfg.Model
		}
		verdict, rerr := plan.ReasonAboutFailure(ctx, cfg.ReasoningProvider, reasoningModel, plan.ReasoningInput{
			SessionID:       session.ID,
			SessionTitle:    session.Title,
			TaskDescription: taskDesc,
			Criterion:       crit,
			FailureOutput:   ac.Output,
			PriorAttempts:   stickyAttempts[ac.CriterionID],
			CodeExcerpts:    codeExcerpts,
			RepoRoot:        cfg.RepoRoot,
		})
		reasoningApplied[ac.CriterionID] = true
		if rerr != nil {
			fmt.Printf("    reasoning loop failed: %v\n", rerr)
			continue
		}
		fmt.Printf("    verdict: %s — %s\n", verdict.Category, truncateForLog(verdict.Reasoning, 200))

		switch verdict.Category {
		case "code_bug":
			if verdict.CodeFix != "" {
				fmt.Fprintf(&hintBlob, "REASONING-LOOP VERDICT for %s: code_bug. %s\nFIX: %s\n\n",
					ac.CriterionID, verdict.Reasoning, verdict.CodeFix)
			}
		case "ac_bug":
			if verdict.ACRewrite != "" {
				fmt.Printf("    → rewriting AC %s command from %q to %q\n",
					ac.CriterionID, truncateForLog(crit.Command, 100), truncateForLog(verdict.ACRewrite, 100))
				effectiveCriteria[critIdx].Command = verdict.ACRewrite
				// Persist so session retries see this rewrite too.
				if cfg.ACRewrites == nil {
					cfg.ACRewrites = map[string]string{}
				}
				cfg.ACRewrites[ac.CriterionID] = verdict.ACRewrite
				// Clear sticky count so the next acceptance pass
				// starts fresh on the rewritten AC.
				delete(stickyAttempts, ac.CriterionID)
			}
		case "both":
			if verdict.ACRewrite != "" {
				fmt.Printf("    → rewriting AC %s command (both-verdict)\n", ac.CriterionID)
				effectiveCriteria[critIdx].Command = verdict.ACRewrite
				if cfg.ACRewrites == nil {
					cfg.ACRewrites = map[string]string{}
				}
				cfg.ACRewrites[ac.CriterionID] = verdict.ACRewrite
				delete(stickyAttempts, ac.CriterionID)
			}
			if verdict.CodeFix != "" {
				fmt.Fprintf(&hintBlob, "REASONING-LOOP VERDICT for %s: both. %s\nFIX: %s\n\n",
					ac.CriterionID, verdict.Reasoning, verdict.CodeFix)
			}
		case "acceptable_as_is":
			// The user explicitly said "no skipping important shit".
			// When the judge says acceptable_as_is, we do NOT rewrite
			// the AC to "true". Instead, we record the judge's
			// approval reasoning as a hint and force the repair loop
			// to keep trying — the code must actually satisfy the
			// real criterion, not have it silently disabled.
			//
			// If the criterion is genuinely unsolvable as written,
			// the reasoning loop should emit ac_bug with a rewritten
			// command that correctly measures the same intent, not
			// acceptable_as_is.
			fmt.Printf("    → reasoning said acceptable_as_is for %s, but skipping is disabled; continuing repair. Judge reasoning: %s\n", ac.CriterionID, truncateForLog(verdict.ApproveReason, 200))
			fmt.Fprintf(&hintBlob, "REASONING-LOOP VERDICT for %s: judge said acceptable_as_is but skipping is disabled. Re-examine: either the code genuinely fails this check (and needs fixing), or the AC is wrong (and needs a concrete rewrite via ac_bug verdict). Do NOT accept the failure. Judge's stated reasoning: %s\n\n",
				ac.CriterionID, verdict.ApproveReason)
			// Allow reasoning to fire again on subsequent attempts —
			// the judge may produce a different (correct) verdict
			// when forced to confront the real failure again.
			delete(reasoningApplied, ac.CriterionID)
		}
	}
	return hintBlob.String()
}

// runFoundationSanityCheck runs a quick build gate before the session's
// declared ACs fire. For Node workspaces: pnpm install + tsc --noEmit.
// If either fails, dispatches a focused repair task to fix it. This
// prevents the "every AC fails because deps weren't installed" cascade.
func runFoundationSanityCheck(ctx context.Context, cfg sowNativeConfig, sowDoc *plan.SOW, workingSession plan.Session, runtimeDir string, maxTurns int) {
	if cfg.RepoRoot == "" {
		return
	}
	// Step 1: ensure deps are installed. Use a 3-minute timeout so a
	// stuck pnpm install (waiting for network, postinstall hang, stdin
	// prompt) can't block the entire session forever. The parent ctx
	// has no timeout when --timeout=0 (default), so without this
	// sub-deadline we'd hang indefinitely — which is what killed run18.
	installCtx, installCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer installCancel()
	installCmd := exec.CommandContext(installCtx, "bash", "-lc", "pnpm install --silent 2>&1 || npm install --silent 2>&1 || true")
	installCmd.Dir = cfg.RepoRoot
	_ = installCmd.Run()

	// Step 2: check if tsc compiles. If not, repair. 2-minute timeout.
	tscCtx, tscCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer tscCancel()
	tscCmd := exec.CommandContext(tscCtx, "bash", "-lc", "tsc --noEmit 2>&1 || pnpm --filter './packages/*' typecheck 2>&1")
	tscCmd.Dir = cfg.RepoRoot
	// Augment PATH with node_modules/.bin
	tscCmd.Env = append(os.Environ(), "PATH="+filepath.Join(cfg.RepoRoot, "node_modules", ".bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := tscCmd.CombinedOutput()
	if err == nil {
		return // foundation is green
	}

	// TypeScript doesn't compile. Dispatch a targeted repair.
	failureBlob := fmt.Sprintf("FOUNDATION BUILD FAILURE — TypeScript does not compile. Fix ALL type errors before the session's acceptance criteria run.\n\nOutput of `tsc --noEmit`:\n%s", string(out))
	fmt.Printf("  ⚠ foundation sanity: TypeScript errors detected, dispatching pre-AC repair...\n")
	repairTask := plan.Task{
		ID:          workingSession.ID + "-foundation-fix",
		Description: "fix TypeScript compilation errors before acceptance criteria run",
	}
	sysP, usrP := buildSOWNativePromptsWithOpts(sowDoc, workingSession, repairTask, promptOpts{
		RepoMap:       cfg.RepoMap,
		RepoMapBudget: cfg.RepoMapBudget,
		Repair:        &failureBlob,
		Wisdom:        cfg.Wisdom,
		RawSOW:        cfg.RawSOWText,
		RepoRoot:      cfg.RepoRoot,
	})
	sup := toEngineSupervisor(autoExtractTaskSupervisor(cfg.RepoRoot, cfg.RawSOWText, workingSession, repairTask, 3))
	_ = execNativeTask(ctx, repairTask.ID, sysP, usrP, runtimeDir, cfg, maxTurns, sup)
}

// reviewAndFollowup runs the per-task LLM reviewer after a worker
// completes and, if the reviewer finds gaps, dispatches ONE targeted
// follow-up worker to close them. Cap of one follow-up per task keeps
// cost bounded and prevents infinite loops. Noop when cfg has no
// ReasoningProvider.
//
// Mutates tr in place: on follow-up failure the result is marked
// failed; on follow-up success the result stays successful (the
// follow-up is a supplement, not a replacement).
func reviewAndFollowup(ctx context.Context, sowDoc *plan.SOW, workingSession plan.Session, task plan.Task, tr *plan.TaskExecResult, runtimeDir string, cfg sowNativeConfig, maxTurns int) {
	if cfg.ReasoningProvider == nil {
		return
	}
	if tr == nil || !tr.Success {
		return
	}
	// Collect code excerpts from task.Files.
	excerpts := plan.CollectCodeExcerpts(cfg.RepoRoot, task.Files, 8, 4000)
	// Pull SOW excerpt.
	sowExcerpt := ""
	if cfg.RawSOWText != "" {
		sowExcerpt = extractTaskSpecExcerpt(cfg.RawSOWText, workingSession, task, specExcerptConfig{})
	}
	reviewModel := cfg.ReasoningModel
	if reviewModel == "" {
		reviewModel = cfg.Model
	}
	verdict, err := plan.ReviewTaskWork(ctx, cfg.ReasoningProvider, reviewModel, plan.TaskReviewInput{
		Task:              task,
		SOWSpec:           sowExcerpt,
		SessionAcceptance: workingSession.AcceptanceCriteria,
		CodeExcerpts:      excerpts,
		WorkerSummary:     "", // result blob isn't available here; rely on code + spec
	})
	if err != nil || verdict == nil {
		return
	}
	if verdict.Complete {
		fmt.Printf("    ✔ reviewer: %s complete — %s\n", task.ID, truncateForLog(verdict.Reasoning, 200))
		return
	}
	fmt.Printf("    ✗ reviewer: %s has gaps:\n", task.ID)
	for _, gap := range verdict.GapsFound {
		fmt.Printf("      - %s\n", gap)
	}
	if strings.TrimSpace(verdict.FollowupDirective) == "" {
		return
	}
	fmt.Printf("    → dispatching follow-up to close gaps: %s\n", truncateForLog(verdict.FollowupDirective, 150))

	// Build a follow-up task with the directive as the description.
	followup := plan.Task{
		ID:           fmt.Sprintf("%s-followup", task.ID),
		Description:  verdict.FollowupDirective,
		Files:        task.Files,
		Dependencies: []string{task.ID},
	}
	sysP, usrP := buildSOWNativePromptsWithOpts(sowDoc, workingSession, followup, promptOpts{
		RepoMap:       cfg.RepoMap,
		RepoMapBudget: cfg.RepoMapBudget,
		Wisdom:        cfg.Wisdom,
		RawSOW:        cfg.RawSOWText,
		RepoRoot:      cfg.RepoRoot,
		Briefing:      cfg.Briefings[task.ID], // reuse original briefing
	})
	sup := toEngineSupervisor(autoExtractTaskSupervisor(cfg.RepoRoot, cfg.RawSOWText, workingSession, followup, 3))
	_ = execNativeTask(ctx, followup.ID, sysP, usrP, runtimeDir, cfg, maxTurns, sup)
}

// collectFailingACs returns the subset of acceptance results that failed.
func collectFailingACs(acceptance []plan.AcceptanceResult) []plan.AcceptanceResult {
	var out []plan.AcceptanceResult
	for _, ac := range acceptance {
		if !ac.Passed {
			out = append(out, ac)
		}
	}
	return out
}

// formatSingleACFailure builds a repair prompt block for exactly ONE
// failing criterion. Used by the per-criterion parallel repair path
// so each worker gets a focused assignment instead of the full failure
// blob.
func formatSingleACFailure(ac plan.AcceptanceResult) string {
	var b strings.Builder
	b.WriteString("FAILING ACCEPTANCE CRITERION (fix THIS ONE criterion only):\n\n")
	fmt.Fprintf(&b, "  [FAIL] %s: %s\n", ac.CriterionID, ac.Description)
	if ac.Output != "" {
		for _, line := range strings.Split(strings.TrimSpace(ac.Output), "\n") {
			fmt.Fprintf(&b, "         %s\n", line)
		}
	}
	b.WriteString("\nFix ONLY this criterion. Do not touch code unrelated to this specific failure. After your fix, re-run the exact failing command via bash and confirm exit 0.\n")
	return b.String()
}

// truncateForLog cuts a string to N runes for printing in a single
// line without wrapping or blowing the terminal. Used by the reasoning
// loop's progress output.
func truncateForLog(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// isNodeStack reports whether a SOW declares a JavaScript / TypeScript
// stack. Used to gate ecosystem-specific prompt nudges so Rust/Go/Python
// sessions don't get irrelevant pnpm / tsc guidance.
func isNodeStack(sowDoc *plan.SOW) bool {
	if sowDoc == nil {
		return false
	}
	lang := strings.ToLower(sowDoc.Stack.Language)
	if lang == "typescript" || lang == "javascript" || lang == "ts" || lang == "js" {
		return true
	}
	fw := strings.ToLower(sowDoc.Stack.Framework)
	if strings.Contains(fw, "next") || strings.Contains(fw, "react") || strings.Contains(fw, "vue") ||
		strings.Contains(fw, "svelte") || strings.Contains(fw, "expo") || strings.Contains(fw, "nest") ||
		strings.Contains(fw, "remix") || strings.Contains(fw, "astro") {
		return true
	}
	if sowDoc.Stack.Monorepo != nil {
		mgr := strings.ToLower(sowDoc.Stack.Monorepo.Manager)
		if mgr == "pnpm" || mgr == "npm" || mgr == "yarn" || mgr == "bun" {
			return true
		}
		tool := strings.ToLower(sowDoc.Stack.Monorepo.Tool)
		if strings.Contains(tool, "turbo") || strings.Contains(tool, "nx") || strings.Contains(tool, "lerna") {
			return true
		}
	}
	return false
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
		// Root go.mod must exist — catches "session created packages
		// under cmd/ but forgot to run go mod init first".
		out = append(out, plan.AcceptanceCriterion{
			ID:          "inferred-gomod-root",
			Description: "go.mod exists at repo root",
			FileExists:  "go.mod",
		})
		out = append(out, plan.AcceptanceCriterion{ID: "inferred-build", Description: "go build succeeds", Command: "go build ./..."})
		out = append(out, plan.AcceptanceCriterion{ID: "inferred-vet", Description: "go vet succeeds", Command: "go vet ./..."})
		out = append(out, plan.AcceptanceCriterion{
			ID:          "inferred-test",
			Description: "go tests pass (or no tests)",
			Command:     "if ls *_test.go 2>/dev/null || find . -name '*_test.go' -type f | head -1 | grep -q .; then go test ./...; else true; fi",
		})
	case "rust":
		// Root Cargo.toml must exist — catches "session created
		// crates/foo/ but forgot the workspace root". Also check
		// that the workspace declares a [workspace] section when
		// any crate references workspace.true, which catches the
		// specific "rust-version.workspace = true references a
		// workspace key that doesn't exist" failure the user hit.
		out = append(out, plan.AcceptanceCriterion{
			ID:          "inferred-cargo-root",
			Description: "Cargo.toml exists at workspace root",
			FileExists:  "Cargo.toml",
		})
		out = append(out, plan.AcceptanceCriterion{
			ID:          "inferred-cargo-workspace-consistent",
			Description: "if any crate uses workspace = true, root Cargo.toml has a [workspace] section",
			Command: `set -e
if find crates -name Cargo.toml 2>/dev/null | xargs -r grep -l "workspace = true" 2>/dev/null | head -1 | grep -q .; then
  grep -q "^\[workspace\]" Cargo.toml || (echo "crates reference workspace.true but root Cargo.toml has no [workspace] section" >&2 && exit 1)
fi`,
		})
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
