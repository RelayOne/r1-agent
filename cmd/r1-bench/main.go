// r1-bench — TruthfulCompletion benchmark runner CLI.
//
// Drives one (mission, agent) pair, scores the result via VerdictScorer,
// and emits the RunResult as JSON to stdout (or a file when --output is
// set). Enforces the cross-vendor judge constraint: the LLM judge model
// must come from a different vendor than the agent under test.
//
// Spec: specs/truthful-completion-benchmark.md §T5 (items 42-46).
//
// Subcommand: `r1-bench reviewereval` runs the S-U-017 cross-model
// review FP/FN measurement harness (internal/reviewereval); see
// reviewereval.go. All other invocations use the flat-flag run mode
// below.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/bench"
	"github.com/RelayOne/r1/internal/bench/agents"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "r1-bench:", err)
		os.Exit(1)
	}
}

type cliFlags struct {
	mission         string
	agent           string
	corpus          string
	workDir         string
	output          string
	timeout         time.Duration
	judgeModel      string
	noJudge         bool
	listAgents      bool
	listCorpus      bool
	aggregate       string // directory of *.json result files to aggregate; empty = run mode
	aggregateFormat string // "markdown" | "per-mission" | "both"
}

func parseFlags(args []string) (*cliFlags, error) {
	fs := flag.NewFlagSet("r1-bench", flag.ContinueOnError)
	f := &cliFlags{}
	fs.StringVar(&f.mission, "mission", "", "mission ID (required unless --list-agents/--list-corpus)")
	fs.StringVar(&f.agent, "agent", "", "agent dispatcher ID (e.g. r1, r1-antitrunc, claude-code-default, cline, aider, codex-cli, cursor, tether+aider) (required)")
	fs.StringVar(&f.corpus, "corpus", "internal/bench/golden/truthful-completion", "corpus root directory")
	fs.StringVar(&f.workDir, "workdir", "", "agent working directory (default: temp dir)")
	fs.StringVar(&f.output, "output", "", "output path for RunResult JSON (default: stdout)")
	fs.DurationVar(&f.timeout, "timeout", 10*time.Minute, "per-mission timeout")
	fs.StringVar(&f.judgeModel, "judge-model", "", "LLM judge model (vendor must differ from agent vendor); empty disables judge")
	fs.BoolVar(&f.noJudge, "no-judge", false, "force-disable the LLM judge even when criteria require it")
	fs.BoolVar(&f.listAgents, "list-agents", false, "list registered agent IDs and exit")
	fs.BoolVar(&f.listCorpus, "list-corpus", false, "list available missions in the corpus and exit")
	fs.StringVar(&f.aggregate, "aggregate", "", "directory of RunResult JSON files to fold into a leaderboard; non-empty switches to aggregate mode")
	fs.StringVar(&f.aggregateFormat, "aggregate-format", "markdown", "aggregate output format: markdown (leaderboard), per-mission (drill-down), both")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if f.listAgents || f.listCorpus || f.aggregate != "" {
		return f, nil
	}
	if f.mission == "" {
		return nil, errors.New("--mission is required")
	}
	if f.agent == "" {
		return nil, errors.New("--agent is required")
	}
	return f, nil
}

func run() error {
	// Subcommand dispatch (S-U-017): `r1-bench reviewereval ...` runs
	// the cross-model review measurement harness. Everything else
	// keeps the original flat-flag surface.
	if len(os.Args) > 1 && os.Args[1] == "reviewereval" {
		return runReviewerEval(os.Args[2:])
	}

	f, err := parseFlags(os.Args[1:])
	if err != nil {
		return err
	}

	if f.listAgents {
		for _, id := range listAgentIDs() {
			fmt.Println(id)
		}
		return nil
	}
	if f.listCorpus {
		ids, err := listMissionIDs(f.corpus)
		if err != nil {
			return err
		}
		for _, id := range ids {
			fmt.Println(id)
		}
		return nil
	}

	if f.aggregate != "" {
		out, err := AggregateDir(f.aggregate, f.aggregateFormat)
		if err != nil {
			return fmt.Errorf("aggregate: %w", err)
		}
		if f.output == "" {
			_, werr := os.Stdout.Write([]byte(out))
			return werr
		}
		return os.WriteFile(f.output, []byte(out), 0o644)
	}

	// SOTA gap #1: wire r1's real native agentloop into the r1/r1-antitrunc
	// dispatchers so `--agent r1` benchmarks r1 itself, not the stub. No-op
	// for other agents. The invoker reads model/creds from the environment
	// (ANTHROPIC_API_KEY / R1_NATIVE_MODEL / R1_NATIVE_BASE_URL); without
	// them the run fails honestly at the first model call rather than
	// reporting a fake completion.
	if strings.HasPrefix(f.agent, "r1") {
		agents.SetR1ModelInvoker(resolveNativeInvoker(os.Getenv("R1_NATIVE_MODEL")))
	}

	dispatcher := agents.Lookup(f.agent)
	if dispatcher == nil {
		return fmt.Errorf("agent %q is not registered (run --list-agents to see available IDs)", f.agent)
	}

	runner := bench.NewRunner(f.corpus)
	mission, err := runner.LoadMission(f.mission)
	if err != nil {
		return fmt.Errorf("load mission: %w", err)
	}

	workDir := f.workDir
	if workDir == "" {
		workDir, err = os.MkdirTemp("", "r1-bench-"+f.mission+"-")
		if err != nil {
			return fmt.Errorf("create temp workdir: %w", err)
		}
		defer os.RemoveAll(workDir)
	}
	// Seal a git baseline so end-of-run diff capture works and the
	// graded tree has no history to mine. No-op for caller-supplied
	// dirs that are already git work trees.
	if err := prepareWorkDir(workDir); err != nil {
		return err
	}

	// Cross-vendor judge check happens BEFORE we dispatch the agent
	// so a misconfiguration fails fast.
	var judge bench.CompletionJudge
	if !f.noJudge && f.judgeModel != "" {
		agentVendor := vendorForAgent(f.agent)
		judgeVendor := vendorForModel(f.judgeModel)
		if agentVendor != "" && judgeVendor != "" && agentVendor == judgeVendor {
			return fmt.Errorf("cross-vendor judge constraint violated: agent %q vendor=%q, judge %q vendor=%q (must differ)",
				f.agent, agentVendor, f.judgeModel, judgeVendor)
		}
		judge, err = buildJudge(f.judgeModel)
		if err != nil {
			return fmt.Errorf("build judge: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), f.timeout+30*time.Second)
	defer cancel()

	result, err := RunOne(ctx, RunSpec{
		Mission:    mission,
		Dispatcher: dispatcher,
		WorkDir:    workDir,
		Timeout:    f.timeout,
		Judge:      judge,
	})
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	return writeResult(result, f.output)
}

func writeResult(result *bench.RunResult, output string) error {
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if output == "" {
		_, werr := os.Stdout.Write(append(body, '\n'))
		return werr
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("mkdir output dir: %w", err)
	}
	return os.WriteFile(output, append(body, '\n'), 0o644)
}

func listAgentIDs() []string {
	// Snapshot to avoid holding the lock across iteration.
	out := []string{}
	for id := range agents.Registry {
		out = append(out, id)
	}
	return out
}

func listMissionIDs(corpus string) ([]string, error) {
	r := bench.NewRunner(corpus)
	ms, err := r.ListMissions()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
