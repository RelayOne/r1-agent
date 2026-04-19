// Package plan — sow_convert_chunked.go
//
// Chunked prose-to-SOW conversion. Replaces the monolithic
// ConvertProseToSOW path that issued ONE giant LLM call, which on
// large SOWs (1500+ lines of prose, 20+ sessions) regularly took 15+
// minutes and sometimes hung indefinitely on LiteLLM/upstream slow
// responses.
//
// Architecture:
//
//   Phase 1: ExtractSkeleton(prose) → SOW skeleton with sessions
//            stubbed out (id, title, description, outputs only — NO
//            tasks, NO ACs). Small LLM call (~3-8k output).
//
//   Phase 2: For each session stub, ExpandSession(prose, stub) →
//            full Session with tasks + ACs. Each call ~3-8k output.
//            Runs concurrently (up to maxParallel).
//
//   Phase 3: Reassemble skeleton + expanded sessions → SOW.
//            Run autoFillMissingACFields + autoCleanTaskDeps +
//            autoAddMissingInfra + ValidateSOW.
//
// Per-call timeouts prevent the indefinite-hang failure mode. A
// per-session failure is retried once; persistent failure leaves
// the session with an explanatory empty-tasks stand-in rather
// than aborting the whole conversion.

package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// chunkedSkeletonPrompt asks the model to extract just the
// high-level structure of the SOW: project id, name, stack, and a
// list of session stubs (id, title, description, outputs). No
// tasks, no acceptance criteria.
const chunkedSkeletonPrompt = `You are reading a free-form project specification and extracting its high-level structure as JSON. DO NOT generate tasks or acceptance criteria — those come in a later pass.

Output ONLY the JSON object below — no prose, no markdown fences, no commentary. Start with '{' and end with '}'.

Schema:
{
  "id": "string (short slug for the project)",
  "name": "string (human-readable name)",
  "description": "string (1-2 sentence project summary)",
  "stack": {
    "language": "typescript|python|go|rust|...",
    "framework": "next|react-native|actix-web|... (optional)",
    "monorepo": {"tool": "turborepo|nx|cargo-workspace|...", "manager": "pnpm|npm|yarn", "packages": ["..."]},
    "infra": [{"name": "postgres|redis|...", "version": "15", "env_vars": ["DATABASE_URL"]}]
  },
  "sessions": [
    {
      "id": "S1",
      "phase": "foundation|core|integration",
      "title": "Short session name",
      "description": "1-2 sentence what this session delivers",
      "outputs": ["short artifact names — 2-4 words each, e.g. 'monorepo skeleton', 'auth flow', 'web dashboard'"]
    }
  ]
}

RULES:
1. Session count scales with spec size. A 1500-line spec with 8 deliverable areas → 8-15 sessions. A small CLI spec → 1-3 sessions. Don't compress for compression's sake — but also don't INFLATE. Each session pays a fixed ~60-120s briefing-pass cost + per-task review + integration-review + cross-review overhead. A trivial SOW split into 3 sessions pays that overhead 3× for zero parallel-work gain.
2. The first session is foundation: monorepo skeleton, deps, config, hello-world build pass.
3. The last session is integration / polish / docs / deployment configs.
4. Session.outputs are the critical glue: downstream sessions reference these as Inputs to form the dependency DAG. Use 2-4 word artifact names.
5. Infer the stack from the prose. If TypeScript+Next.js, set language and framework. If Postgres mentioned, add to stack.infra with the env vars it needs.
6. If the spec is huge, prefer MORE sessions (each smaller) over FEWER sessions (each larger). The execution layer will further split anything too big.
7. EXCLUSIVE OWNERSHIP: each prose deliverable must be assigned to EXACTLY ONE session. If the prose says "Alert Rules Editor", that scope belongs to one session — not two sessions that both build alert-rules components. Overlap = guaranteed merge conflict.
8. Also emit Inputs for each session — the artifact names from EARLIER sessions this session consumes. This builds the dependency DAG at the skeleton level so the expansion phase can enforce ordering. A session with no inputs is a root session.
9. PARALLELIZATION RESTRAINT — each session you create is a worker that will run briefing + per-task review + integration review + cross-review. That overhead is a fixed tax of roughly 3-6 minutes per session regardless of task count. Only create multiple sessions when:
   (a) the work is genuinely long and the parallel wall-clock savings EXCEED the per-session overhead × extra session count, OR
   (b) the work is structurally independent (different subtrees, different stacks, different runtimes) so one session's failure doesn't block the others, OR
   (c) the work is large enough that a single session's review-recursion budget can't contain it.
   A 7-task TS package or 12-task monorepo scaffold is NOT a case for parallelism — one session chews through that faster than two sessions racing, because two sessions pay ~10 min of review overhead for ~2 min of parallel-work gain. Err toward FEWER, larger sessions when sessions would be homogeneous and short.

PROSE INPUT:
`

// chunkedSessionPrompt asks the model to expand ONE session of an
// already-extracted skeleton into full tasks + ACs. The full prose
// is passed as context so the model can find the spec details
// relevant to this session.
const chunkedSessionPrompt = `You are expanding ONE session of a Statement of Work into its concrete tasks + acceptance criteria. The session's id, title, description, and outputs are already determined. Your job: emit the JSON for THIS session only — including its tasks (with files + dependencies) and acceptance_criteria.

Output ONLY a JSON session object — no prose, no markdown fences, no commentary, no top-level SOW wrapper. Start with '{' and end with '}'.

Schema:
{
  "id": "EXACTLY the session id provided below",
  "phase": "same phase",
  "title": "same title",
  "description": "same description (or improve it if needed)",
  "tasks": [
    {
      "id": "TN — unique across the whole SOW; use numbers far enough apart that other sessions' tasks won't collide (e.g. S3 → T30, T31, T32 ...)",
      "description": "single specific sentence",
      "files": ["paths the task creates or modifies"],
      "dependencies": ["task IDs from PRIOR sessions (look at the inputs list); never reference a task in a later session"],
      "type": "refactor|typesafety|docs|security|architecture|devops|concurrency|review (optional)"
    }
  ],
  "acceptance_criteria": [
    {
      "id": "ACN — unique across the whole SOW",
      "description": "what passes mean",
      "command": "shell command that exits 0; OR file_exists / content_match per the schema"
    }
  ],
  "inputs": ["artifact names from earlier sessions this session depends on"],
  "outputs": ["copy from the skeleton — these are the artifact names downstream sessions reference"]
}

RULES (follow these or this session's tasks will fail at execution):

  a. TASK COUNT scales with session scope. A small infra session needs 5-10 tasks; a major deliverable needs 15-30. Tasks should be small enough to run in a few tool calls each.

  b. Every task description is a SINGLE specific sentence. No bullet lists, no vague goals.

  c. ACCEPTANCE CRITERIA hygiene:
     - Commands run in current working directory; no '$REPO_URL', no 'mktemp', no 'git clone'
     - No '|| echo ok' / '|| true' fallbacks (turn failures into lies)
     - No '2>/dev/null' (hides errors)
     - No long-running processes ('next dev', 'expo start', 'vitest' without 'run')
     - No Playwright / Cypress / Puppeteer (no browser binaries available)
     - Prefer 'pnpm --filter <pkg> <script>' over 'cd dir && cmd'
     - Prefer direct binaries ('tsc', 'vitest run') over 'npx'
     - FILE EXISTENCE CHECK: use the "file_exists" JSON field (NOT the "command" field). "file_exists" is a struct field the harness evaluates natively. Putting the string "file_exists path" in the "command" field runs it as a shell command which fails with exit 127 because file_exists is not a bash builtin. For config artifacts: {"file_exists": "path/to/file.json"}. For source files: pair with a build/test command in "command".
     - 3-5 ACs per session is the sweet spot; the FIRST AC should be a build/test that catches compilation
     - If an AC uses a tool, that tool must be in package.json or stack.infra

  c2. SCOPE EXCLUSIVITY: This session's deliverables are EXCLUSIVE — if the skeleton assigned "Alert Rules Editor" to this session, no other session should create alert-rules files. If you see that another session's skeleton description overlaps, do NOT duplicate its work. Only create tasks for deliverables in THIS session's description and outputs.

  d. DEPENDENCIES: only reference task IDs in EARLIER sessions (per the session.inputs hints) or earlier tasks IN THIS session. Forward references break the DAG.

  e. SCAFFOLDING: every declared deliverable surface needs scaffolding tasks. Next.js app → app/layout.tsx, app/page.tsx, per-route page.tsx, next.config.js, tailwind.config.ts. Expo app → App.tsx, navigation, per-screen, app.json, metro.config.js. Backend → server entry, route registration, per-endpoint handler, middleware. Don't ship "implement the X app" as one task — apps are dozens of tasks.

SESSION TO EXPAND (id, title, description, outputs are FIXED; you are filling in tasks + ACs):
`

// ConvertProseToSOWChunked is the chunked alternative to
// ConvertProseToSOW. It runs in three phases:
//
//   1. Skeleton extraction: small LLM call returns project + sessions
//      (titles + descriptions + outputs only).
//   2. Per-session expansion: one LLM call per session, parallel up
//      to maxParallel. Each call returns full tasks + ACs.
//   3. Reassembly + validation.
//
// Failure handling: per-call timeout (10 min default per call), one
// retry per session on transient errors. A session that exhausts
// retries gets a stand-in note in its description and an empty
// task list, surfaced in the returned SOW so the operator sees
// what failed rather than aborting the whole conversion.
//
// Returns the parsed SOW + raw JSON of the assembled result.
// SkipRefine can be set to true by callers who want the
// chunked convert to produce a SOW without running the CTO
// approval + refine loop. Trades plan quality for speed:
// ~5 min instead of ~30 min. The harness's execution-time
// quality gates (AC checks, content judge, repair loops)
// catch what the refine loop would have caught, just at a
// higher per-session cost.
var SkipRefine bool

func ConvertProseToSOWChunked(ctx context.Context, prose string, prov provider.Provider, model string, maxParallel int) (*SOW, []byte, error) {
	if strings.TrimSpace(prose) == "" {
		return nil, nil, fmt.Errorf("empty prose")
	}
	if prov == nil {
		return nil, nil, fmt.Errorf("no provider configured")
	}
	if maxParallel < 1 {
		maxParallel = 4
	}

	// Phase 1: skeleton.
	skel, err := extractSkeleton(ctx, prose, prov, model)
	if err != nil {
		return nil, nil, fmt.Errorf("skeleton extraction: %w", err)
	}
	fmt.Printf("  ⚡ chunked convert: skeleton has %d sessions; expanding each in parallel (max %d)\n", len(skel.Sessions), maxParallel)

	// Phase 2: per-session expansion. Worker pool bounded by
	// maxParallel; each worker runs expandSession with timeout +
	// one retry.
	type result struct {
		idx     int
		session Session
		err     error
	}
	expanded := make([]Session, len(skel.Sessions))
	results := make(chan result, len(skel.Sessions))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, stub := range skel.Sessions {
		wg.Add(1)
		go func(i int, stub Session) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			full, err := expandSessionWithRetry(ctx, prose, &skel.Stack, stub, prov, model)
			elapsed := time.Since(start).Round(time.Second)
			if err != nil {
				fmt.Printf("     ⚠ session %s DROPPED after expand failure (%s): %v\n", stub.ID, elapsed, err)
				fmt.Printf("       original scope: %s — %s\n", stub.Title, stub.Description)
				fmt.Printf("       operator action: re-run OR add this session manually to the SOW\n")
				results <- result{idx: i, err: err}
				return
			}
			fmt.Printf("     ✓ session %s expanded in %s (%d tasks, %d ACs)\n", stub.ID, elapsed, len(full.Tasks), len(full.AcceptanceCriteria))
			results <- result{idx: i, session: full}
		}(i, stub)
	}
	wg.Wait()
	close(results)
	expandFailed := make([]bool, len(skel.Sessions))
	for r := range results {
		if r.err != nil {
			expandFailed[r.idx] = true
			continue
		}
		expanded[r.idx] = r.session
	}
	// Filter out failed sessions — a stub with no tasks/ACs fails
	// ValidateSOW downstream ('session X has no acceptance criteria')
	// and forces the 20+ min monolith fallback. Dropping preserves
	// partial coverage so the run proceeds; the warning above tells
	// the operator exactly what was lost and how to recover.
	kept := expanded[:0]
	for i, s := range expanded {
		if expandFailed[i] {
			continue
		}
		kept = append(kept, s)
	}
	expanded = kept

	// Phase 3: reassemble + validate.
	out := &SOW{
		ID:          skel.ID,
		Name:        skel.Name,
		Description: skel.Description,
		Stack:       skel.Stack,
		Sessions:    expanded,
	}
	// Per-session expanders run independently and can't coordinate
	// on task-ID ranges, so two sessions often pick overlapping
	// numeric ranges (e.g. both emit T30-T44). Renumber every
	// task to a globally-unique ID in session order and rewrite
	// intra-session Dependencies via the oldID→newID map. AC IDs
	// get the same treatment.
	renumberTasksAndACsGlobally(out)

	// Recursive consistency + coverage loop: run deterministic
	// consistency + LLM coverage review. If either surfaces issues
	// (missing sessions to add, dangling inputs, etc.) apply fixes
	// and re-check. Cap at 3 rounds so a pathological case can't
	// loop indefinitely. Converges when consistency is clean AND
	// coverage review adds zero sessions.
	const maxReviewRounds = 3
	for round := 1; round <= maxReviewRounds; round++ {
		// Deterministic repair FIRST — fixes file-ownership conflicts
		// and drops dangling inputs in-place. Then re-run the check
		// to see what remains after the auto-repair.
		fd, or, id := repairChunkedConsistency(out)
		if fd > 0 || or > 0 || id > 0 {
			fmt.Printf("  🔧 chunked convert consistency round %d auto-repair: %d file-claim drop(s), %d output rename(s), %d input drop(s)\n", round, fd, or, id)
		}
		issues := checkChunkedConsistency(out)
		added := 0
		if ctx.Err() == nil {
			var cErr error
			added, cErr = reviewCoverageAndPatch(ctx, prose, out, prov, model)
			if cErr != nil {
				fmt.Printf("  ⚠ chunked convert coverage review round %d skipped: %v\n", round, cErr)
			}
		}
		if len(issues) > 0 {
			fmt.Printf("  ⚠ chunked convert consistency round %d: %d residual issue(s) after repair\n", round, len(issues))
			for i, is := range issues {
				if i >= 10 {
					fmt.Printf("     ... and %d more\n", len(issues)-10)
					break
				}
				fmt.Printf("     - %s\n", is)
			}
		}
		if added > 0 {
			fmt.Printf("  ✓ chunked convert coverage round %d added %d session(s)\n", round, added)
			renumberTasksAndACsGlobally(out)
		}
		// Converged: coverage added zero sessions AND repair found
		// nothing to fix this round. Residual consistency issues
		// that survive auto-repair are edge cases the execution
		// layer handles via the fuzzy DAG.
		// Canonicalize session inputs to match producer outputs
		// exactly. Without this every expander's paraphrased input
		// ("design-tokens package") fails to match the producer's
		// actual output ("design tokens") and the DAG resolver
		// falls back to declaration-order — serializing what
		// should run in parallel. Runs every round because
		// coverage-added sessions introduce new producers that
		// earlier sessions' inputs may now be able to match.
		cr := canonicalizeSessionInputs(out)
		if cr > 0 {
			fmt.Printf("  🧭 chunked convert canonicalized %d input(s) to producer-exact names (DAG will resolve them)\n", cr)
		}
		if added == 0 && fd == 0 && or == 0 && id == 0 && cr == 0 {
			if round > 1 {
				fmt.Printf("  ✓ chunked convert converged after %d review round(s)\n", round)
			}
			break
		}
		if round == maxReviewRounds {
			fmt.Printf("  ⚠ chunked convert hit review cap (%d rounds) — proceeding with %d residual issues\n", maxReviewRounds, len(issues))
		}
	}

	autoFillMissingACFields(out)
	autoCleanTaskDeps(out)
	autoAddMissingInfra(out)
	if errs := ValidateSOW(out); len(errs) > 0 {
		return out, nil, fmt.Errorf("chunked convert produced invalid SOW: %s", strings.Join(errs, "; "))
	}

	// Final plan approval — CTO-role LLM asks: "reading the prose
	// AND this merged SOW, will this plan deliver what the user
	// asked for?" Coverage loop confirms no gaps; this confirms
	// fidelity + feasibility + coherence. Best-effort: a transport
	// error logs + proceeds rather than halting (the review is
	// advisory). A blocking verdict, however, halts with the
	// operator-facing concern list so the SOW gets fixed before
	// dispatch.
	// SkipRefine: bypass the entire CTO approval + refine loop.
	// The SOW goes straight from chunked convert to dispatch.
	// Cuts planning from ~30 min to ~5 min. The harness's
	// execution-time gates (AC checks, content judge, repair
	// loops) handle what refine would have caught.
	if SkipRefine {
		fmt.Println("  ⚡ --fast-plan: skipping CTO approval + refine loop (saves ~25 min)")
		out.ChunkedConvertApproved = true
		raw, _ := json.MarshalIndent(out, "", "  ")
		return out, raw, nil
	}

	// CTO approval + refine loop. The agentic reviewer reads prose
	// + SOW via tool calls and emits a structured verdict. On
	// approve we mark the SOW chunked-approved and proceed. On
	// request_changes we invoke the structured refine pass —
	// rewrites the SOW addressing each concern — and re-review,
	// up to maxRefineRounds (default 4). On reject or blocking
	// verdict we halt. The refine path closes the gap where
	// previously we just printed concerns and dispatched-with-
	// known-bugs.
	// maxRefineRounds raised from 2 to 4 after run 47 showed:
	//   round 0: fidelity 62, feasibility 72
	//   round 1: fidelity 78, feasibility 82 (big jump)
	//   round 2: proceeded with residual non-blocking concerns
	// Each round materially improves the plan when the refiner
	// has concrete fix directives, and the best-of snapshot
	// tracker protects against regressions — so allowing 2
	// additional rounds trades planning time for higher-quality
	// plans without adding rollback risk.
	const maxRefineRounds = 4
	// cloneSOW takes a deep copy via JSON round-trip so the
	// best-snapshot can't be mutated by a subsequent refine
	// iteration. The SOW is large-ish (~30k bytes for a
	// Sentinel-class plan) so this runs at most 3 times total
	// (once per approval round).
	cloneSOW := func(s *SOW) *SOW {
		if s == nil {
			return nil
		}
		b, err := json.Marshal(s)
		if err != nil {
			return s // best-effort fallback; marshal failures here are vanishingly rare
		}
		var copy SOW
		if err := json.Unmarshal(b, &copy); err != nil {
			return s
		}
		return &copy
	}
	if ctx.Err() == nil {
		var verdict *FinalApprovalVerdict
		var aerr error
		// Best-SOW tracker: refine rounds can REGRESS (run 43
		// symptom — round 2 dropped fidelity 88→85 and added 4
		// blocking concerns while "addressing" 3 from round 1).
		// Keep the best scoring SOW+verdict across rounds so a
		// regression at the cap can roll back to the better
		// earlier state instead of surfacing a strictly-worse
		// terminal failure. Scoring: fewest blocking > highest
		// (fidelity + feasibility) > earliest round (stable).
		type refineSnapshot struct {
			sow     *SOW
			verdict *FinalApprovalVerdict
			round   int
		}
		var best *refineSnapshot
		isBetter := func(a, b *refineSnapshot) bool {
			if b == nil {
				return true
			}
			aBlock := 0
			for _, c := range a.verdict.Concerns {
				if c.Severity == "blocking" {
					aBlock++
				}
			}
			bBlock := 0
			for _, c := range b.verdict.Concerns {
				if c.Severity == "blocking" {
					bBlock++
				}
			}
			if aBlock != bBlock {
				return aBlock < bBlock
			}
			aScore := a.verdict.FidelityScore + a.verdict.FeasibilityScore
			bScore := b.verdict.FidelityScore + b.verdict.FeasibilityScore
			if aScore != bScore {
				return aScore > bScore
			}
			// Tie: prefer the earlier (stabler) round.
			return a.round < b.round
		}
		for round := 0; round <= maxRefineRounds; round++ {
			// Give EACH approval attempt its own fresh context —
			// agentic and monolithic must not share a budget
			// (otherwise a slow agentic failure leaves monolith with
			// no time). Both detached from the chunked parent so
			// they can't be starved by a long expand phase.
			// Observed in run 38: chunkedCtx expired during convert
			// → approval got -4s and failed instantly.
			agenticCtx, agenticCancel := context.WithTimeout(context.Background(), 15*time.Minute)
			verdict, aerr = FinalPlanApprovalAgentic(agenticCtx, prose, out, prov, model, "")
			agenticCancel()
			if aerr != nil {
				fmt.Printf("  ⚠ agentic final approval failed (%v); falling back to monolithic\n", aerr)
				monolithicCtx, monolithicCancel := context.WithTimeout(context.Background(), 12*time.Minute)
				verdict, aerr = FinalPlanApproval(monolithicCtx, prose, out, prov, model)
				monolithicCancel()
			}
			if aerr != nil {
				// Both agentic AND monolithic approval failed. This
				// is a stronger signal than "approval skipped" —
				// without review, we have no idea whether the
				// merged SOW is structurally sound or full of
				// cross-session conflicts. Previously we just
				// logged and dispatched; now we surface as a
				// terminal failure so the operator sees the gap
				// and can retry with more provider budget or
				// investigate what made approval unreachable.
				return out, nil, &TerminalApprovalError{
					Reason: fmt.Sprintf("final plan approval failed (both agentic and monolithic): %v — SOW unreviewed, NOT dispatching", aerr),
				}
			}
			fmt.Print(FormatApprovalVerdict(verdict))
			// Snapshot the current (SOW, verdict) pair so a
			// later regression can roll back to this one.
			cur := &refineSnapshot{sow: cloneSOW(out), verdict: verdict, round: round}
			if isBetter(cur, best) {
				best = cur
			}
			// reject — reviewer says the SOW is structurally broken.
			// BUT: the concerns often have actionable fix directives
			// ("merge S3+S21", "change S37 path"). Attempt ONE refine
			// pass before halting. If re-review still rejects after
			// refine, THEN it's terminal. This closes the gap where
			// run 39's reviewer produced 12 fixable concerns but we
			// bailed instantly without trying.
			if verdict.Decision == "reject" {
				if round < maxRefineRounds {
					fmt.Printf("  🔁 reject-repair round %d: attempting refine on %d concern(s) before giving up\n", round+1, len(verdict.Concerns))
					// Let the refine loop handle this — fall through to
					// the refine block below. The "reject" decision
					// doesn't short-circuit anymore on the FIRST round;
					// if refine can't fix it, the re-review will reject
					// again and we'll halt on the SECOND round.
				} else {
					return out, nil, &TerminalApprovalError{
						Reason: fmt.Sprintf("final plan approval: reject after %d refine attempt(s) — %d concern(s); SOW not fit for dispatch", round, len(verdict.Concerns)),
					}
				}
			}
			// Defense-in-depth against inconsistent model output: an
			// approve decision with blocking concerns is malformed —
			// trust the severity, not the decision label, and treat
			// as request_changes so refine still runs.
			if verdict.HasBlocking() {
				// Fall through to the request_changes refine path
				// below (round-cap check + refine attempt).
			} else if verdict.Decision == "approve" || len(verdict.Concerns) == 0 {
				out.ChunkedConvertApproved = true
				break
			}
			// request_changes with concerns (any severity) — refine
			// loop attempts to fix them. Blocking concerns are
			// EXACTLY what the refine loop should target: explicit
			// fix directives the reviewer wants applied (file
			// collisions to resolve, undeclared packages to declare,
			// oversized sessions to split). Bailing on blocking
			// before refine bypasses the entire purpose of the
			// loop. After the cap, if blocking concerns still
			// remain, halt with the operator-facing concern list.
			if round >= maxRefineRounds {
				if verdict.HasBlocking() {
					// Before surfacing a terminal failure, check
					// whether an EARLIER round produced a better
					// SOW (fewer blocking concerns or higher
					// combined score). If so, roll back — the
					// refiner regressed, but the prior state may
					// still be dispatchable (if blocking count
					// dropped to zero in that earlier round) OR
					// at least shows a cleaner plan than what
					// terminal would dispatch nothing for.
					if best != nil && best.round < round && isBetter(best, cur) {
						rb := 0
						for _, c := range best.verdict.Concerns {
							if c.Severity == "blocking" {
								rb++
							}
						}
						fmt.Printf("  ⚠ refine round %d regressed (blocking: %d → %d); rolling back to round %d snapshot\n",
							round, len(cur.verdict.Concerns), len(best.verdict.Concerns), best.round)
						out = best.sow
						verdict = best.verdict
						if !verdict.HasBlocking() {
							fmt.Printf("  ✓ rollback snapshot has no blocking concerns; proceeding to dispatch\n")
							out.ChunkedConvertApproved = true
							break
						}
						return out, nil, &TerminalApprovalError{
							Reason: fmt.Sprintf("final plan approval: %d blocking concern(s) remain after %d refine round(s) (best-of rolled back to round %d); SOW not fit for dispatch", rb, maxRefineRounds, best.round),
						}
					}
					return out, nil, &TerminalApprovalError{
						Reason: fmt.Sprintf("final plan approval: %d blocking concern(s) remain after %d refine round(s); SOW not fit for dispatch", len(verdict.Concerns), maxRefineRounds),
					}
				}
				fmt.Printf("  ⚠ refine cap (%d rounds) reached — proceeding with %d unaddressed non-blocking concern(s)\n", maxRefineRounds, len(verdict.Concerns))
				out.ChunkedConvertApproved = true
				break
			}
			blockingCount := 0
			for _, c := range verdict.Concerns {
				if c.Severity == "blocking" {
					blockingCount++
				}
			}
			if blockingCount > 0 {
				fmt.Printf("  🔁 refine round %d: addressing %d concern(s) (%d blocking) — refine first, halt only if cap reached with blockers remaining\n", round+1, len(verdict.Concerns), blockingCount)
			} else {
				fmt.Printf("  🔁 refine round %d: addressing %d concern(s) before dispatch\n", round+1, len(verdict.Concerns))
			}
			// Refine also gets a fresh detached context for the same
			// reason as the approval calls: if the approval call ran
			// after the chunked parent expired, using ctx for refine
			// would return context.DeadlineExceeded immediately,
			// which the non-blocking branch below would then treat
			// as a "refine failed, proceed with current SOW"
			// approval — silently bypassing the request_changes
			// verdict that just came back.
			refineCtx, refineCancel := context.WithTimeout(context.Background(), 15*time.Minute)
			refined, rerr := RefineSOWFromConcerns(refineCtx, prose, out, verdict.Concerns, prov, model)
			refineCancel()
			if rerr != nil {
				if blockingCount > 0 {
					// Refine pass couldn't run AND we had blocking
					// concerns. Don't mark approved — that would
					// silently dispatch a SOW the reviewer said was
					// unfit. Surface as terminal failure so the
					// operator sees the gap.
					return out, nil, &TerminalApprovalError{
						Reason: fmt.Sprintf("refine pass failed (%v) with %d blocking concern(s) outstanding; SOW not fit for dispatch", rerr, blockingCount),
					}
				}
				fmt.Printf("  ⚠ refine pass failed (%v); proceeding with current SOW (no blocking concerns)\n", rerr)
				out.ChunkedConvertApproved = true
				break
			}
			// Re-run the SAME deterministic cleanup pipeline the
			// initial chunked convert applied so a refinement that
			// introduces a duplicate file claim, dangling input,
			// missing infra ref, output rename collision, missing
			// AC field, or dangling task dep doesn't slip past
			// validation. Convergence check includes outputRenames
			// because a rename can create a second-order collision
			// with a session whose output now matches the prefixed
			// name — needs another repair pass to settle.
			for i := 0; i < 4; i++ {
				fd, or, id := repairChunkedConsistency(refined)
				cr := canonicalizeSessionInputs(refined)
				if fd == 0 && or == 0 && id == 0 && cr == 0 {
					break
				}
			}
			// On the refine path we are STRICT: the refiner was
			// explicitly told to preserve every task + every AC,
			// and we already verified ID conservation. We do NOT
			// run autoFillMissingACFields here because it would
			// synthesize a generic description for an AC whose
			// real description was cleared (turning a real gate
			// into a pass-by-default manual check). We do NOT run
			// autoCleanTaskDeps here because it can delete a task
			// whose description was cleared. Instead, we run only
			// structural-only helpers (autoAddMissingInfra +
			// autoDeduplicateTaskIDs) and pre-check that no AC
			// has lost its description or its verifier — those
			// are gate-weakening regressions that must reject
			// the refinement and fall through to the legacy
			// critique path.
			//
			// autoDeduplicateTaskIDs is safe on the strict path:
			// renaming a collided ID doesn't weaken a gate, and
			// without it the refiner's per-session counter bug
			// (observed run 41) would bounce every refinement
			// into "preserving previous SOW" and leave the plan
			// stuck with the concerns that triggered refine in
			// the first place.
			autoAddMissingInfra(refined)
			autoDeduplicateTaskIDs(refined)
			// Prune dangling task.Dependencies: the splice step
			// inside RefineSOWFromConcerns restores dropped tasks
			// verbatim, but some of those tasks may carry deps on
			// OTHER dropped tasks that weren't restored (splice's
			// rename detection suppressed them, or the refiner
			// renamed-in-place via a different path the detection
			// didn't catch). Without this prune, ValidateSOW
			// rejects the whole refinement on broken task deps —
			// which is exactly how run 44 died at 31min (T394's
			// dep on T313 was the fatal blocker).
			//
			// Safe on the strict path because CleanTaskDependencies
			// doesn't touch task descriptions or ACs; it only
			// drops empty-ID/empty-desc tasks (shouldn't happen
			// post-splice) and trims Dependencies arrays. Any
			// legitimate cross-session ordering info survives via
			// session-level Inputs/Outputs.
			autoCleanTaskDeps(refined)
			if reason := refineGateRegressions(out, refined); reason != "" {
				fmt.Printf("  ⚠ refine weakens an acceptance gate — preserving previous SOW: %s\n", reason)
				break
			}
			if vErrs := ValidateSOW(refined); len(vErrs) > 0 {
				// Refined SOW is structurally broken AND the cleanup
				// helpers couldn't repair it. Preserve the previous
				// SOW and DO NOT mark it approved — the prior
				// verdict was request_changes, so it still has
				// concerns that should fall through to the legacy
				// CritiqueAndRefine fallback in main.go rather than
				// dispatching unchanged.
				fmt.Printf("  ⚠ refined SOW failed validation — preserving previous SOW (concerns remain). errors:\n")
				for _, e := range vErrs {
					fmt.Printf("       - %s\n", e)
				}
				break
			}
			out = refined
			fmt.Printf("  ✓ refine round %d applied — re-running CTO approval\n", round+1)
		}
	}

	raw, _ := json.MarshalIndent(out, "", "  ")
	return out, raw, nil
}

// extractSkeleton issues the phase-1 LLM call and parses the
// skeleton SOW.
func extractSkeleton(ctx context.Context, prose string, prov provider.Provider, model string) (*SOW, error) {
	userText := chunkedSkeletonPrompt + prose
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	skelCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	resp, err := callChatWithCtx(skelCtx, prov, provider.ChatRequest{
		Model:     model,
		MaxTokens: 64000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, fmt.Errorf("skeleton truncated at max_tokens — increase budget")
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("skeleton returned no usable content (stop_reason=%q)", resp.StopReason)
	}
	var skel SOW
	if _, err := jsonutil.ExtractJSONInto(raw, &skel); err != nil {
		return nil, fmt.Errorf("parse skeleton: %w", err)
	}
	if len(skel.Sessions) == 0 {
		return nil, fmt.Errorf("skeleton produced zero sessions")
	}
	return &skel, nil
}

// expandSessionWithRetry runs expandSession with escalating
// budget. First attempt uses 64k output; if that hits max_tokens,
// the retry doubles to 128k which is the practical ceiling on
// most providers and enough for truly large sessions. Non-
// max_tokens transient errors (JSON parse, timeout, network) get
// the same retry at the SAME budget since bigger output wouldn't
// help there.
func expandSessionWithRetry(ctx context.Context, prose string, stack *StackSpec, stub Session, prov provider.Provider, model string) (Session, error) {
	const firstBudget = 64000
	const retryBudget = 128000
	full, err := expandSession(ctx, prose, stack, stub, prov, model, firstBudget)
	if err == nil {
		return full, nil
	}
	// Pick retry budget based on failure class. max_tokens → bigger
	// budget gives the model room. Other errors → same budget since
	// the issue is parse/network, not size.
	budget := firstBudget
	if strings.Contains(err.Error(), "max_tokens") {
		budget = retryBudget
	}
	full, err2 := expandSession(ctx, prose, stack, stub, prov, model, budget)
	if err2 != nil {
		return Session{}, fmt.Errorf("first attempt: %v; retry: %v", err, err2)
	}
	return full, nil
}

// expandSession issues one phase-2 LLM call to fill in tasks + ACs
// for a single session stub. maxTokens is the output budget for
// this attempt — expandSessionWithRetry escalates this on
// max_tokens truncation so a genuinely-too-big session gets a
// second chance with more room.
func expandSession(ctx context.Context, prose string, stack *StackSpec, stub Session, prov provider.Provider, model string, maxTokens int) (Session, error) {
	if maxTokens <= 0 {
		maxTokens = 64000
	}
	stubBlob, err := json.MarshalIndent(stub, "", "  ")
	if err != nil {
		return Session{}, fmt.Errorf("marshal stub: %w", err)
	}
	stackBlob, _ := json.MarshalIndent(stack, "", "  ")
	userText := chunkedSessionPrompt + string(stubBlob) +
		"\n\nSTACK CONTEXT:\n" + string(stackBlob) +
		"\n\nFULL SPEC PROSE (find the parts relevant to this session):\n" + prose
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	sessCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	resp, err := callChatWithCtx(sessCtx, prov, provider.ChatRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return Session{}, err
	}
	if resp.StopReason == "max_tokens" {
		return Session{}, fmt.Errorf("session truncated at max_tokens (budget %d) — retry with larger budget", maxTokens)
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return Session{}, fmt.Errorf("empty response (stop_reason=%q)", resp.StopReason)
	}
	var sess Session
	if _, err := jsonutil.ExtractJSONInto(raw, &sess); err != nil {
		// Persist the raw response for postmortem on parse failures so
		// new repair patterns can be derived from real LLM output.
		// One file per session ID + timestamp; bounded count via the
		// 50-most-recent rule below to avoid disk bloat on a long run.
		dumpPath := fmt.Sprintf("/tmp/stoke-expand-fail-%s-%d.txt", stub.ID, time.Now().UnixNano())
		_ = os.WriteFile(dumpPath, []byte(raw), 0644)
		return Session{}, fmt.Errorf("parse session: %w (raw saved to %s)", err, dumpPath)
	}
	// Force the ID + outputs to match the stub even if the model
	// drifted — these are load-bearing for the DAG.
	sess.ID = stub.ID
	if len(sess.Outputs) == 0 {
		sess.Outputs = stub.Outputs
	}
	if strings.TrimSpace(sess.Title) == "" {
		sess.Title = stub.Title
	}
	if strings.TrimSpace(sess.Description) == "" {
		sess.Description = stub.Description
	}
	return sess, nil
}

// callChatWithCtx wraps prov.Chat with a context-aware timeout.
// The provider interface itself is synchronous; this goroutine +
// channel pattern lets the caller bail out when ctx is canceled
// (e.g. our 10-minute per-call timeout) instead of blocking
// indefinitely on a hung LiteLLM upstream — the run-20 failure
// mode where the conversion call never returned.
func callChatWithCtx(ctx context.Context, prov provider.Provider, req provider.ChatRequest) (*provider.ChatResponse, error) {
	type chatResult struct {
		resp *provider.ChatResponse
		err  error
	}
	ch := make(chan chatResult, 1)
	go func() {
		resp, err := prov.Chat(req)
		ch <- chatResult{resp: resp, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("chat timed out after %v: %w", deadlineDuration(ctx), ctx.Err())
	case r := <-ch:
		return r.resp, r.err
	}
}

// scoreSessionOwnership scores how well a session "fits" as the
// owner of a given file path. Higher = better fit. Used by the
// semantic merger to resolve file-claim conflicts when multiple
// sessions declare the same file in task.Files.
//
// Scoring:
//   - Full session-output phrase appears as substring in file → +3
//   - Any path segment shares a word with any session output → +2
//   - Any path segment appears in session title/description → +1
//   - Session ID appears as substring in file (rare) → +1
//
// Ties are broken by declaration order (lower session index wins).
// The result: apps/web/tailwind.config.ts goes to the session
// whose outputs say "web app" or whose title is "Web Application
// Foundation", not whichever session happened to emit first.
func scoreSessionOwnership(file string, s Session) float64 {
	fileLower := strings.ToLower(file)
	pathParts := strings.Split(strings.Trim(fileLower, "/"), "/")
	score := 0.0

	for _, out := range s.Outputs {
		outLower := strings.ToLower(strings.TrimSpace(out))
		if outLower == "" {
			continue
		}
		if strings.Contains(fileLower, outLower) {
			score += 3.0
			continue
		}
		// Token-level overlap: any word of the output matches any
		// path segment.
		outWords := strings.Fields(outLower)
		for _, w := range outWords {
			if len(w) < 3 {
				continue // skip stopwords ("a", "of", etc.)
			}
			for _, part := range pathParts {
				if part == w || strings.Contains(part, w) || strings.Contains(w, part) {
					score += 2.0
					break
				}
			}
		}
	}

	titleDesc := strings.ToLower(s.Title + " " + s.Description)
	for _, part := range pathParts {
		if len(part) < 3 {
			continue
		}
		if strings.Contains(titleDesc, part) {
			score += 1.0
		}
	}

	if strings.Contains(fileLower, strings.ToLower(s.ID)) {
		score += 1.0
	}

	return score
}

// repairChunkedConsistency is the semantic merger: it runs AFTER
// all session expanders return proposals and BEFORE
// ValidateSOW/dispatch. Git-shaped flow —
//
//   1. Collect every claim: which session put which file in its
//      task.Files; which output-artifact name which session emits.
//   2. For each CONFLICTED file (N > 1 claimants), pick the
//      winner by scoreSessionOwnership. Remove the file from the
//      losers' task.Files (they can still READ the file, they
//      just don't own/declare scope over it).
//   3. For each CONFLICTED output name (N > 1 producers), pick
//      winner by scoreSessionOwnership applied to the output name
//      as if it were a path. Losers keep the output entry but
//      prefixed with their session id to disambiguate
//      (downstream consumers via fuzzy match still resolve).
//   4. Drop dangling session.Inputs entries that don't match any
//      producer (even after output renames) — the DAG can't use
//      them anyway.
//
// Returns counts (fileDrops, outputRenames, inputDrops) for log
// rendering. Mutates the SOW in place.
func repairChunkedConsistency(sow *SOW) (fileDrops, outputRenames, inputDrops int) {
	if sow == nil {
		return 0, 0, 0
	}

	// --- Pass 1: file ownership via semantic scoring. ---
	// Collect every claim: file → []sessionIndex.
	fileClaims := map[string][]int{}
	for i := range sow.Sessions {
		for ti := range sow.Sessions[i].Tasks {
			for _, f := range sow.Sessions[i].Tasks[ti].Files {
				key := strings.TrimSpace(f)
				if key == "" {
					continue
				}
				fileClaims[key] = append(fileClaims[key], i)
			}
		}
	}
	// Dedup per-file claim list (same session with multiple tasks
	// touching the same file is not a conflict).
	fileOwner := map[string]int{}
	for file, claims := range fileClaims {
		seen := map[int]bool{}
		unique := []int{}
		for _, idx := range claims {
			if !seen[idx] {
				seen[idx] = true
				unique = append(unique, idx)
			}
		}
		if len(unique) == 1 {
			fileOwner[file] = unique[0]
			continue
		}
		// Conflicted — score each claimant.
		bestIdx := unique[0]
		bestScore := scoreSessionOwnership(file, sow.Sessions[bestIdx])
		for _, idx := range unique[1:] {
			sc := scoreSessionOwnership(file, sow.Sessions[idx])
			if sc > bestScore {
				bestScore = sc
				bestIdx = idx
			}
			// Tie-break: lower index wins (first declared).
		}
		fileOwner[file] = bestIdx
	}
	// Apply: drop file from any session that isn't the winner, BUT
	// preserve the loser's intent. The loser wanted to work on that
	// file for a reason (usually to extend/modify what the owner
	// creates). Rewrite the loser's task description to reflect an
	// EDIT relationship instead of a CREATE relationship, and add
	// the winner session's id to the loser's session.Inputs so the
	// DAG serializes correctly (winner-creates, loser-edits).
	for i := range sow.Sessions {
		s := &sow.Sessions[i]
		addedInputs := map[string]bool{}
		for ti := range s.Tasks {
			t := &s.Tasks[ti]
			kept := t.Files[:0]
			var edits []string // filenames that shifted from create→edit
			for _, f := range t.Files {
				key := strings.TrimSpace(f)
				if key == "" {
					continue
				}
				if owner, ok := fileOwner[key]; ok && owner != i {
					fileDrops++
					edits = append(edits, key)
					// Record the owner session's ID in this session's
					// inputs so the DAG knows: owner produces, we
					// edit. De-dup via addedInputs map.
					ownerID := sow.Sessions[owner].ID
					if !addedInputs[ownerID] {
						s.Inputs = append(s.Inputs, ownerID+" artifact ownership")
						addedInputs[ownerID] = true
					}
					continue
				}
				kept = append(kept, f)
			}
			t.Files = kept
			// Annotate the task description so the worker treats the
			// edit-list as "files already exist, modify in place" —
			// not "create these from scratch". Without this the
			// worker would see a reduced task.Files and might think
			// its scope shrunk; with the annotation, the original
			// intent (extend these files) is preserved in prose.
			if len(edits) > 0 {
				t.Description = strings.TrimSuffix(t.Description, ".") +
					". NOTE: the following file(s) are created by another session — your work EXTENDS/MODIFIES them in place, does not recreate them: " +
					strings.Join(edits, ", ") + "."
			}
		}
	}

	// --- Pass 2: output-name collisions. ---
	// Two sessions emitting "web dashboard" = the DAG can't tell
	// which one produces it. Keep the highest-scoring session's
	// claim, rename the loser's entry to "<sessionID> <name>" so
	// it's still visible (the fuzzy DAG resolver can still match
	// fragments).
	outClaims := map[string][]int{}
	for i := range sow.Sessions {
		for _, out := range sow.Sessions[i].Outputs {
			key := strings.ToLower(strings.TrimSpace(out))
			if key == "" {
				continue
			}
			outClaims[key] = append(outClaims[key], i)
		}
	}
	for out, claims := range outClaims {
		if len(claims) < 2 {
			continue
		}
		bestIdx := claims[0]
		bestScore := scoreSessionOwnership(out, sow.Sessions[bestIdx])
		for _, idx := range claims[1:] {
			sc := scoreSessionOwnership(out, sow.Sessions[idx])
			if sc > bestScore {
				bestScore = sc
				bestIdx = idx
			}
		}
		for _, idx := range claims {
			if idx == bestIdx {
				continue
			}
			s := &sow.Sessions[idx]
			for oi := range s.Outputs {
				if strings.EqualFold(strings.TrimSpace(s.Outputs[oi]), out) {
					s.Outputs[oi] = s.ID + " " + s.Outputs[oi]
					outputRenames++
				}
			}
		}
	}
	// Pass 2: dangling inputs. Rebuild producer set from outputs
	// (some outputs may have been renamed but this path doesn't
	// touch outputs, only inputs).
	producers := map[string]bool{}
	for _, s := range sow.Sessions {
		for _, out := range s.Outputs {
			producers[strings.ToLower(strings.TrimSpace(out))] = true
		}
	}
	fuzzy := func(in string) bool {
		key := strings.ToLower(strings.TrimSpace(in))
		if key == "" {
			return true // empty = skip
		}
		if producers[key] {
			return true
		}
		for pk := range producers {
			if strings.Contains(key, pk) || strings.Contains(pk, key) {
				return true
			}
		}
		return false
	}
	// Build fuzzy-rename map: for each dangling input, try to
	// substring-match to an existing producer output. Rename when
	// found (reconciles typo'd / paraphrased refs); only drop when
	// truly unresolvable. Preserves intent over silent loss.
	producerKeys := make([]string, 0, len(producers))
	for k := range producers {
		producerKeys = append(producerKeys, k)
	}
	closestProducer := func(in string) string {
		key := strings.ToLower(strings.TrimSpace(in))
		if key == "" {
			return ""
		}
		for _, pk := range producerKeys {
			if strings.Contains(key, pk) || strings.Contains(pk, key) {
				return pk
			}
		}
		// Word-overlap match: any 4+ char word shared.
		inWords := strings.Fields(key)
		for _, pk := range producerKeys {
			pkWords := strings.Fields(pk)
			for _, iw := range inWords {
				if len(iw) < 4 {
					continue
				}
				for _, pw := range pkWords {
					if iw == pw {
						return pk
					}
				}
			}
		}
		return ""
	}
	for i := range sow.Sessions {
		s := &sow.Sessions[i]
		kept := s.Inputs[:0]
		for _, in := range s.Inputs {
			if fuzzy(in) {
				kept = append(kept, in)
				continue
			}
			// Try fuzzy rename before dropping.
			if match := closestProducer(in); match != "" {
				kept = append(kept, match)
				continue
			}
			inputDrops++
			// Preserve intent: record the unresolvable input in the
			// session description so the operator + reviewer see
			// what the per-session expander wanted but couldn't
			// wire up. Without this, the intent evaporates.
			if !strings.Contains(s.Description, "UNRESOLVED INPUT") {
				s.Description = strings.TrimSuffix(s.Description, ".") + ". UNRESOLVED INPUTS (producer session missing): " + in
			} else {
				s.Description = strings.TrimSuffix(s.Description, ".") + ", " + in
			}
		}
		s.Inputs = kept
	}
	return fileDrops, outputRenames, inputDrops
}

// canonicalizeSessionInputs rewrites every session.Inputs entry so
// it matches the EXACT string a producer session declared in its
// Outputs. Without this, per-session expanders (running
// independently) emit paraphrased inputs like "design-tokens
// package" when the producer declared "design tokens" — the fuzzy
// DAG resolver only partially resolves these, falling back to
// declaration-order for the rest and serializing everything.
//
// Post-canonicalization the DAG resolver finds every input in its
// producer map (exact key match), so cross-session parallelism
// actually works. Example from run 32:
//
//   BEFORE: S4.Inputs = ["design-tokens package"]
//           S3-design-tokens.Outputs = ["design tokens"]
//           → fuzzy resolver returns empty → declaration-order
//             fallback serializes S4 behind S3-ui-mobile
//
//   AFTER:  S4.Inputs = ["design tokens"]
//           → exact match → S4 is ready as soon as
//             S3-design-tokens completes, even if S3-ui-mobile is
//             still running
//
// Pass runs AFTER semantic-merger file/output reconciliation and
// BEFORE ValidateSOW / DAG build. Returns count of rewrites for
// the log.
func canonicalizeSessionInputs(sow *SOW) int {
	if sow == nil {
		return 0
	}
	// Build producer-output-name index. Keys are normalized
	// (lowercase, whitespace-collapsed) for matching; values are
	// the ORIGINAL output strings to rewrite inputs to.
	outputIndex := map[string]string{} // normKey → originalOutput
	for _, s := range sow.Sessions {
		for _, out := range s.Outputs {
			trimmed := strings.TrimSpace(out)
			if trimmed == "" {
				continue
			}
			norm := normalizeArtifact(trimmed)
			if _, seen := outputIndex[norm]; !seen {
				outputIndex[norm] = trimmed
			}
		}
	}
	if len(outputIndex) == 0 {
		return 0
	}
	rewrites := 0
	for i := range sow.Sessions {
		s := &sow.Sessions[i]
		for j, in := range s.Inputs {
			trimmed := strings.TrimSpace(in)
			if trimmed == "" {
				continue
			}
			inNorm := normalizeArtifact(trimmed)
			// Exact normalized match: use the producer's original
			// spelling.
			if canonical, ok := outputIndex[inNorm]; ok {
				if canonical != in {
					s.Inputs[j] = canonical
					rewrites++
				}
				continue
			}
			// Substring fuzzy: find the first producer output whose
			// normalized form is contained in (or contains) the
			// input. Prefer longer producer keys to avoid
			// false-positives on common words like "app".
			var bestMatch string
			bestLen := 0
			for pk, orig := range outputIndex {
				if len(pk) <= bestLen {
					continue
				}
				if strings.Contains(inNorm, pk) || strings.Contains(pk, inNorm) {
					bestMatch = orig
					bestLen = len(pk)
				}
			}
			if bestMatch != "" && bestMatch != in {
				s.Inputs[j] = bestMatch
				rewrites++
			}
			// No match — leave as-is. repairChunkedConsistency's
			// pass-2 dangling-input handler already drops/flags
			// these.
		}
	}
	return rewrites
}


// checkChunkedConsistency validates cross-session contracts after
// chunked expansion:
//
//   1. Every session's Inputs must reference an Output declared by
//      an earlier session. Per-session expanders emit Inputs
//      speculatively based on the skeleton's cross-session hints;
//      stale/typo'd names surface here.
//   2. No two sessions declare ownership of the same file. When
//      the per-session expander for S2 and S3 both list
//      'packages/api-client/src/index.ts' in task.Files, they'll
//      race at dispatch.
//
// Returns human-readable issue lines; caller logs but does not
// abort (the execution layer's fuzzy DAG + session scheduler
// handle a lot of this gracefully, so strict rejection would be
// over-eager).
func checkChunkedConsistency(sow *SOW) []string {
	if sow == nil {
		return nil
	}
	var issues []string

	// Build producer map: normalized output-name → first session
	// that declared it.
	producers := map[string]string{}
	for _, s := range sow.Sessions {
		for _, out := range s.Outputs {
			key := strings.ToLower(strings.TrimSpace(out))
			if key == "" {
				continue
			}
			if _, seen := producers[key]; !seen {
				producers[key] = s.ID
			}
		}
	}

	// Inputs-resolve check.
	for _, s := range sow.Sessions {
		for _, in := range s.Inputs {
			key := strings.ToLower(strings.TrimSpace(in))
			if key == "" {
				continue
			}
			// Accept exact match OR substring both ways (matches the
			// fuzzy DAG resolver's semantics).
			if _, ok := producers[key]; ok {
				continue
			}
			matched := false
			for pk := range producers {
				if strings.Contains(key, pk) || strings.Contains(pk, key) {
					matched = true
					break
				}
			}
			if !matched {
				issues = append(issues, fmt.Sprintf("session %s input %q has no matching producer in earlier sessions — likely typo or missing session", s.ID, in))
			}
		}
	}

	// File-ownership check.
	fileOwners := map[string][]string{}
	for _, s := range sow.Sessions {
		for _, t := range s.Tasks {
			for _, f := range t.Files {
				f = strings.TrimSpace(f)
				if f == "" {
					continue
				}
				fileOwners[f] = append(fileOwners[f], s.ID)
			}
		}
	}
	for f, owners := range fileOwners {
		if len(owners) < 2 {
			continue
		}
		// Dedup session names (one session can have two tasks
		// touching the same file — that's fine).
		seen := map[string]bool{}
		unique := []string{}
		for _, o := range owners {
			if !seen[o] {
				seen[o] = true
				unique = append(unique, o)
			}
		}
		if len(unique) >= 2 {
			issues = append(issues, fmt.Sprintf("file %q claimed by %d sessions: %s", f, len(unique), strings.Join(unique, ", ")))
		}
	}

	return issues
}

// TerminalApprovalError signals that the chunked convert path
// produced a SOW the CTO reviewer flagged as terminally unfit
// (reject decision, OR blocking concerns that survived the refine
// loop, OR refine itself failed with blocking concerns
// outstanding). ConvertProseToSOW recognizes this error and does
// NOT fall back to the monolithic single-call convert — falling
// back would discard the reviewer verdict and produce a fresh
// unreviewed SOW that's strictly worse than surfacing the failure
// to the operator.
//
// Use errors.As to detect this from caller code.
type TerminalApprovalError struct {
	Reason string
}

func (e *TerminalApprovalError) Error() string { return e.Reason }

// coverageReviewPrompt asks the model to compare a reassembled SOW
// against the original prose and flag deliverables the prose
// mentions that no session covers. Returns JSON with any missing
// session stubs; zero-length missing list = full coverage.
const coverageReviewPrompt = `You are auditing whether a Statement of Work SESSION LIST fully covers a free-form project specification. Your job: flag any deliverable the PROSE mentions that no listed session covers.

Output ONLY the JSON object below — no prose, no markdown fences. Start with '{' and end with '}'.

{
  "missing": [
    {
      "id": "SNN",
      "phase": "foundation|core|integration",
      "title": "string",
      "description": "what this session delivers; tie back to the prose",
      "outputs": ["short artifact names, 2-4 words each"]
    }
  ]
}

RULES:
1. Only flag deliverables the prose EXPLICITLY describes. Don't invent scope.
2. If the existing session list covers every deliverable, emit {"missing": []}.
3. CRITICAL: Before adding a session, verify that NO existing session already covers the same deliverable — even partially, under a different title, or with different file paths. If an existing session covers 60%+ of a deliverable, it is NOT missing. Example: if S3 already implements "auth login flow", do NOT add a new session for "authentication and login". This is the #1 source of scope duplication and structural failure in the merged SOW.
4. Maximum 3 sessions per response. If you believe more than 3 are genuinely missing, list only the 3 most critical. The next coverage round will catch the rest.
5. Descriptions in 'missing' should reference prose verbatim where possible so the next expansion pass knows what to generate.
6. Keep the session.outputs terse (2-4 words); they form the DAG edges.
7. The "id" MUST be a plain identifier matching the regex ^S\d+(-[a-z0-9-]+)?$ — examples: "S23", "S24-shared", "S99". Do NOT include prose, hyphens-with-spaces, parentheticals, or any other text in the id field. Use the next unused integer after the highest existing S<number> in the session list above.

EXISTING SESSION LIST (id, title, description, outputs):
`

// reviewCoverageAndPatch fires one LLM call comparing the expanded
// SOW's session list against the original prose and appending any
// missing session stubs to the SOW. Returns the count of sessions
// added. Errors surface to the caller but aren't fatal — the run
// proceeds with partial coverage rather than aborting.
//
// New sessions added here are STUBS (no tasks, no ACs). A follow-up
// expansion call would be needed to fill them in — handled by the
// next run of ConvertProseToSOWChunked or deferred to the operator.
func reviewCoverageAndPatch(ctx context.Context, prose string, sow *SOW, prov provider.Provider, model string) (int, error) {
	if sow == nil || prov == nil {
		return 0, fmt.Errorf("nil sow or provider")
	}
	// Summarize existing sessions compactly.
	var listBuf strings.Builder
	for _, s := range sow.Sessions {
		fmt.Fprintf(&listBuf, "- [%s %s] %s — outputs: %s\n",
			s.ID, s.Phase, s.Title, strings.Join(s.Outputs, ", "))
	}
	userText := coverageReviewPrompt + listBuf.String() + "\n\nPROSE:\n" + prose
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	revCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	resp, err := callChatWithCtx(revCtx, prov, provider.ChatRequest{
		Model:     model,
		MaxTokens: 32000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return 0, err
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("coverage review empty (stop_reason=%q)", resp.StopReason)
	}
	var verdict struct {
		Missing []Session `json:"missing"`
	}
	if _, err := jsonutil.ExtractJSONInto(raw, &verdict); err != nil {
		return 0, fmt.Errorf("parse coverage verdict: %w", err)
	}
	if len(verdict.Missing) == 0 {
		return 0, nil
	}
	// Hard cap: even if the model lists 15 missing sessions
	// (ignoring the "max 3" prompt rule), we only process the first
	// 3 per round. Remaining genuine gaps will be caught in the next
	// coverage iteration. This prevents the scope-balloon effect
	// (run 39: 20→33→38 sessions → reject) where each round's
	// additions duplicate existing scope.
	const maxCoveragePerRound = 3
	if len(verdict.Missing) > maxCoveragePerRound {
		fmt.Printf("     ⚠ coverage reviewer flagged %d missing session(s); capping at %d per round\n", len(verdict.Missing), maxCoveragePerRound)
		verdict.Missing = verdict.Missing[:maxCoveragePerRound]
	}
	// Expand each missing session stub to full tasks + ACs using
	// the existing per-session expander. Drop on failure rather
	// than keeping a stub — tasks-less stubs fail ValidateSOW
	// downstream ('session X has no acceptance criteria') and force
	// a 20+ min monolith fallback. Coverage is best-effort; losing
	// a session here is preferable to losing the entire chunked
	// pipeline.
	// Sanitize stub IDs before expansion. The model occasionally
	// embeds the prompt's example text into the id field
	// ("S99 — next unused ID after the provided sessions"). Strip
	// anything past the first whitespace / em-dash / parenthesis
	// and validate against the canonical id shape; on invalid id,
	// synthesize the next unused S<N> from the existing session
	// IDs in the SOW.
	used := map[string]bool{}
	maxN := 0
	for _, s := range sow.Sessions {
		used[s.ID] = true
		if n := parseSessionNumber(s.ID); n > maxN {
			maxN = n
		}
	}
	added := 0
	for _, stub := range verdict.Missing {
		clean := sanitizeSessionID(stub.ID)
		if clean == "" || used[clean] {
			maxN++
			clean = fmt.Sprintf("S%d", maxN)
		}
		used[clean] = true
		stub.ID = clean
		full, xerr := expandSessionWithRetry(ctx, prose, &sow.Stack, stub, prov, model)
		if xerr != nil {
			fmt.Printf("     ⚠ coverage-review session %s DROPPED after expand failure: %v\n", stub.ID, xerr)
			fmt.Printf("       intended scope: %s — %s\n", stub.Title, stub.Description)
			continue
		}
		// expandSession may overwrite the ID from the model output;
		// re-sanitize and re-pin to the planner's clean id so the
		// model can't inject prose into the ID field on this pass
		// either.
		full.ID = clean
		sow.Sessions = append(sow.Sessions, full)
		added++
	}
	return added, nil
}

// sessionIDRE matches the canonical session ID shape used everywhere
// in the SOW pipeline. Anchored so the matcher must consume from
// the start; suffix is optional ("S23" or "S24-shared").
var sessionIDRE = regexp.MustCompile(`^S\d+(?:-[A-Za-z0-9-]+)?`)

// sanitizeSessionID extracts the canonical session ID from a possibly-
// prose-contaminated string. Examples handled:
//
//	"S99"                                                → "S99"
//	"S99 — next unused ID after the provided sessions"  → "S99"
//	"S24-shared (mobile)"                                → "S24-shared"
//	"  S15  "                                            → "S15"
//	"random prose"                                       → ""
//
// Returns "" when no canonical prefix is present; caller should
// synthesize a fresh id.
func sanitizeSessionID(id string) string {
	id = strings.TrimSpace(id)
	if m := sessionIDRE.FindString(id); m != "" {
		// Trim a trailing hyphen left over by suffix splitting.
		return strings.TrimRight(m, "-")
	}
	return ""
}

// parseSessionNumber returns the integer N from "S<N>..." or 0 when
// the id doesn't start with the canonical prefix.
func parseSessionNumber(id string) int {
	if !strings.HasPrefix(id, "S") {
		return 0
	}
	end := 1
	for end < len(id) && id[end] >= '0' && id[end] <= '9' {
		end++
	}
	if end == 1 {
		return 0
	}
	var n int
	fmt.Sscanf(id[1:end], "%d", &n)
	return n
}

// renumberTasksAndACsGlobally assigns each task + AC a globally
// unique ID in session/task order (T1, T2, ..., AC1, AC2, ...) and
// rewrites intra-session task.Dependencies via the oldID→newID
// map. Per-session expanders can't coordinate on numbering (they
// run independently) so two sessions often picked overlapping
// ranges like T30-T44 on both sides — ValidateSOW then rejected
// with "duplicate task ID across sessions". Renumbering fixes the
// collision without losing the expander output.
//
// Cross-session task Dependencies from the expanders are almost
// always speculative (the expander can't know the other session's
// final IDs) so they get dropped here. Real cross-session
// dependencies live in Session.Inputs/Outputs (artifact names, not
// task IDs) which the DAG resolver uses.
func renumberTasksAndACsGlobally(sow *SOW) {
	if sow == nil {
		return
	}
	taskNext := 1
	acNext := 1
	// Per-session oldID→newID for tasks; used to rewrite
	// Dependencies AFTER we've walked every task in the session.
	for si := range sow.Sessions {
		s := &sow.Sessions[si]
		oldToNew := map[string]string{}
		for ti := range s.Tasks {
			t := &s.Tasks[ti]
			oldID := strings.TrimSpace(t.ID)
			newID := fmt.Sprintf("T%d", taskNext)
			taskNext++
			oldToNew[oldID] = newID
			t.ID = newID
		}
		// Rewrite intra-session deps. Drop any dep that doesn't
		// resolve (cross-session speculative refs or dangling).
		for ti := range s.Tasks {
			t := &s.Tasks[ti]
			kept := t.Dependencies[:0]
			for _, dep := range t.Dependencies {
				dep = strings.TrimSpace(dep)
				if dep == "" {
					continue
				}
				if newDep, ok := oldToNew[dep]; ok {
					kept = append(kept, newDep)
				}
				// else: drop (cross-session or dangling)
			}
			t.Dependencies = kept
		}
		// Renumber ACs similarly. No structural dep to rewrite for
		// ACs in the current schema.
		for ai := range s.AcceptanceCriteria {
			ac := &s.AcceptanceCriteria[ai]
			ac.ID = fmt.Sprintf("AC%d", acNext)
			acNext++
		}
	}
}

// deadlineDuration returns how long the context allows from now
// until its deadline (or "no deadline" when it has none). Cosmetic
// helper for the timeout error message.
func deadlineDuration(ctx context.Context) time.Duration {
	if d, ok := ctx.Deadline(); ok {
		return time.Until(d).Round(time.Second)
	}
	return 0
}
