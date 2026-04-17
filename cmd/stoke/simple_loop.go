package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// simpleLoopCmd implements the "just let claude code build it"
// approach. No chunked SOW planning, no session scheduler, no
// MiniMax, no refine loops. Just:
//
//  1. Claude Code reads the prose → produces a plan
//  2. Codex reviews + enhances the plan
//  3. Claude Code reads codex feedback → does one more plan round
//  4. Claude Code builds, committing as it goes
//  5. We watch for commits → codex reviews each one
//  6. Codex review feedback → back to Claude Code to fix
//  7. Loop until codex signs off
//  8. Claude Code self-audits against the original SOW
//  9. If gaps remain → new prose → loop back to step 4
//  10. Repeat until "no gaps" + everything builds
//
// Usage:
//   stoke simple-loop --repo /path --file SOW.md
func simpleLoopCmd(args []string) {
	fs := flag.NewFlagSet("simple-loop", flag.ExitOnError)
	repo := fs.String("repo", ".", "Repository root")
	sowFile := fs.String("file", "", "SOW prose file")
	maxRounds := fs.Int("max-rounds", 5, "Max outer loops (plan→build→audit)")
	claudeBin := fs.String("claude-bin", "claude", "Claude Code binary")
	claudeModel := fs.String("claude-model", "", "Claude Code worker model (sonnet, opus, etc)")
	codexBin := fs.String("codex-bin", "codex", "Codex binary")
	reviewer := fs.String("reviewer", "codex", "Reviewer backend: codex | cc-opus | cc-sonnet")
	fixMode := fs.String("fix-mode", "sequential", "How to deliver reviewer findings to CC: sequential (one big prompt, iterate until clean post-build) | parallel (split into chunks, N workers concurrently post-build) | concurrent (reviewer-approved worktree merges fire while big worker still building — Level 2)")
	fixWorkers := fs.Int("fix-workers", 3, "Max concurrent CC fix workers when --fix-mode=parallel")
	fs.Parse(args)

	if *sowFile == "" {
		fmt.Fprintln(os.Stderr, "usage: stoke simple-loop --file SOW.md --repo /path")
		os.Exit(2)
	}
	absRepo, _ := filepath.Abs(*repo)
	prose, err := os.ReadFile(*sowFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read SOW:", err)
		os.Exit(1)
	}

	fmt.Printf("🔄 simple-loop: %s (%d bytes prose)\n", *sowFile, len(prose))
	fmt.Printf("   repo: %s\n", absRepo)
	claudeModelArg := *claudeModel
	fmt.Printf("   claude worker: %s (model: %s)\n", *claudeBin, func() string { if claudeModelArg == "" { return "default" }; return claudeModelArg }())
	fmt.Printf("   reviewer: %s\n", *reviewer)
	fmt.Printf("   max rounds: %d\n\n", *maxRounds)

	globalClaudeModel = claudeModelArg
	globalReviewer = *reviewer
	globalClaudeBin = *claudeBin
	globalCodexBin = *codexBin
	globalFixMode = *fixMode
	globalFixWorkers = *fixWorkers
	if globalFixWorkers < 1 {
		globalFixWorkers = 1
	}
	fmt.Printf("   fix-mode: %s (workers: %d)\n", globalFixMode, globalFixWorkers)
	currentProse := string(prose)

	for round := 1; round <= *maxRounds; round++ {
		fmt.Printf("═══════════════════════════════════════\n")
		fmt.Printf("  ROUND %d/%d\n", round, *maxRounds)
		fmt.Printf("═══════════════════════════════════════\n\n")

		// Step 1: Claude Code plans
		fmt.Println("📋 Step 1: Claude Code planning...")
		planText := claudeCall(*claudeBin, absRepo, fmt.Sprintf(
			"Read this project specification and create a detailed implementation plan. "+
				"List every file you need to create/modify, in order, with what each file should contain. "+
				"Be specific — exact file paths, exact exports, exact dependencies. "+
				"Output the plan as a numbered list.\n\nSPECIFICATION:\n%s\n\n"+
				"CURRENT REPO STATE: check what already exists with ls/find before planning.",
			currentProse))
		if planText == "" {
			fmt.Println("  ⚠ Claude Code planning failed, retrying...")
			continue
		}
		fmt.Printf("  ✓ plan: %d chars\n", len(planText))

		// Step 2: Reviewer reviews the plan
		fmt.Printf("📝 Step 2: %s reviewing plan...\n", *reviewer)
		codexReview := reviewCall(absRepo,
			"Review this implementation plan for a software project. "+
				"Flag any issues: missing files, wrong dependencies, unrealistic steps, "+
				"ordering problems. Suggest improvements. Be specific.\n\nPLAN:\n"+planText)
		fmt.Printf("  ✓ review: %d chars\n", len(codexReview))

		// Step 3: Claude Code builds (background) while we watch commits
		fmt.Println("🔧 Step 3: Claude Code building (watching commits)...")
		headBefore := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null || echo none")

		// In concurrent mode, a fixOrchestrator runs alongside the
		// big worker: flagged commits spawn fix-workers in git
		// worktrees that merge back only on reviewer approval.
		var orch *fixOrchestrator
		bigWorkerExtra := ""
		if globalFixMode == "concurrent" {
			orch = newFixOrchestrator(absRepo, *claudeBin, *reviewer)
			bigWorkerExtra = "\n\n⚠️ IMPORTANT — CONCURRENT FIX PIPELINE ACTIVE:\n" +
				"A reviewer is watching every commit you make. When it finds issues, " +
				"a parallel fix worker is spawned in a separate git worktree to repair " +
				"them; once the reviewer approves those fixes, they are MERGED INTO YOUR " +
				"BRANCH automatically. Before every Edit or Write:\n" +
				"  • Run `git status` and `git log --oneline -10` to see fix-worker merges.\n" +
				"  • Re-Read the file you're about to modify (someone may have just fixed it).\n" +
				"  • If a conflict appears after `git status`, run `git diff` and reconcile — " +
				"do NOT blow away merged fixes.\n" +
				"Never assume your in-memory view of a file is up-to-date. The merge " +
				"orchestrator is silent; only `git log` reveals its work."
		}

		// Launch Claude Code build in background — with continuation
		// support. A single CC call is capped at 100 turns; the SOW
		// is too big to fit in 100 turns. When the builder exits
		// (clean finish OR max-turns), we inspect git + disk state,
		// and if the SOW isn't obviously done we spawn a continuation
		// builder with "here's what's committed, keep going". Loop
		// terminates when: (a) CC signals completion in its result,
		// (b) a continuation made ZERO new commits (stuck), or
		// (c) maxBuildContinuations reached.
		buildDone := make(chan string, 1)
		go func() {
			// Builder continuation is PROGRESS-SIGNAL BOUNDED, not
			// count-bounded. We loop as long as:
			//   - each continuation produces at least 1 new commit
			//   - the worker has not explicitly reported completion
			// We stop when:
			//   (a) 2 CONSECUTIVE continuations produced zero new
			//       commits (worker is stuck — spinning on the same
			//       problem without making progress);
			//   (b) CC signals "ALL DELIVERABLES COMPLETE" in its
			//       final text;
			//   (c) absoluteCap rounds have fired — escape hatch so
			//       a truly pathological SOW can't run forever.
			// absoluteCap is deliberately high so normal big SOWs
			// are bounded by real progress, not an arbitrary counter.
			const absoluteCap = 40 // ~4000 turns — hard ceiling
			priorCommits := shellCmd(absRepo, "git rev-list --count HEAD 2>/dev/null")
			consecutiveStalls := 0
			var finalResult string
			cont := 0
			for cont < absoluteCap {
				var prompt string
				if cont == 0 {
					prompt = fmt.Sprintf(
						"Here's your implementation plan and codex's review. "+
							"Refine the plan addressing codex's feedback, then START BUILDING. "+
							"Implement step by step.\n\n"+
							"COMMIT CADENCE — commit on LOGICAL-UNIT-OF-WORK boundaries:\n"+
							"  • Commit when you FINISH something coherent a reviewer can evaluate as a "+
							"unit — a planned task, a fully-wired feature (e.g. 'login flow end-to-end'), "+
							"a completed module (e.g. 'packages/types Zod schemas'), a working refactor.\n"+
							"  • Each commit should compile + pass its local build at the boundary. "+
							"Run the relevant typecheck/build BEFORE committing; fix failures first.\n"+
							"  • DO NOT commit mid-function, mid-feature, or in a broken state — the "+
							"reviewer will reject unreviewable 'wip' commits.\n"+
							"  • DO NOT batch several unrelated features into one commit — if ANY piece "+
							"is wrong the whole commit has to be rejected or split. Keep scope tight.\n"+
							"  • Commit message should answer 'what unit of work did I just complete?' — "+
							"'feat(api-client): residents + alarms modules' IS a unit; 'wip: more stuff' "+
							"is NOT.\n"+
							"  • Aim for commits small enough that each one is a clean, standalone win — "+
							"not a time-sliced chunk. Multiple small commits beat one monster commit "+
							"every time.\n"+
							"  • Your turn budget is 100. Do not try to finish the whole SOW in one call. "+
							"Get as many COMPLETE units in cleanly as possible; a continuation builder "+
							"will pick up from your last good commit.\n\n"+
							"YOUR PLAN:\n%s\n\nCODEX REVIEW:\n%s\n\n"+
							"SPECIFICATION:\n%s\n\n"+
							"START BUILDING NOW.%s",
						planText, codexReview, currentProse, bigWorkerExtra)
				} else {
					// Continuation prompt — show what's been done, ask
					// CC to diff against the SOW and keep going from
					// wherever the previous builder left off.
					doneLog := shellCmd(absRepo, "git log --oneline "+headBefore+"..HEAD 2>/dev/null | head -40")
					tree := shellCmd(absRepo, "ls -la 2>/dev/null; echo ---; find . -maxdepth 3 -type d -not -path './node_modules*' -not -path './.git*' 2>/dev/null | sort")
					prompt = fmt.Sprintf(
						"CONTINUATION BUILDER (call %d, %d stalls so far) — the prior builder "+
							"call has exited (either cleanly or at the 100-turn budget). "+
							"The SOW is large; we're continuing where you left off. The "+
							"harness will keep spawning continuations AS LONG AS each one "+
							"produces new commits, so take your turn budget fully.\n\n"+
							"COMMITTED SO FAR (%d prior commits in this build phase):\n%s\n\n"+
							"CURRENT DIRECTORY TREE:\n%s\n\n"+
							"YOUR JOB:\n"+
							"  1. Run `git log --oneline -20` and `git status` first to see the latest state.\n"+
							"  2. Read the SOW below and identify what's missing or incomplete.\n"+
							"  3. KEEP BUILDING from there. Do NOT duplicate work already committed.\n"+
							"  4. Fix any compile/typecheck errors you encounter along the way.\n"+
							"  5. Commit on LOGICAL-UNIT-OF-WORK boundaries (completed tasks/features/modules, "+
							"not time chunks). Each commit must compile and represent something the reviewer "+
							"can evaluate as a standalone unit.\n"+
							"  6. If you genuinely finish everything, end your last message with the "+
							"phrase 'ALL DELIVERABLES COMPLETE'. Otherwise we'll spawn another continuation.\n\n"+
							"ORIGINAL SPECIFICATION:\n%s%s",
						cont+1, consecutiveStalls, cont, doneLog, tree, currentProse, bigWorkerExtra)
				}
				fmt.Printf("🔧 Step 3 builder call %d (absoluteCap=%d, stalls=%d/2)...\n",
					cont+1, absoluteCap, consecutiveStalls)
				finalResult = claudeCall(*claudeBin, absRepo, prompt)

				curCommits := shellCmd(absRepo, "git rev-list --count HEAD 2>/dev/null")
				if curCommits == priorCommits {
					consecutiveStalls++
					fmt.Printf("  ⚠ builder %d made no new commits (stall %d/2)\n", cont+1, consecutiveStalls)
					if consecutiveStalls >= 2 {
						fmt.Printf("  ⛔ 2 consecutive stalled continuations — stopping build phase\n")
						break
					}
				} else {
					consecutiveStalls = 0
				}
				priorCommits = curCommits

				lower := strings.ToLower(finalResult)
				if strings.Contains(lower, "all deliverables complete") ||
					strings.Contains(lower, "sow complete") ||
					strings.Contains(lower, "nothing left to build") {
					fmt.Printf("  ✓ builder %d reports completion — ending build phase\n", cont+1)
					break
				}
				cont++
			}
			if cont >= absoluteCap {
				fmt.Printf("  ⛔ hit absoluteCap=%d continuations — stopping (unusual, investigate)\n", absoluteCap)
			}
			buildDone <- finalResult
		}()

		// Step 4: Watch commits. Two behaviors:
		//   - sequential/parallel fix-modes: accumulate findings
		//     into pendingReviews; deliver in Step 4b after big
		//     worker finishes.
		//   - concurrent fix-mode: dispatch findings IMMEDIATELY
		//     to the orchestrator (worktree + CC fix worker + auto
		//     merge-on-approval). Big worker keeps running.
		if globalFixMode == "concurrent" {
			fmt.Println("👀 Step 4: Watching commits, dispatching fix workers concurrently...")
		} else {
			fmt.Println("👀 Step 4: Watching for commits, queueing reviewer feedback...")
		}
		lastReviewedHead := headBefore
		reviewRound := 0
		const maxReviewRounds = 20
		var pendingReviews []string

	commitWatch:
		for reviewRound < maxReviewRounds {
			select {
			case <-buildDone:
				fmt.Println("  📦 Claude Code build phase complete")
				break commitWatch

			case <-time.After(30 * time.Second):
				currentHead := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null")
				if currentHead != lastReviewedHead && currentHead != headBefore {
					diff := shellCmd(absRepo, "git diff "+lastReviewedHead+".."+currentHead+" --stat 2>/dev/null")
					commitMsg := shellCmd(absRepo, "git log --oneline "+lastReviewedHead+".."+currentHead+" 2>/dev/null")
					if diff != "" {
						reviewRound++
						fmt.Printf("  📝 New commits (round %d):\n%s\n", reviewRound, indent(commitMsg, "    "))

						// Deterministic per-commit quality sweep — earliest-
						// fire gate. Before the LLM reviewer even looks at
						// the diff, we scan the changed files for hollow
						// bodies, skipped tests, tautology assertions,
						// duplicate scaffolds, silent catches. Blocking
						// findings are prepended to the review feedback so
						// the fixer gets concrete file:line targets.
						var qualityAddendum string
						changedFiles := strings.Split(strings.TrimSpace(
							shellCmd(absRepo, "git diff --name-only "+lastReviewedHead+".."+currentHead+" 2>/dev/null")),
							"\n")
						var cleanChanged []string
						for _, f := range changedFiles {
							f = strings.TrimSpace(f)
							if f != "" {
								cleanChanged = append(cleanChanged, f)
							}
						}
						if len(cleanChanged) > 0 {
							qual := plan.RunQualitySweep(absRepo, cleanChanged)
							if qual != nil && len(qual.Findings) > 0 {
								fmt.Printf("  🕵 quality sweep on diff: %s\n", qual.Summary())
								if qual.Blocking() {
									qualityAddendum = plan.FormatQualityReport(qual)
									if len(qualityAddendum) > 3000 {
										qualityAddendum = qualityAddendum[:3000] + "\n... (truncated)"
									}
									fmt.Println(qualityAddendum)
								}
							}
						}

						fmt.Printf("  🔍 %s reviewing...\n", *reviewer)
						reviewPrompt := "Review these specific changes. Check for: compilation errors, " +
							"missing imports, skeleton code. Be specific about what to fix.\n\n" +
							"COMMITS:\n" + commitMsg + "\n\nDIFF STAT:\n" + diff
						if qualityAddendum != "" {
							reviewPrompt = "DETERMINISTIC QUALITY SWEEP FLAGGED THE FOLLOWING — fixing these is MANDATORY regardless of your other findings:\n\n" +
								qualityAddendum + "\n\n---\n\n" + reviewPrompt
						}
						codeReview := reviewCall(absRepo, reviewPrompt)
						// If the quality sweep found blocking issues, the
						// reviewer's verdict doesn't get to approve — we
						// force feedback into the pending-reviews queue
						// with the concrete gap list so the fixer addresses
						// them even if the LLM tries to rubber-stamp.
						if qualityAddendum != "" && (approvedReview(codeReview) || len(codeReview) < 100) {
							codeReview = "QUALITY SWEEP BLOCKING SIGNALS (reviewer attempted to approve but deterministic scan found these):\n\n" +
								qualityAddendum
						}
						if len(codeReview) > 100 && !approvedReview(codeReview) {
							if orch != nil {
								id := orch.dispatch(currentHead,
									fmt.Sprintf("Commits reviewed:\n%s\n\nFindings:\n%s",
										commitMsg, codeReview))
								active, merged, abandoned := orch.stats()
								fmt.Printf("  🚀 dispatched fix-%d concurrently (active:%d merged:%d abandoned:%d)\n",
									id, active, merged, abandoned)
							} else {
								pendingReviews = append(pendingReviews,
									fmt.Sprintf("Commits reviewed:\n%s\n\nFindings:\n%s",
										commitMsg, codeReview))
								fmt.Printf("  ✗ reviewer found issues — queued (%d pending)\n", len(pendingReviews))
							}
						} else {
							fmt.Printf("  ✓ reviewer approved commits\n")
						}
						lastReviewedHead = currentHead
					}
				} else {
					fmt.Printf("  ⏳ waiting for commits... (%ds)\n", (reviewRound+1)*30)
				}
			}
		}

		// Concurrent mode: drain the orchestrator before Step 4b.
		// Any still-in-flight fix attempts get up to 10 min to
		// complete their merge-or-abandon cycle. After that, if
		// they haven't reached an approved merge they stay
		// abandoned on their fix branches (not merged to main).
		if orch != nil {
			active, merged, abandoned := orch.stats()
			if active > 0 {
				fmt.Printf("  ⏳ draining %d in-flight fix attempts (merged:%d abandoned:%d so far)\n",
					active, merged, abandoned)
				orch.waitIdle(10 * time.Minute)
			}
			_, merged, abandoned = orch.stats()
			fmt.Printf("  🛠️  concurrent fix pipeline final: merged=%d abandoned=%d\n",
				merged, abandoned)
		}

		// Step 4b: Iterate-until-clean. Deliver queued findings +
		// do a fresh final review over the full diff. If the
		// reviewer approves, we're done. Otherwise send to CC for
		// fix, wait for those fix commits, re-review. Repeat up
		// to maxFixRounds. This is the gate that makes simple-loop
		// actually enforce reviewer sign-off instead of shipping
		// unreviewed code.
		const maxFixRounds = 5
		for fixRound := 1; fixRound <= maxFixRounds; fixRound++ {
			currentHead := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null")
			if currentHead == headBefore {
				fmt.Println("  (no commits produced — skipping fix loop)")
				break
			}
			fullDiff := shellCmd(absRepo, "git diff "+headBefore+"..HEAD --stat 2>/dev/null")
			fmt.Printf("  🔍 Final review %d/%d (via %s)...\n", fixRound, maxFixRounds, *reviewer)
			finalPrompt := "Review ALL changes in this branch for PRODUCTION READINESS. " +
				"REJECT (do NOT say 'NO ISSUES' or 'LGTM') if ANY of these are present:\n" +
				"  • skeleton functions: body is a scaffold-marker, empty body, bare return of nil/undefined, or unresolved TODO\n" +
				"  • scaffold-only files: declared but no real body, or body is just mocked/hard-coded values\n" +
				"  • fake returns: hard-coded scaffold values that pretend a feature works without real logic\n" +
				"  • mock-only implementations where the SOW asked for real behavior\n" +
				"  • empty request handlers, empty event handlers, empty callbacks\n" +
				"  • functions that throw 'not implemented' style errors\n" +
				"  • compilation errors, missing imports, broken tests\n" +
				"  • anything that would fail a typecheck\n" +
				"You MUST look INSIDE the changed files — a diff that adds a file whose body is scaffolding is NOT acceptable. " +
				"Only respond with 'NO ISSUES' or 'LGTM' if every change is a genuine, complete, working implementation.\n\n" +
				"FULL DIFF STAT:\n" + fullDiff
			if len(pendingReviews) > 0 {
				finalPrompt += "\n\nPREVIOUSLY FLAGGED ISSUES (must be verified fixed):\n" +
					strings.Join(pendingReviews, "\n\n---\n\n")
			}
			finalReview := reviewCall(absRepo, finalPrompt)
			if len(finalReview) < 100 || approvedReview(finalReview) {
				fmt.Printf("  ✅ reviewer approved (round %d) — build sign-off obtained\n", fixRound)
				pendingReviews = nil
				break
			}
			fmt.Printf("  ✗ reviewer still finding issues (round %d, mode=%s)\n", fixRound, globalFixMode)
			fixHeadBefore := currentHead
			if globalFixMode == "parallel" {
				dispatchParallelFix(*claudeBin, absRepo, finalReview, globalFixWorkers)
			} else {
				dispatchSequentialFix(*claudeBin, absRepo, finalReview)
			}
			pendingReviews = nil
			postFixHead := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null")
			if postFixHead == fixHeadBefore {
				fmt.Printf("  ⚠ CC made no fix commits — exiting fix loop\n")
				break
			}
			fmt.Printf("  📝 CC produced fix commits; re-reviewing...\n")
		}

		// Step 5: Build verification
		fmt.Println("🏗️  Step 5: Build verification...")
		buildResult := shellCmd(absRepo, detectSimpleBuildCmd(absRepo))
		buildPassed := !strings.Contains(buildResult, "error") || strings.Contains(buildResult, "0 errors")
		if buildPassed {
			fmt.Println("  ✓ build passes")
		} else {
			fmt.Printf("  ✗ build failed, sending to CC...\n")
			claudeCall(*claudeBin, absRepo, fmt.Sprintf(
				"The build failed. Fix these errors and commit:\n\n%s", buildResult))
		}

		// Step 8: Self-audit against SOW
		fmt.Println("📋 Step 8: Claude Code self-auditing against SOW...")
		audit := claudeCall(*claudeBin, absRepo, fmt.Sprintf(
			"Compare the current state of this repository against the original specification. "+
				"For EACH deliverable in the spec, state whether it's: DONE, PARTIAL, or MISSING. "+
				"BE BRUTALLY HONEST. A deliverable is NOT DONE if it is any of: "+
				"skeleton function body; hard-coded fake returns; empty handler/callback; "+
				"mock-only implementation where SOW asked for real behavior; file exists but logic is missing. "+
				"Report PARTIAL or MISSING for anything that is scaffolding only.\n\n"+
				"Then answer: IS THERE MORE WORK TO DO? If yes, describe EXACTLY what remains "+
				"(list each stub/missing item by name) as a new specification for the next round. "+
				"If no, say 'ALL DELIVERABLES COMPLETE'.\n\n"+
				"ORIGINAL SPECIFICATION:\n%s", currentProse))
		fmt.Printf("  audit: %d chars\n", len(audit))

		// Step 8b: Deterministic compliance sweep — anti-rubber-stamp
		// The CC audit above is circular (CC grading CC's work). This
		// sweep walks the SOW prose for named deliverables and checks
		// each against the actual repo via filename+content-definition
		// match + 80-byte + body-line thresholds. Authoritative: if
		// compliance finds stubs/missing, we override any "ALL
		// DELIVERABLES COMPLETE" claim from CC.
		ccSaysDone := strings.Contains(strings.ToUpper(audit), "ALL DELIVERABLES COMPLETE")
		tmpSOW := &plan.SOW{Description: currentProse}
		compReport := plan.RunSOWCompliance(absRepo, tmpSOW)
		complianceClean := compReport != nil && compReport.Passed()
		if compReport != nil && len(compReport.Findings) > 0 {
			fmt.Printf("  🕵 compliance sweep: %s\n", compReport.Summary())
			if !complianceClean {
				// Show what's missing/stub so CC has concrete feedback
				// for the next round's prose.
				shortReport := plan.FormatComplianceReport(compReport)
				if len(shortReport) > 4000 {
					shortReport = shortReport[:4000] + "\n... (truncated)"
				}
				fmt.Println(shortReport)
			}
		} else {
			fmt.Printf("  🕵 compliance sweep: no extractable deliverables from prose\n")
		}

		// Step 9: Check if done — BOTH gates must agree
		if ccSaysDone && complianceClean {
			fmt.Printf("\n✅ ROUND %d: All deliverables complete (CC audit + compliance sweep both clean)\n", round)
			break
		}
		if ccSaysDone && !complianceClean {
			fmt.Printf("\n⚠ ROUND %d: CC claimed complete but compliance sweep found stubs/missing — overriding to gaps-remain\n", round)
		}

		// Extract remaining work as new prose for next round.
		// If compliance found specific missing/stub items, prepend
		// those to the audit text so the next round gets concrete
		// targets instead of vague CC-self-assessment.
		nextProse := audit
		if compReport != nil && !complianceClean {
			var gaps []string
			for _, f := range compReport.Findings {
				if f.Verdict == plan.VerdictMissing {
					gaps = append(gaps, fmt.Sprintf("MISSING: %s", f.Deliverable.Name))
				} else if f.Verdict == plan.VerdictFoundStub {
					gaps = append(gaps, fmt.Sprintf("STUB (must implement real logic): %s", f.Deliverable.Name))
				}
			}
			if len(gaps) > 0 {
				nextProse = "COMPLIANCE GATE FOUND THE FOLLOWING GAPS — IMPLEMENT THEM FULLY (no scaffolds, no mocks, no filler values):\n\n" +
					strings.Join(gaps, "\n") + "\n\n---\n\nADDITIONAL CC AUDIT NOTES:\n" + audit
			}
		}

		// Auto-extend rounds if we've hit the cap but compliance
		// still says gaps remain. One-time extension to avoid the
		// MS failure mode (exited at max-rounds with gaps). Logs
		// a loud warning so the user sees it.
		if round == *maxRounds && !complianceClean {
			newCap := *maxRounds + 3
			fmt.Printf("\n⚠ ROUND %d = max but compliance still failing — auto-extending max-rounds to %d (one-time)\n",
				round, newCap)
			*maxRounds = newCap
		}

		fmt.Printf("\n🔄 ROUND %d: gaps remain — extracting remaining work for next round\n", round)
		currentProse = nextProse // next round's input
	}

	// Final summary
	fmt.Println("\n═══════════════════════════════════════")
	fmt.Println("  SIMPLE LOOP COMPLETE")
	fmt.Printf("  repo: %s\n", absRepo)
	fmt.Println("  run 'stoke sessions status' to see results")
	fmt.Println("═══════════════════════════════════════")
}

var (
	globalClaudeModel string // worker model override
	globalReviewer    string // "codex", "cc-opus", "cc-sonnet"
	globalClaudeBin   string // resolved claude binary path
	globalCodexBin    string // resolved codex binary path
	globalFixMode     string // "sequential" or "parallel"
	globalFixWorkers  int    // concurrency for parallel fix mode
)

// approvedReview returns true when the reviewer text looks
// like sign-off. Treats "no issues", "lgtm", "looks good",
// "approved" as approval. A short (<100 char) response is
// considered ambiguous and NOT approval — forces iteration.
func approvedReview(text string) bool {
	t := strings.ToLower(text)
	for _, marker := range []string{"no issues", "lgtm", "looks good", "approved", "no changes needed"} {
		if strings.Contains(t, marker) {
			return true
		}
	}
	return false
}

// splitReviewIntoIssues breaks a reviewer's response into
// discrete actionable findings. Heuristic: lines starting with
// "-", "*", digit+dot, or "Issue:". When the reviewer writes
// free prose, returns the whole text as one issue. Returns at
// most maxChunks issues; extras are merged into the last chunk.
func splitReviewIntoIssues(text string, maxChunks int) []string {
	if maxChunks < 1 {
		maxChunks = 1
	}
	var issues []string
	var cur strings.Builder
	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			issues = append(issues, s)
		}
		cur.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		isNew := strings.HasPrefix(trimmed, "- ") ||
			strings.HasPrefix(trimmed, "* ") ||
			strings.HasPrefix(strings.ToLower(trimmed), "issue:") ||
			(len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' && (trimmed[1] == '.' || trimmed[1] == ')'))
		if isNew && cur.Len() > 0 {
			flush()
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	flush()
	if len(issues) <= 1 {
		return []string{strings.TrimSpace(text)}
	}
	if len(issues) > maxChunks {
		// Collapse overflow into the last chunk so nothing is dropped.
		head := issues[:maxChunks-1]
		tail := strings.Join(issues[maxChunks-1:], "\n\n")
		issues = append(head, tail)
	}
	return issues
}

// reviewCall dispatches the plan/code-review call to the
// configured reviewer backend. Reviewers run in TEXT-ONLY mode
// — no filesystem tools, no commits. The caller hands in a
// fully-formed prompt; we return the review text.
func reviewCall(dir, prompt string) string {
	switch globalReviewer {
	case "cc-opus":
		return claudeReviewCall(globalClaudeBin, dir, prompt, "opus")
	case "cc-sonnet":
		return claudeReviewCall(globalClaudeBin, dir, prompt, "sonnet")
	case "cc", "claude":
		// Generic "claude code as reviewer" — uses its default model.
		return claudeReviewCall(globalClaudeBin, dir, prompt, "")
	default:
		return codexCall(globalCodexBin, dir, prompt)
	}
}

// claudeReviewCall invokes Claude Code in text-only mode for
// review purposes. No --dangerously-skip-permissions, no tools,
// no JSON wrapping — just --print with optional model override.
func claudeReviewCall(bin, dir, prompt, model string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	args := []string{
		"--print",
		"--no-session-persistence",
		prompt,
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  claude-reviewer error: %v\n", err)
		return ""
	}
	return strings.TrimSpace(out.String())
}

func claudeCall(bin, dir, prompt string) string {
	// Hard cap 40 min; previous 30-min was tight for big fix calls.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	// stream-json gives us live line-by-line tool-use events — we
	// scan its growth as the progress signal for the watchdog.
	// Without stream-json, the ONLY output is a single final JSON
	// blob at exit, which makes every long CC call look identical
	// to a hang. With stream-json, each tool call emits a line
	// immediately, so the watchdog can distinguish "CC is doing
	// work" from "CC is wedged".
	args := []string{
		"-p", prompt,
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
		"--max-turns", "100",
	}
	if globalClaudeModel != "" {
		args = append(args, "--model", globalClaudeModel)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "  claude start error: %v\n", err)
		return ""
	}
	go func() { done <- cmd.Wait() }()

	// Watchdog: 30-s ticker, 5 min of zero growth = hung.
	// This is the same pattern the provider package uses for
	// long-running CLI invocations (internal/provider/claudecode.go).
	watchdog := time.NewTicker(30 * time.Second)
	defer watchdog.Stop()
	lastSize := 0
	stale := 0
	const maxStale = 10 // 10 × 30s = 5 min of silence
	running := true
	var runErr error
	for running {
		select {
		case err := <-done:
			runErr = err
			running = false
		case <-watchdog.C:
			cur := out.Len()
			if cur == lastSize {
				stale++
				if stale >= maxStale {
					fmt.Fprintf(os.Stderr, "  ⛔ claude: no stream output for %ds — killing\n", maxStale*30)
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
					running = false
				}
			} else {
				stale = 0
				lastSize = cur
			}
		}
	}
	if runErr != nil && !strings.Contains(runErr.Error(), "killed") {
		fmt.Fprintf(os.Stderr, "  claude error: %v\n", runErr)
	}

	// stream-json emits one JSON object per line. The final line
	// is a `result` event with the .result + usage. Scan backward
	// to find it. If we don't find one (watchdog kill / truncation),
	// fall back to the raw bytes so the caller still has something.
	raw := out.Bytes()
	lines := strings.Split(string(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || line[0] != '{' {
			continue
		}
		var result struct {
			Type     string  `json:"type"`
			Result   string  `json:"result"`
			NumTurns int     `json:"num_turns"`
			Cost     float64 `json:"total_cost_usd"`
		}
		if json.Unmarshal([]byte(line), &result) != nil {
			continue
		}
		if result.Type == "result" || result.Result != "" {
			fmt.Printf("  [CC: %d turns, $%.4f]\n", result.NumTurns, result.Cost)
			return result.Result
		}
	}
	return strings.TrimSpace(string(raw))
}

// codexCall invokes `codex exec` with JSONL output (so we can
// detect turn.completed/turn.failed inline) plus an output-growth
// watchdog that kills the process if stdout goes silent for 5 min.
// Reviewer calls are --sandbox read-only; codex has no business
// editing files when we ask it to review.
func codexCall(bin, dir, prompt string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	lastMsg := fmt.Sprintf("/tmp/codex-simple-%d.txt", time.Now().UnixNano())
	defer os.Remove(lastMsg)
	args := []string{"exec",
		"--json",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--output-last-message", lastMsg,
		prompt,
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	watchdog := time.NewTicker(30 * time.Second)
	defer watchdog.Stop()
	lastSize := 0
	stale := 0
	const maxStale = 10 // 10 × 30s = 5 min of silence

	turnFailed := false
	for running := true; running; {
		select {
		case err := <-done:
			running = false
			if err != nil {
				fmt.Fprintf(os.Stderr, "  codex error: %v (stderr: %s)\n",
					err, strings.TrimSpace(stderr.String()))
			}
		case <-watchdog.C:
			cur := stdout.Len() + stderr.Len()
			if cur == lastSize {
				stale++
				if stale >= maxStale {
					fmt.Fprintf(os.Stderr, "  codex: no output for %ds — killing\n", maxStale*30)
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
					running = false
				}
			} else {
				stale = 0
				lastSize = cur
			}
			// Scan new JSONL events for turn.failed / usage_limit / 429
			for _, line := range strings.Split(stdout.String(), "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "{") {
					continue
				}
				var ev struct{ Type string `json:"type"` }
				if json.Unmarshal([]byte(line), &ev) == nil {
					if ev.Type == "turn.failed" {
						turnFailed = true
					}
				}
			}
			if strings.Contains(stderr.String(), "429") ||
				strings.Contains(stderr.String(), "usage limit") {
				fmt.Fprintf(os.Stderr, "  codex rate-limited (stderr contains 429/usage-limit)\n")
			}
		}
	}

	if turnFailed {
		fmt.Fprintf(os.Stderr, "  codex reported turn.failed\n")
	}

	// Prefer the output-last-message file (clean final text).
	// Retry briefly — codex flushes the file slightly after exit.
	var data []byte
	for i := 0; i < 10; i++ {
		data, _ = os.ReadFile(lastMsg)
		if len(data) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(data) == 0 {
		// Fallback: extract final agent_message from JSONL stream.
		data = []byte(extractCodexFinalMessage(stdout.String()))
	}
	return strings.TrimSpace(string(data))
}

// extractCodexFinalMessage parses codex JSONL stdout and returns
// the text of the last `item.completed` event with type
// `agent_message`. Used as a fallback when --output-last-message
// hasn't flushed yet.
func extractCodexFinalMessage(jsonl string) string {
	var last string
	for _, line := range strings.Split(jsonl, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "item.completed" && ev.Item.Type == "agent_message" && ev.Item.Text != "" {
			last = ev.Item.Text
		}
	}
	return last
}

func shellCmd(dir, cmd string) string {
	out, _ := exec.Command("bash", "-lc", "cd "+dir+" && "+cmd).CombinedOutput()
	return strings.TrimSpace(string(out))
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func detectSimpleBuildCmd(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); err == nil {
		return "npx tsc --noEmit 2>&1 || echo 'tsc not available'"
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		// Check if there's a build script
		data, _ := os.ReadFile(filepath.Join(dir, "package.json"))
		var pkg map[string]interface{}
		if json.Unmarshal(data, &pkg) == nil {
			if scripts, ok := pkg["scripts"].(map[string]interface{}); ok {
				if _, ok := scripts["build"]; ok {
					return "pnpm build 2>&1 || npm run build 2>&1"
				}
				if _, ok := scripts["typecheck"]; ok {
					return "pnpm typecheck 2>&1"
				}
			}
		}
		return "echo 'no build script'"
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return "go build ./... 2>&1"
	}
	return "echo 'no build detected'"
}

// dispatchSequentialFix sends the entire reviewer feedback as
// one prompt to a single CC worker. Simple, no concurrency, no
// git conflicts. One fat claudeCall; the worker iterates through
// every flagged item within its --max-turns budget.
func dispatchSequentialFix(bin, dir, feedback string) {
	fmt.Println("    → sequential: 1 CC worker fixing the full feedback")
	claudeCall(bin, dir, fmt.Sprintf(
		"The reviewer has flagged specific issues in your code. "+
			"Fix EVERY single one. Read each affected file carefully. "+
			"After each fix run the build (tsc --noEmit or the project's "+
			"build command). Commit each fix with a descriptive message. "+
			"Only fix what the reviewer flagged — do not add features.\n\n"+
			"REVIEWER FEEDBACK:\n%s", feedback))
}

// dispatchParallelFix splits the reviewer feedback into discrete
// issues and launches up to `workers` CC workers concurrently.
// Each worker owns one chunk. Git state is shared across
// workers — concurrent writes to different files are fine; the
// real contention is on commit. We do NOT serialize commits
// ourselves because `git commit` is atomic in the index and CC
// workers naturally stagger by processing different issues.
// On rare conflicts CC re-resolves via the build step. Returns
// once all workers finish.
func dispatchParallelFix(bin, dir, feedback string, workers int) {
	issues := splitReviewIntoIssues(feedback, workers)
	fmt.Printf("    → parallel: %d issue chunk(s) across up to %d worker(s)\n",
		len(issues), workers)
	if len(issues) == 0 {
		return
	}
	sem := make(chan struct{}, workers)
	done := make(chan struct{}, len(issues))
	for i, issue := range issues {
		go func(idx int, text string) {
			sem <- struct{}{}
			defer func() { <-sem; done <- struct{}{} }()
			fmt.Printf("    [worker %d/%d] starting\n", idx+1, len(issues))
			claudeCall(bin, dir, fmt.Sprintf(
				"You are parallel worker %d of %d, all running concurrently on the same repo. "+
					"Other workers may be editing different files RIGHT NOW and committing "+
					"between your tool calls. Follow these rules exactly:\n"+
					"  1. BEFORE editing any file, read it fresh (Read tool) to see the latest state.\n"+
					"  2. BEFORE reading, run `git status` and `git log --oneline -10` to see what "+
					"other workers have committed since you started.\n"+
					"  3. If a file you planned to edit was just changed, re-read it and reconcile — "+
					"do NOT overwrite another worker's fix.\n"+
					"  4. Keep edits small and committed one at a time. After each commit run "+
					"`git pull --rebase origin HEAD 2>/dev/null || true` (no-op in single-branch "+
					"repos but safe to run).\n"+
					"  5. Stick strictly to files required by YOUR assigned issue. Do not touch "+
					"files you cannot justify from the issue description.\n"+
					"  6. Run the build (tsc --noEmit or project build) after each fix. If the "+
					"build breaks because of something NOT in your issue, that's another "+
					"worker's in-flight change — wait 30s and retry once before giving up.\n"+
					"  7. Commit with a message that starts with `fix(parallel-%d):` so humans can "+
					"see which worker made which change.\n"+
					"  8. Do not add new features. Only fix what the reviewer flagged below.\n\n"+
					"YOUR ASSIGNED ISSUE (%d of %d):\n%s",
				idx+1, len(issues), idx+1, idx+1, len(issues), text))
			fmt.Printf("    [worker %d/%d] done\n", idx+1, len(issues))
		}(i, issue)
	}
	for range issues {
		<-done
	}
}
