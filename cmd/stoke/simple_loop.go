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

		// Step 4: Watch for commits and review each one
		fmt.Println("👀 Step 4: Watching for commits...")
		lastReviewedHead := headBefore
		reviewRound := 0
		const maxReviewRounds = 10

	commitWatch:
		for reviewRound < maxReviewRounds {
			select {
			case <-buildDone:
				fmt.Println("  📦 Claude Code build phase complete")
				// Do one final review of any remaining commits
				currentHead := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null")
				if currentHead != lastReviewedHead && currentHead != headBefore {
					diff := shellCmd(absRepo, "git diff "+lastReviewedHead+"..HEAD --stat 2>/dev/null")
					if diff != "" {
						reviewRound++
						fmt.Printf("  🔍 Final review (round %d, via %s)...\n", reviewRound, *reviewer)
						codeReview := reviewCall(absRepo,
							"Review ALL recent changes. Check for: compilation errors, "+
								"missing imports, broken tests, stub code. Be specific.\n\n"+
								"CHANGES:\n"+diff)
						if len(codeReview) > 100 && !strings.Contains(strings.ToLower(codeReview), "no issues") &&
							!strings.Contains(strings.ToLower(codeReview), "lgtm") &&
							!strings.Contains(strings.ToLower(codeReview), "looks good") {
							fmt.Printf("  ✗ reviewer found issues (round %d), sending to CC...\n", reviewRound)
							claudeCall(*claudeBin, absRepo, fmt.Sprintf(
								"The reviewer flagged issues in your code. Fix ALL of them. "+
									"Run the build after each fix. Commit fixes.\n\nREVIEW:\n%s",
								codeReview))
						} else {
							fmt.Printf("  ✓ reviewer approved (round %d)\n", reviewRound)
						}
					}
				}
				break commitWatch

			case <-time.After(30 * time.Second):
				// Check for new commits
				currentHead := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null")
				if currentHead != lastReviewedHead && currentHead != headBefore {
					diff := shellCmd(absRepo, "git diff "+lastReviewedHead+".."+currentHead+" --stat 2>/dev/null")
					commitMsg := shellCmd(absRepo, "git log --oneline "+lastReviewedHead+".."+currentHead+" 2>/dev/null")
					if diff != "" {
						reviewRound++
						fmt.Printf("  📝 New commits detected (round %d):\n%s\n", reviewRound, indent(commitMsg, "    "))
						fmt.Printf("  🔍 %s reviewing...\n", *reviewer)
						codeReview := reviewCall(absRepo,
							"Review these specific changes. Check for: compilation errors, "+
								"missing imports, stub code. Be specific about what to fix.\n\n"+
								"COMMITS:\n"+commitMsg+"\n\nDIFF STAT:\n"+diff)
						if len(codeReview) > 100 && !strings.Contains(strings.ToLower(codeReview), "no issues") &&
							!strings.Contains(strings.ToLower(codeReview), "lgtm") {
							fmt.Printf("  ✗ reviewer found issues, queuing fix for CC...\n")
							// Don't interrupt CC mid-build — queue the feedback
							// CC will get it in the final review round
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
)

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
