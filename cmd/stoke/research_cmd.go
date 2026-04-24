package main

// research_cmd.go: `stoke research "query"` — thin CLI wrapper over
// internal/executor.ResearchExecutor. Runs one research pass,
// pretty-prints the resulting Report to stdout, and also dumps the
// full deliverable (plus per-claim verification verdicts) as JSON
// under .stoke/research/<timestamp>.json so the operator has a
// replayable artifact.
//
// Usage:
//
//   stoke research "Compare Go vs Rust for CLI tools"
//   stoke research --url https://go.dev/blog --url https://rust-lang.org "Go vs Rust"
//   stoke research --out path/to/dir "my query"
//
// The --url flag supplies candidate sources the executor will fetch
// and extract claims from; the MVP does not ship a live search
// provider, so without at least one --url the Claims list will be
// empty. That path still produces a report body with the sub-question
// breakdown, which is useful for inspecting the decomposer on its own.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/executor"
	"github.com/RelayOne/r1/internal/research"
)

// researchCmd is the subcommand handler wired into main.go.
func researchCmd(args []string) {
	os.Exit(runResearchCmd(args))
}

// runResearchCmd is the real entry point; extracted so deferred
// cancel() and any other cleanup fires before the outer os.Exit.
func runResearchCmd(args []string) int {
	fs := flag.NewFlagSet("research", flag.ContinueOnError)
	var urlsCSV stringList
	fs.Var(&urlsCSV, "url", "candidate source URL (repeat to add more)")
	outDir := fs.String("out", "", "output directory for the JSON dump (default: .stoke/research)")
	effort := fs.String("effort", "medium", "effort level: low|medium|high")
	stub := fs.Bool("stub-fetch", false, "use in-memory stub fetcher (tests / demos) — reads --stub-body as the body for every --url")
	stubBody := fs.String("stub-body", "", "body returned by --stub-fetch for every supplied URL")
	useTLS := fs.Bool("require-tls", true, "reject http:// URLs (https-only)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: stoke research [flags] \"query\"")
		fs.PrintDefaults()
		return 2
	}
	query := strings.Join(rest, " ")

	// Build the fetcher.
	var fetcher research.Fetcher
	if *stub {
		pages := map[string]string{}
		for _, u := range urlsCSV {
			pages[u] = *stubBody
		}
		fetcher = &research.StubFetcher{Pages: pages}
	} else {
		hf := research.NewHTTPFetcher()
		hf.RequireTLS = *useTLS
		fetcher = hf
	}

	ex := executor.NewResearchExecutor(fetcher)
	plan := executor.Plan{
		ID:    fmt.Sprintf("R-%d", time.Now().Unix()),
		Query: query,
		Extra: map[string]any{"urls": []string(urlsCSV)},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	d, err := ex.Execute(ctx, plan, executor.EffortLevelFromString(*effort))
	if err != nil {
		fmt.Fprintf(os.Stderr, "research failed: %v\n", err)
		return 1
	}
	rd, ok := d.(executor.ResearchDeliverable)
	if !ok {
		fmt.Fprintf(os.Stderr, "unexpected deliverable type %T\n", d)
		return 1
	}

	// Build verification verdicts so the JSON dump captures which
	// claims would pass / fail at the moment of the run. This makes
	// the artifact useful without re-running descent.
	acs := ex.BuildCriteria(executor.Task{ID: plan.ID}, rd)
	verdicts := make([]verdictRecord, 0, len(acs))
	for _, ac := range acs {
		if ac.VerifyFunc == nil {
			continue
		}
		passed, reason := ac.VerifyFunc(ctx)
		verdicts = append(verdicts, verdictRecord{
			ClaimID: ac.ID,
			Passed:  passed,
			Reason:  reason,
		})
	}

	// Pretty-print to stdout.
	fmt.Println(rd.Report.Body)
	fmt.Printf("\nVerification: ")
	passed := 0
	for _, v := range verdicts {
		if v.Passed {
			passed++
		}
	}
	fmt.Printf("%d/%d claims supported\n", passed, len(verdicts))
	for _, v := range verdicts {
		mark := "FAIL"
		if v.Passed {
			mark = "PASS"
		}
		fmt.Printf("  [%s] %s — %s\n", mark, v.ClaimID, v.Reason)
	}

	// JSON dump.
	dir := *outDir
	if dir == "" {
		dir = filepath.Join(".stoke", "research")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", dir, err)
		return 1
	}
	ts := time.Now().UTC().Format("20060102-150405")
	outPath := filepath.Join(dir, ts+".json")
	payload := reportPayload{
		Generated: time.Now().UTC().Format(time.RFC3339),
		Query:     query,
		Report:    rd.Report,
		Verdicts:  verdicts,
	}
	buf, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode json: %v\n", err)
		return 1
	}
	if err := os.WriteFile(outPath, buf, 0644); err != nil { // #nosec G306 -- CLI output artefact; user-readable.
		fmt.Fprintf(os.Stderr, "write %s: %v\n", outPath, err)
		return 1
	}
	fmt.Printf("\nReport JSON: %s\n", outPath)
	return 0
}

// reportPayload is the JSON-dump shape. Stable field order so the
// artifact diffs cleanly across runs.
type reportPayload struct {
	Generated string           `json:"generated_at"`
	Query     string           `json:"query"`
	Report    research.Report  `json:"report"`
	Verdicts  []verdictRecord  `json:"verdicts"`
}

type verdictRecord struct {
	ClaimID string `json:"claim_id"`
	Passed  bool   `json:"passed"`
	Reason  string `json:"reason"`
}

// stringList is a repeatable --flag=value collector for flag.Var.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v != "" {
		*s = append(*s, v)
	}
	return nil
}
