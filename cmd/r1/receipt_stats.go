package main

// receipt_stats.go — `r1 receipt stats`.
//
// A per-worker receipt table joined from two ledgers:
//   - task telemetry (internal/taskstats): authoritative turns / cost / pass-fail
//     per task, grouped here by the worker (SessionID) that ran it.
//   - the repo's receipt index (internal/receipts): how many receipts each task
//     emitted, joined by TaskID.
//
// The table answers "per worker: turns, cost, receipts, pass/fail" for the work
// visible in this repo. Turns/cost/pass-fail come from the telemetry ledger;
// the Receipts column reflects THIS repo's receipt index.

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/RelayOne/r1/internal/receipts"
	"github.com/RelayOne/r1/internal/taskstats"
)

// WorkerStat is one row of the receipt stats table: a worker (session) with its
// aggregated telemetry and receipt count.
type WorkerStat struct {
	Worker   string  `json:"worker"`
	Tasks    int     `json:"tasks"`
	Turns    int     `json:"turns"`
	CostUSD  float64 `json:"cost_usd"`
	Receipts int     `json:"receipts"`
	Passed   int     `json:"passed"`
	Failed   int     `json:"failed"`
}

// buildWorkerStats aggregates task telemetry into per-worker rows, joining the
// repo's receipts by TaskID for the Receipts column. When project is non-empty,
// only telemetry rows whose ProjectSlug matches are included. Rows are returned
// sorted by worker id for deterministic output.
func buildWorkerStats(stats []taskstats.Record, recs []receipts.Receipt, project string) []WorkerStat {
	receiptsByTask := map[string]int{}
	for _, r := range recs {
		receiptsByTask[r.TaskID]++
	}

	agg := map[string]*WorkerStat{}
	for _, s := range stats {
		if project != "" && s.ProjectSlug != project {
			continue
		}
		key := s.SessionID
		if key == "" {
			key = "(unknown)"
		}
		w, ok := agg[key]
		if !ok {
			w = &WorkerStat{Worker: key}
			agg[key] = w
		}
		w.Tasks++
		w.Turns += s.Turns
		w.CostUSD += s.CostUSD
		w.Receipts += receiptsByTask[s.TaskID]
		if s.Success {
			w.Passed++
		} else {
			w.Failed++
		}
	}

	out := make([]WorkerStat, 0, len(agg))
	for _, w := range agg {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Worker < out[j].Worker })
	return out
}

// totalRow sums the per-worker rows into a single TOTAL row.
func totalRow(rows []WorkerStat) WorkerStat {
	total := WorkerStat{Worker: "TOTAL"}
	for _, r := range rows {
		total.Tasks += r.Tasks
		total.Turns += r.Turns
		total.CostUSD += r.CostUSD
		total.Receipts += r.Receipts
		total.Passed += r.Passed
		total.Failed += r.Failed
	}
	return total
}

func runReceiptStats(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("receipt stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root (for the receipt index)")
	project := fs.String("project", "", "restrict telemetry to this project slug (default: all recent workers)")
	limit := fs.Int("limit", 0, "consider only the N most recent telemetry records (0 = all)")
	asJSON := fs.Bool("json", false, "emit json")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	recs, err := receipts.Load(*repo, receipts.Filter{})
	if err != nil {
		fmt.Fprintf(stderr, "receipt stats: %v\n", err)
		return 1
	}
	rows := buildWorkerStats(taskstats.LoadRecent(*limit), recs, *project)

	if *asJSON {
		return encodeJSON(stdout, rows, stderr)
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no worker telemetry")
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKER\tTASKS\tTURNS\tCOST_USD\tRECEIPTS\tPASS\tFAIL")
	writeRow := func(r WorkerStat) {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%.4f\t%d\t%d\t%d\n", r.Worker, r.Tasks, r.Turns, r.CostUSD, r.Receipts, r.Passed, r.Failed)
	}
	for _, r := range rows {
		writeRow(r)
	}
	writeRow(totalRow(rows))
	_ = tw.Flush()
	return 0
}
