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
  advance   Manually advance a mission to the next phase
  run       Drive a mission through the convergence loop to completion
  context   Build enriched agent context for a mission

Flags vary by subcommand. Use "stoke mission <subcommand> --help" for details.
`)
}

func getOrchestrator(storeDir string) (*orchestrate.Orchestrator, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if storeDir == "" {
		// Default: .stoke/data in current directory
		storeDir = filepath.Join(cwd, ".stoke", "data")
	}
	return orchestrate.New(orchestrate.Config{
		StoreDir: storeDir,
		RepoRoot: cwd,
	})
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

func truncateField(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
