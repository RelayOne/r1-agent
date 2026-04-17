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
	codexBin := fs.String("codex-bin", "codex", "Codex binary")
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
	fmt.Printf("   claude: %s, codex: %s\n", *claudeBin, *codexBin)
	fmt.Printf("   max rounds: %d\n\n", *maxRounds)

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

		// Step 2: Codex reviews the plan
		fmt.Println("📝 Step 2: Codex reviewing plan...")
		codexReview := codexCall(*codexBin, absRepo,
			"Review this implementation plan for a software project. "+
				"Flag any issues: missing files, wrong dependencies, unrealistic steps, "+
				"ordering problems. Suggest improvements. Be specific.\n\nPLAN:\n"+plan)
		fmt.Printf("  ✓ codex review: %d chars\n", len(codexReview))

		// Step 3: Claude Code refines with codex feedback
		fmt.Println("🔧 Step 3: Claude Code refining plan with codex feedback...")
		refinedPlan := claudeCall(*claudeBin, absRepo, fmt.Sprintf(
			"Here's your implementation plan and codex's review of it. "+
				"Refine the plan addressing codex's feedback, then START BUILDING. "+
				"Implement the plan step by step. After each logical chunk (2-3 files), "+
				"run the build command to verify it compiles. Fix any errors before moving on. "+
				"Commit your work with descriptive messages as you complete each chunk.\n\n"+
				"YOUR PLAN:\n%s\n\nCODEX REVIEW:\n%s\n\n"+
				"SPECIFICATION:\n%s\n\n"+
				"START BUILDING NOW. Use tools to create files, run builds, and commit.",
			plan, codexReview, currentProse))
		fmt.Printf("  ✓ build phase done: %d chars response\n", len(refinedPlan))

		// Step 4: Get list of new commits
		fmt.Println("📊 Step 4: Checking commits...")
		commitsOut := shellCmd(absRepo, "git log --oneline -20")
		fmt.Printf("  commits:\n%s\n", indent(commitsOut, "    "))

		// Step 5: Codex reviews the code
		fmt.Println("🔍 Step 5: Codex reviewing code...")
		diff := shellCmd(absRepo, "git diff HEAD~5..HEAD --stat 2>/dev/null || git diff --stat")
		codeReview := codexCall(*codexBin, absRepo,
			"Review the recent changes to this repository. "+
				"Check for: compilation errors, missing imports, broken tests, "+
				"stub code, incomplete implementations. Be specific about what to fix.\n\n"+
				"RECENT CHANGES:\n"+diff)
		fmt.Printf("  ✓ code review: %d chars\n", len(codeReview))

		// Step 6: If codex found issues, send back to Claude Code
		if len(codeReview) > 100 && !strings.Contains(strings.ToLower(codeReview), "no issues") {
			fmt.Println("🔧 Step 6: Claude Code fixing codex review issues...")
			claudeCall(*claudeBin, absRepo, fmt.Sprintf(
				"Codex reviewed your code and found issues. Fix ALL of them. "+
					"Run the build after each fix to verify. Commit fixes.\n\n"+
					"CODEX REVIEW:\n%s\n\n"+
					"Fix these issues now. Use tools to edit files, run builds, commit.",
				codeReview))
			fmt.Println("  ✓ fixes applied")
		} else {
			fmt.Println("  ✓ codex approved — no issues found")
		}

		// Step 7: Build verification
		fmt.Println("🏗️  Step 7: Build verification...")
		buildResult := shellCmd(absRepo, detectSimpleBuildCmd(absRepo))
		buildPassed := !strings.Contains(buildResult, "error") || strings.Contains(buildResult, "0 errors")
		if buildPassed {
			fmt.Println("  ✓ build passes")
		} else {
			fmt.Printf("  ✗ build failed:\n%s\n", indent(buildResult, "    "))
			fmt.Println("  → sending build errors to Claude Code...")
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

func claudeCall(bin, dir, prompt string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--print", "--no-session-persistence", prompt)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  claude error: %v\n", err)
		return ""
	}
	return strings.TrimSpace(out.String())
}

func codexCall(bin, dir, prompt string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	tmpOut := fmt.Sprintf("/tmp/codex-simple-%d.txt", time.Now().UnixNano())
	cmd := exec.CommandContext(ctx, bin, "exec",
		"--dangerously-bypass-approvals-and-sandbox",
		"-o", tmpOut, prompt)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  codex error: %v\n", err)
		return ""
	}
	data, _ := os.ReadFile(tmpOut)
	os.Remove(tmpOut)
	return strings.TrimSpace(string(data))
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
