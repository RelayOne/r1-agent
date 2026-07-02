package main

// memory_consolidate.go — A101: `r1 memory consolidate`.
//
// Activates internal/consolidation (STOKE-010): the offline pass that
// reads Episodic memory and promotes recurring patterns into Semantic
// insights. Before this command nothing constructed
// consolidation.BackgroundJob and nothing wired the tiered
// memory.Router, so the consolidation subsystem — and the tiered Router
// it depends on — were both dormant.
//
// Wiring:
//   - Episodic source: the agent's existing cross-session Store at
//     <repo>/.r1/agent-memory.json, exposed to the Router via
//     memory.NewEpisodicView. This is the live memory surface agents
//     already write to; consolidation reads it unmodified.
//   - Semantic sink: a durable memory.FileStore at
//     <repo>/.r1/memory/semantic.json so consolidated insights survive
//     between runs.
//   - A deterministic ExtractFunc (consolidateExtract) clusters
//     episodic items into one insight per label — pure function of the
//     corpus, no randomness or wall-clock in the output.
//
//	r1 memory consolidate [--repo DIR] [--interval DUR] [--json]
//
// interval=0 (default) runs a single pass (RunOnce) and exits — the
// on-demand path. interval>0 runs the background ticker in the
// foreground until SIGINT/SIGTERM — the daemon path. Folding the ticker
// into `r1 serve` proper is a follow-up: that path owns its own process
// lifecycle and is out of this change's scope.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/RelayOne/r1/internal/consolidation"
	"github.com/RelayOne/r1/internal/memory"
)

// runMemoryConsolidateCmd implements `r1 memory consolidate`.
func runMemoryConsolidateCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory consolidate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "repository root holding .r1/ (default: cwd)")
	interval := fs.Duration("interval", 0, "run continuously on this interval (0 = single pass then exit)")
	asJSON := fs.Bool("json", false, "emit the run report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	repoDir := *repo
	if repoDir == "" {
		// LINT-ALLOW chdir-cli-entry: r1 memory consolidate subcommand; cwd is the .r1 discovery anchor when --repo is unset.
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "memory consolidate: getwd: %v\n", err)
			return 1
		}
		repoDir = cwd
	}
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "memory consolidate: abs %s: %v\n", repoDir, err)
		return 1
	}

	job, _, err := buildConsolidationJob(abs, *interval)
	if err != nil {
		fmt.Fprintf(stderr, "memory consolidate: %v\n", err)
		return 1
	}

	if *interval > 0 {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runConsolidateDaemon(ctx, job, stdout, *asJSON)
	}

	// On-demand single pass. Subscribe before RunOnce so the synchronous
	// emit lands in the buffered channel we then read.
	sub := job.Subscribe()
	if err := job.RunOnce(context.Background()); err != nil {
		fmt.Fprintf(stderr, "memory consolidate: %v\n", err)
		return 1
	}
	report := <-sub
	return printConsolidateReport(stdout, report, *asJSON)
}

// buildConsolidationJob wires the tiered Router over repoDir's memory
// surfaces and returns a ready-to-run job plus the Semantic FileStore
// (so callers/tests can inspect the persisted result).
func buildConsolidationJob(repoDir string, interval time.Duration) (*consolidation.BackgroundJob, *memory.FileStore, error) {
	store, err := memory.NewStore(memory.Config{Path: filepath.Join(repoDir, ".r1", "agent-memory.json")})
	if err != nil {
		return nil, nil, fmt.Errorf("open episodic store: %w", err)
	}
	semantic, err := memory.NewFileStore(filepath.Join(repoDir, ".r1", "memory", "semantic.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("open semantic store: %w", err)
	}
	router := memory.NewRouter()
	router.Register(memory.TierEpisodic, memory.NewEpisodicView(store))
	router.Register(memory.TierSemantic, semantic)
	job := consolidation.NewBackgroundJob(router, consolidateExtract, interval)
	return job, semantic, nil
}

// runConsolidateDaemon runs the background ticker until ctx is
// cancelled (SIGINT/SIGTERM at the CLI boundary, or a deadline in
// tests), printing each RunReport as it arrives.
func runConsolidateDaemon(ctx context.Context, job *consolidation.BackgroundJob, stdout io.Writer, asJSON bool) int {
	sub := job.Subscribe()
	job.Start(ctx)
	defer job.Stop()
	if !asJSON {
		fmt.Fprintln(stdout, "memory consolidate: running (Ctrl-C to stop)")
	}
	for {
		select {
		case <-ctx.Done():
			return 0
		case r := <-sub:
			_ = printConsolidateReport(stdout, r, asJSON)
		}
	}
}

// consolidateExtract is the deterministic pattern extractor injected
// into the consolidation job. It clusters episodic items by their
// primary label (first tag, or "untagged") and emits one Semantic
// insight per cluster whose ID and content are a pure function of the
// cluster — no randomness, no wall-clock in the output — so a given
// episodic corpus always consolidates to the same insights, and
// re-running is idempotent (Put overwrites by ID).
func consolidateExtract(_ context.Context, items []memory.Item) ([]consolidation.Insight, error) {
	groups := map[string][]memory.Item{}
	for _, it := range items {
		label := primaryLabel(it)
		groups[label] = append(groups[label], it)
	}
	labels := make([]string, 0, len(groups))
	for l := range groups {
		labels = append(labels, l)
	}
	sort.Strings(labels)

	out := make([]consolidation.Insight, 0, len(labels))
	for _, label := range labels {
		grp := groups[label]
		earliest := grp[0].CreatedAt
		for _, it := range grp {
			if it.CreatedAt.Before(earliest) {
				earliest = it.CreatedAt
			}
		}
		out = append(out, consolidation.Insight{
			ID:        "insight-" + label,
			Tier:      consolidation.TierIntern,
			Content:   fmt.Sprintf("consolidated %d episodic memories labeled %q", len(grp), label),
			Tags:      []string{label},
			Samples:   len(grp),
			Successes: len(grp),
			CreatedAt: earliest,
		})
	}
	return out, nil
}

// primaryLabel picks a deterministic cluster key for an episodic item:
// its first non-empty tag, else "untagged".
func primaryLabel(it memory.Item) string {
	if len(it.Tags) > 0 && strings.TrimSpace(it.Tags[0]) != "" {
		return it.Tags[0]
	}
	return "untagged"
}

// consolidateReportJSON is the `--json` wire shape of a run report.
type consolidateReportJSON struct {
	At              string   `json:"at"`
	EpisodicScanned int      `json:"episodic_scanned"`
	InsightsAdded   int      `json:"insights_added"`
	Errors          []string `json:"errors,omitempty"`
}

func printConsolidateReport(stdout io.Writer, r consolidation.RunReport, asJSON bool) int {
	if asJSON {
		out := consolidateReportJSON{
			At:              r.At.UTC().Format(time.RFC3339),
			EpisodicScanned: r.EpisodicScanned,
			InsightsAdded:   r.InsightsAdded,
		}
		for _, e := range r.Errors {
			out.Errors = append(out.Errors, e.Error())
		}
		if err := json.NewEncoder(stdout).Encode(out); err != nil {
			fmt.Fprintf(stdout, "consolidation: encode report: %v\n", err)
			return 1
		}
		return exitForReport(r)
	}
	fmt.Fprintf(stdout, "consolidation: scanned %d episodic, added %d semantic insight(s)\n",
		r.EpisodicScanned, r.InsightsAdded)
	for _, e := range r.Errors {
		fmt.Fprintf(stdout, "  error: %v\n", e)
	}
	return exitForReport(r)
}

func exitForReport(r consolidation.RunReport) int {
	if len(r.Errors) > 0 {
		return 1
	}
	return 0
}
