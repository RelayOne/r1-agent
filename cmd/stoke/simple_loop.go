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
	fixMode := fs.String("fix-mode", "sequential", "How to deliver reviewer findings to CC: sequential (one big prompt, iterate until clean) | parallel (split into chunks, run N workers concurrently)")
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
		plan := claudeCall(*claudeBin, absRepo, fmt.Sprintf(
			"Read this project specification and create a detailed implementation plan. "+
				"List every file you need to create/modify, in order, with what each file should contain. "+
				"Be specific — exact file paths, exact exports, exact dependencies. "+
				"Output the plan as a numbered list.\n\nSPECIFICATION:\n%s\n\n"+
				"CURRENT REPO STATE: check what already exists with ls/find before planning.",
			currentProse))
		if plan == "" {
			fmt.Println("  ⚠ Claude Code planning failed, retrying...")
			continue
		}
		fmt.Printf("  ✓ plan: %d chars\n", len(plan))

		// Step 2: Reviewer reviews the plan
		fmt.Printf("📝 Step 2: %s reviewing plan...\n", *reviewer)
		codexReview := reviewCall(absRepo,
			"Review this implementation plan for a software project. "+
				"Flag any issues: missing files, wrong dependencies, unrealistic steps, "+
				"ordering problems. Suggest improvements. Be specific.\n\nPLAN:\n"+plan)
		fmt.Printf("  ✓ review: %d chars\n", len(codexReview))

		// Step 3: Claude Code builds (background) while we watch commits
		fmt.Println("🔧 Step 3: Claude Code building (watching commits)...")
		headBefore := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null || echo none")

		// Launch Claude Code build in background
		buildDone := make(chan string, 1)
		go func() {
			result := claudeCall(*claudeBin, absRepo, fmt.Sprintf(
				"Here's your implementation plan and codex's review. "+
					"Refine the plan addressing codex's feedback, then START BUILDING. "+
					"Implement step by step. After each logical chunk (2-3 files), "+
					"run tsc --noEmit (or the project's build command) to verify. "+
					"Fix any errors before moving on. "+
					"Commit your work with descriptive messages as you complete each chunk.\n\n"+
					"YOUR PLAN:\n%s\n\nCODEX REVIEW:\n%s\n\n"+
					"SPECIFICATION:\n%s\n\n"+
					"START BUILDING NOW.",
				plan, codexReview, currentProse))
			buildDone <- result
		}()

		// Step 4: Watch for commits; queue reviewer feedback.
		// DO NOT deliver feedback mid-build — that would interrupt
		// CC's build goroutine. Instead accumulate findings into
		// pendingReviews; when the build phase completes (Step 5),
		// we iterate-until-clean: send all queued feedback to CC,
		// wait for fix commits, re-review, repeat until approved.
		fmt.Println("👀 Step 4: Watching for commits, queueing reviewer feedback...")
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
						fmt.Printf("  🔍 %s reviewing...\n", *reviewer)
						codeReview := reviewCall(absRepo,
							"Review these specific changes. Check for: compilation errors, "+
								"missing imports, stub code. Be specific about what to fix.\n\n"+
								"COMMITS:\n"+commitMsg+"\n\nDIFF STAT:\n"+diff)
						if len(codeReview) > 100 && !approvedReview(codeReview) {
							pendingReviews = append(pendingReviews,
								fmt.Sprintf("Commits reviewed:\n%s\n\nFindings:\n%s",
									commitMsg, codeReview))
							fmt.Printf("  ✗ reviewer found issues — queued (%d pending)\n", len(pendingReviews))
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
			finalPrompt := "Review ALL changes in this branch. Check for: " +
				"compilation errors, missing imports, broken tests, incomplete " +
				"functions that only return mock data, unimplemented markers, " +
				"and anything that would fail a typecheck. " +
				"Respond with 'NO ISSUES' or 'LGTM' only if genuinely clean.\n\n" +
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
				"Be brutally honest. If something is a stub or doesn't actually work, say so.\n\n"+
				"Then answer: IS THERE MORE WORK TO DO? If yes, describe EXACTLY what remains "+
				"as a new specification for the next round. If no, say 'ALL DELIVERABLES COMPLETE'.\n\n"+
				"ORIGINAL SPECIFICATION:\n%s", currentProse))
		fmt.Printf("  audit: %d chars\n", len(audit))

		// Step 9: Check if done
		if strings.Contains(strings.ToUpper(audit), "ALL DELIVERABLES COMPLETE") {
			fmt.Printf("\n✅ ROUND %d: All deliverables complete!\n", round)
			break
		}

		// Extract remaining work as new prose for next round
		fmt.Printf("\n🔄 ROUND %d: gaps remain — extracting remaining work for next round\n", round)
		currentProse = audit // the audit becomes the next round's input
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	// Use -p (interactive prompt) NOT --print (text-only).
	// --print can't write files or use tools.
	// --dangerously-skip-permissions auto-approves file writes.
	// --output-format json gives us structured result with
	// the final text in .result field.
	args := []string{
		"-p", prompt,
		"--dangerously-skip-permissions",
		"--output-format", "json",
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
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  claude error: %v\n", err)
	}
	// Parse the JSON output to get the result text
	raw := out.Bytes()
	var result struct {
		Result   string `json:"result"`
		NumTurns int    `json:"num_turns"`
		Cost     float64 `json:"total_cost_usd"`
	}
	if json.Unmarshal(raw, &result) == nil {
		fmt.Printf("  [CC: %d turns, $%.4f]\n", result.NumTurns, result.Cost)
		return result.Result
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
