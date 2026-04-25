package main

// ops_cost.go — OPSUX-tail: `stoke cost` read-only verb. Sums USD cost
// across events in .stoke/events.db, from two sources:
//
//   1. events whose Type contains the substring "cost" (the canonical
//      cost snapshot channel, e.g. "worker.cost", "stoke.cost.update"),
//   2. events whose payload contains a top-level "cost_usd" field,
//      regardless of Type — belt-and-suspenders for emitters that tag
//      cost onto richer events (e.g. task.complete).
//
//	stoke cost [--db PATH] [--session SID] [--json]
//
// Output table:
//
//	TOTAL_USD  EVENTS  FIRST                 LAST
//	3.2140     42      2026-04-20T12:01:02Z  2026-04-20T13:47:11Z
//
// JSON mode emits a single object, not NDJSON — there's only one
// aggregate and a multi-line output would just invite confusion.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/eventlog"
)

// costAggregate is the shape printed in --json mode.
type costAggregate struct {
	TotalUSD  float64   `json:"total_usd"`
	Events    int       `json:"events"`
	FirstSeen time.Time `json:"first_seen,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
}

// runCostCmd implements `stoke cost`. Exit-code convention matches
// runEventsCmd.
func runCostCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cost", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to events.db (default: <cwd>/.stoke/events.db)")
	session := fs.String("session", "", "filter to events scoped to this session/mission/task/loop id")
	asJSON := fs.Bool("json", false, "emit a single JSON object instead of a table")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	resolved := resolveEventsDB(*dbPath)
	if _, err := os.Stat(resolved); err != nil {
		fmt.Fprintf(stderr, "cost: db not found: %s\n", resolved)
		return 1
	}

	log, err := eventlog.Open(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "cost: open %s: %v\n", resolved, err)
		return 1
	}
	defer log.Close()

	agg, err := aggregateCost(context.Background(), log, *session)
	if err != nil {
		fmt.Fprintf(stderr, "cost: %v\n", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		if err := enc.Encode(agg); err != nil {
			fmt.Fprintf(stderr, "cost: encode: %v\n", err)
			return 1
		}
		return 0
	}
	return renderCostTable(stdout, agg)
}

// aggregateCost walks every relevant event and sums cost_usd across
// matching rows. An event contributes when either:
//   - its Type contains "cost" (case-insensitive), OR
//   - its payload has a top-level cost_usd number (float or int).
//
// Multiple payload shapes are accepted for cost_usd: float64,
// json.Number, and int-looking numbers. We log *nothing* on malformed
// payloads — a single bad row should not bring down the aggregate;
// the Events counter tells operators how many rows contributed.
func aggregateCost(ctx context.Context, log *eventlog.Log, session string) (costAggregate, error) {
	var seq func(yield func(bus.Event, error) bool)
	if session != "" {
		seq = log.ReplaySession(ctx, session)
	} else {
		seq = log.ReadFrom(ctx, 0)
	}

	var agg costAggregate
	for ev, err := range seq {
		if err != nil {
			return costAggregate{}, err
		}
		contrib, ok := eventCostUSD(ev)
		if !ok {
			continue
		}
		agg.TotalUSD += contrib
		agg.Events++
		if agg.FirstSeen.IsZero() || ev.Timestamp.Before(agg.FirstSeen) {
			agg.FirstSeen = ev.Timestamp
		}
		if ev.Timestamp.After(agg.LastSeen) {
			agg.LastSeen = ev.Timestamp
		}
	}
	return agg, nil
}

// eventCostUSD extracts a USD cost contribution from a single event.
// The boolean return distinguishes "no cost on this event" from
// "cost was zero" — the former does NOT increment Events, the latter
// does.
//
// Precedence: payload.cost_usd wins over type-contains-cost alone so
// a 0-valued cost_usd is still counted as a data point. When the type
// matches but the payload has no numeric cost_usd, we treat it as a
// zero-cost contribution (counts toward Events so operators can see
// the snapshot frequency).
func eventCostUSD(ev bus.Event) (float64, bool) {
	typeHasCost := strings.Contains(strings.ToLower(string(ev.Type)), "cost")

	if len(ev.Payload) == 0 {
		if typeHasCost {
			return 0, true
		}
		return 0, false
	}

	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		// Not an object. If the type says it's a cost event, count it
		// as a zero-cost data point rather than dropping it silently.
		if typeHasCost {
			return 0, true
		}
		return 0, false
	}

	raw, present := payload["cost_usd"]
	if !present {
		if typeHasCost {
			return 0, true
		}
		return 0, false
	}

	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, typeHasCost
		}
		return f, true
	default:
		// Unknown shape — count as zero-cost when type flags it, else
		// drop. This matches the rule "don't break aggregate on one
		// malformed row".
		return 0, typeHasCost
	}
}

func renderCostTable(stdout io.Writer, agg costAggregate) int {
	if agg.Events == 0 {
		fmt.Fprintln(stdout, "no cost events")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TOTAL_USD\tEVENTS\tFIRST\tLAST")
	fmt.Fprintf(tw, "%.4f\t%d\t%s\t%s\n",
		agg.TotalUSD,
		agg.Events,
		agg.FirstSeen.UTC().Format(time.RFC3339),
		agg.LastSeen.UTC().Format(time.RFC3339),
	)
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stdout, "cost: tabwriter flush: %v\n", err)
		return 1
	}
	return 0
}
