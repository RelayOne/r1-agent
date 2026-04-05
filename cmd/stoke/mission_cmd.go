package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/mission"
	"github.com/ericmacdougall/stoke/internal/orchestrate"
)

// missionCmd dispatches to mission subcommands.
func missionCmd(args []string) {
	if len(args) == 0 {
		missionUsage()
		os.Exit(2)
	}

	switch args[0] {
	case "create":
		missionCreateCmd(args[1:])
	case "list":
		missionListCmd(args[1:])
	case "status":
		missionStatusCmd(args[1:])
	case "advance":
		missionAdvanceCmd(args[1:])
	case "run":
		missionRunCmd(args[1:])
	case "findings":
		missionFindingsCmd(args[1:])
	case "context":
		missionContextCmd(args[1:])
	case "help", "--help", "-h":
		missionUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown mission subcommand: %s\n\n", args[0])
		missionUsage()
		os.Exit(2)
	}
}

func missionUsage() {
	fmt.Fprintf(os.Stderr, `Usage: stoke mission <subcommand> [flags]

Subcommands:
  create    Create a new mission from user intent
  list      List missions, optionally filtered by phase
  status    Show convergence status for a mission
  findings  Show convergence findings/gaps for a mission
  advance   Manually advance a mission to the next phase
  run       Drive a mission through the convergence loop to completion
  context   Build enriched agent context for a mission

Flags vary by subcommand. Use "stoke mission <subcommand> --help" for details.
`)
}

func getOrchestrator(storeDir string) (*orchestrate.Orchestrator, error) {
	orch, _, err := getOrchestratorWithDiscovery(storeDir, "", false)
	return orch, err
}

// getOrchestratorWithDiscovery creates an orchestrator with optional agentic
// discovery engine wired in. When enabled, the DiscoveryEngine creates
// multi-turn Claude sessions with MCP codebase tools for deep code analysis.
// Returns the discovery engine (may be nil) so the caller can call Cleanup().
func getOrchestratorWithDiscovery(storeDir, claudeBin string, noDiscovery bool) (*orchestrate.Orchestrator, *orchestrate.DiscoveryEngine, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	if storeDir == "" {
		storeDir = filepath.Join(cwd, ".stoke", "data")
	}

	config := orchestrate.Config{
		StoreDir: storeDir,
		RepoRoot: cwd,
	}

	var de *orchestrate.DiscoveryEngine
	if !noDiscovery {
		runner := engine.NewClaudeRunner(claudeBin)
		de = orchestrate.NewDiscoveryEngine(runner, cwd)
		config.DiscoveryFn = de.DiscoveryFn()
		config.ValidateDiscoveryFn = de.ValidateDiscoveryFn()
		config.ExecuteFn = de.ExecuteFn()
		config.ValidateFn = de.ValidateFn()
	}

	orch, err := orchestrate.New(config)
	if err != nil {
		if de != nil {
			de.Cleanup()
		}
		return nil, nil, err
	}
	return orch, de, nil
}

// --- create ---

func missionCreateCmd(args []string) {
	fs := flag.NewFlagSet("mission create", flag.ExitOnError)
	title := fs.String("title", "", "Short mission title (required)")
	intent := fs.String("intent", "", "Full user intent description (required)")
	criteria := fs.String("criteria", "", "Comma-separated acceptance criteria")
	storeDir := fs.String("store", "", "Mission store directory (default: .stoke/data)")
	fs.Parse(args)

	if *title == "" || *intent == "" {
		fmt.Fprintln(os.Stderr, "Error: --title and --intent are required")
		fs.Usage()
		os.Exit(1)
	}

	orch, err := getOrchestrator(*storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer orch.Close()

	var criteriaList []string
	if *criteria != "" {
		for _, c := range strings.Split(*criteria, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				criteriaList = append(criteriaList, c)
			}
		}
	}

	m, err := orch.CreateMission(*title, *intent, criteriaList)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created mission %s\n", m.ID)
	fmt.Printf("  Title:    %s\n", m.Title)
	fmt.Printf("  Intent:   %s\n", m.Intent)
	fmt.Printf("  Criteria: %d\n", len(m.Criteria))
	for _, c := range m.Criteria {
		fmt.Printf("    %s: %s\n", c.ID, c.Description)
	}
}

// --- list ---

func missionListCmd(args []string) {
	fs := flag.NewFlagSet("mission list", flag.ExitOnError)
	phase := fs.String("phase", "", "Filter by phase (created, executing, validating, etc.)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	storeDir := fs.String("store", "", "Mission store directory")
	fs.Parse(args)

	orch, err := getOrchestrator(*storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer orch.Close()

	missions, err := orch.ListMissions(mission.Phase(*phase))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(missions)
		return
	}

	if len(missions) == 0 {
		fmt.Println("No missions found.")
		return
	}

	fmt.Printf("%-20s %-12s %-40s %s\n", "ID", "PHASE", "TITLE", "CRITERIA")
	fmt.Println(strings.Repeat("-", 90))
	for _, m := range missions {
		satisfied := 0
		for _, c := range m.Criteria {
			if c.Satisfied {
				satisfied++
			}
		}
		fmt.Printf("%-20s %-12s %-40s %d/%d\n",
			truncateField(m.ID, 20),
			m.Phase,
			truncateField(m.Title, 40),
			satisfied, len(m.Criteria))
	}
}

// --- status ---

func missionStatusCmd(args []string) {
	fs := flag.NewFlagSet("mission status", flag.ExitOnError)
	id := fs.String("id", "", "Mission ID (required)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	storeDir := fs.String("store", "", "Mission store directory")
	fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "Error: --id is required")
		os.Exit(1)
	}

	orch, err := getOrchestrator(*storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer orch.Close()

	m, err := orch.GetMission(*id)
	if err != nil || m == nil {
		fmt.Fprintf(os.Stderr, "Error: mission %q not found\n", *id)
		os.Exit(1)
	}

	status, err := orch.CheckConvergence(*id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]interface{}{
			"mission": m,
			"status":  status,
		})
		return
	}

	fmt.Printf("Mission: %s\n", m.Title)
	fmt.Printf("ID:      %s\n", m.ID)
	fmt.Printf("Phase:   %s\n", m.Phase)
	fmt.Printf("Intent:  %s\n", m.Intent)
	fmt.Println()

	fmt.Printf("Criteria: %d/%d satisfied\n", status.SatisfiedCriteria, status.TotalCriteria)
	for _, c := range m.Criteria {
		mark := "[ ]"
		if c.Satisfied {
			mark = "[x]"
		}
		fmt.Printf("  %s %s\n", mark, c.Description)
	}
	fmt.Println()

	fmt.Printf("Gaps:      %d open (%d blocking)\n", status.OpenGapCount, status.BlockingGapCount)
	fmt.Printf("Handoffs:  %d\n", status.HandoffCount)
	fmt.Printf("Consensus: %d votes (%d complete)\n", status.ConsensusCount, status.CompleteVotes)
	fmt.Printf("Converged: %v\n", status.IsConverged)
	fmt.Printf("Consensus: %v\n", status.HasConsensus)
}

// --- advance ---

func missionAdvanceCmd(args []string) {
	fs := flag.NewFlagSet("mission advance", flag.ExitOnError)
	id := fs.String("id", "", "Mission ID (required)")
	phase := fs.String("phase", "", "Target phase (required)")
	reason := fs.String("reason", "", "Reason for transition")
	agent := fs.String("agent", "cli", "Agent performing the transition")
	storeDir := fs.String("store", "", "Mission store directory")
	fs.Parse(args)

	if *id == "" || *phase == "" {
		fmt.Fprintln(os.Stderr, "Error: --id and --phase are required")
		os.Exit(1)
	}

	orch, err := getOrchestrator(*storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer orch.Close()

	if err := orch.AdvanceMission(*id, mission.Phase(*phase), *reason, *agent); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Advanced mission %s to phase %s\n", *id, *phase)
}

// --- run ---

func missionRunCmd(args []string) {
	fs := flag.NewFlagSet("mission run", flag.ExitOnError)
	id := fs.String("id", "", "Mission ID (required)")
	maxLoops := fs.Int("max-loops", 5, "Maximum convergence loop iterations")
	consensus := fs.Int("consensus", 2, "Required consensus model count")
	timeout := fs.Duration("timeout", 60*time.Minute, "Overall timeout")
	storeDir := fs.String("store", "", "Mission store directory")
	claudeBin := fs.String("claude-bin", "", "Path to claude binary (default: auto-detect)")
	noDiscovery := fs.Bool("no-discovery", false, "Disable agentic discovery loops")
	fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "Error: --id is required")
		os.Exit(1)
	}

	orch, de, err := getOrchestratorWithDiscovery(*storeDir, *claudeBin, *noDiscovery)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer orch.Close()
	if de != nil {
		defer de.Cleanup()
	}

	config := mission.RunnerConfig{
		MaxConvergenceLoops: *maxLoops,
		RequiredConsensus:   *consensus,
		OnPhaseComplete: func(missionID string, result *mission.PhaseResult) {
			fmt.Printf("[%s] Phase %s completed: %s (%s)\n",
				missionID, result.Phase, result.Summary, result.Duration.Round(time.Millisecond))
		},
		OnConvergenceLoop: func(missionID string, iteration, gapCount int) {
			fmt.Printf("[%s] Convergence loop %d: %d open gaps\n", missionID, iteration, gapCount)
		},
		OnMissionComplete: func(missionID string, phase mission.Phase, summary string) {
			fmt.Printf("[%s] Mission %s: %s\n", missionID, phase, summary)
		},
	}

	runner := orch.NewRunner(config)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := runner.Run(ctx, *id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("Final phase: %s\n", result.FinalPhase)
	fmt.Printf("Phases run:  %d\n", len(result.Phases))
	fmt.Printf("Conv loops:  %d\n", result.ConvergenceLoops)
	fmt.Printf("Duration:    %s\n", result.TotalDuration.Round(time.Millisecond))

	if result.IsFailed() {
		os.Exit(1)
	}
}

// --- context ---

func missionContextCmd(args []string) {
	fs := flag.NewFlagSet("mission context", flag.ExitOnError)
	id := fs.String("id", "", "Mission ID (required)")
	maxTokens := fs.Int("max-tokens", 4000, "Maximum token budget")
	storeDir := fs.String("store", "", "Mission store directory")
	fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "Error: --id is required")
		os.Exit(1)
	}

	orch, err := getOrchestrator(*storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer orch.Close()

	config := mission.DefaultContextConfig()
	config.MaxTokens = *maxTokens

	ctx, err := orch.BuildAgentContext(*id, config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(ctx)
}

// --- findings ---

func missionFindingsCmd(args []string) {
	fs := flag.NewFlagSet("mission findings", flag.ExitOnError)
	id := fs.String("id", "", "Mission ID (required)")
	severity := fs.String("severity", "", "Filter by severity: blocking, major, minor, info")
	category := fs.String("category", "", "Filter by category: completeness, test, code, security, docs, consistency, ux")
	all := fs.Bool("all", false, "Include resolved findings")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	storeDir := fs.String("store", "", "Mission store directory")
	fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "Error: --id is required")
		fs.Usage()
		os.Exit(1)
	}

	orch, err := getOrchestrator(*storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer orch.Close()

	m, err := orch.GetMission(*id)
	if err != nil || m == nil {
		fmt.Fprintf(os.Stderr, "Error: mission %q not found\n", *id)
		os.Exit(1)
	}

	var gaps []mission.Gap
	if *all {
		gaps, err = orch.AllGaps(*id)
	} else {
		gaps, err = orch.OpenGaps(*id)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Apply filters
	var filtered []mission.Gap
	for _, g := range gaps {
		if *severity != "" && g.Severity != *severity {
			continue
		}
		if *category != "" && g.Category != *category {
			continue
		}
		filtered = append(filtered, g)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]interface{}{
			"mission_id": m.ID,
			"findings":   filtered,
			"count":      len(filtered),
			"total":      len(gaps),
		})
		return
	}

	fmt.Printf("Mission: %s (%s)\n", m.Title, m.ID)
	label := "Open findings"
	if *all {
		label = "All findings"
	}
	fmt.Printf("%s: %d", label, len(filtered))
	if len(filtered) != len(gaps) {
		fmt.Printf(" (of %d total)", len(gaps))
	}
	fmt.Println()
	fmt.Println()

	if len(filtered) == 0 {
		fmt.Println("  No findings.")
		return
	}

	// Group by severity for display
	sevOrder := []string{"blocking", "major", "minor", "info"}
	grouped := map[string][]mission.Gap{}
	for _, g := range filtered {
		grouped[g.Severity] = append(grouped[g.Severity], g)
	}

	for _, sev := range sevOrder {
		items := grouped[sev]
		if len(items) == 0 {
			continue
		}

		icon := severityIcon(sev)
		fmt.Printf("%s %s (%d)\n", icon, strings.ToUpper(sev), len(items))
		for _, g := range items {
			loc := ""
			if g.File != "" {
				loc = g.File
				if g.Line > 0 {
					loc = fmt.Sprintf("%s:%d", g.File, g.Line)
				}
			}
			fmt.Printf("  [%s] %s", g.Category, g.Description)
			if loc != "" {
				fmt.Printf(" (%s)", loc)
			}
			if g.Resolved {
				fmt.Print(" [RESOLVED]")
			}
			fmt.Println()
			if g.Suggestion != "" {
				fmt.Printf("         -> %s\n", g.Suggestion)
			}
		}
		fmt.Println()
	}
}

func severityIcon(sev string) string {
	switch sev {
	case "blocking":
		return "!!!"
	case "major":
		return " !!"
	case "minor":
		return "  !"
	case "info":
		return "  ."
	default:
		return "  ?"
	}
}

func truncateField(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
