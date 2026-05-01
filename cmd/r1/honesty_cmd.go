package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/honesty"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/r1dir"
)

func honestyCmd(args []string) {
	os.Exit(runHonestyCmd(args, os.Stdout, os.Stderr))
}

func runHonestyCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: r1 honesty <refuse|why-not|list> [flags]")
		return 2
	}
	switch args[0] {
	case "refuse":
		return runHonestyRefuse(args[1:], stdout, stderr)
	case "why-not":
		return runHonestyWhyNot(args[1:], stdout, stderr)
	case "list":
		return runHonestyList(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "honesty: unknown verb %q\n", args[0])
		return 2
	}
}

func runHonestyRefuse(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("honesty refuse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	taskID := fs.String("task", "", "task id")
	claim := fs.String("claim", "", "claim that R1 refuses to make")
	reason := fs.String("reason", "", "operator-facing refusal reason")
	evidence := fs.String("evidence", "", "comma-separated missing evidence list")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return recordHonestyDecision(*repo, honesty.Decision{
		Kind:      honesty.KindRefused,
		TaskID:    *taskID,
		Claim:     *claim,
		Reason:    *reason,
		Evidence:  splitCSV(*evidence),
		CreatedAt: time.Now().UTC(),
	}, stdout, stderr)
}

func runHonestyWhyNot(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("honesty why-not", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	taskID := fs.String("task", "", "task id")
	action := fs.String("action", "", "skipped/deferred/downgraded action")
	reason := fs.String("reason", "", "why the action was not taken")
	overrideBy := fs.String("override-by", "", "human override identity")
	evidence := fs.String("evidence", "", "comma-separated evidence list")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return recordHonestyDecision(*repo, honesty.Decision{
		Kind:       honesty.KindWhyNot,
		TaskID:     *taskID,
		Action:     *action,
		Reason:     *reason,
		OverrideBy: *overrideBy,
		Evidence:   splitCSV(*evidence),
		CreatedAt:  time.Now().UTC(),
	}, stdout, stderr)
}

func recordHonestyDecision(repo string, d honesty.Decision, stdout, stderr io.Writer) int {
	lg, err := ledger.New(r1dir.CanonicalPathFor(repo, "ledger"))
	if err != nil {
		fmt.Fprintf(stderr, "honesty: open ledger: %v\n", err)
		return 1
	}
	defer lg.Close()
	nodeID, err := honesty.Record(lg, "r1 honesty", d.TaskID, d)
	if err != nil {
		fmt.Fprintf(stderr, "honesty: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s %s\n", d.Kind, nodeID)
	return 0
}

func runHonestyList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("honesty list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	taskID := fs.String("task", "", "task id")
	asJSON := fs.Bool("json", false, "emit json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	lg, err := ledger.New(r1dir.CanonicalPathFor(*repo, "ledger"))
	if err != nil {
		fmt.Fprintf(stderr, "honesty: open ledger: %v\n", err)
		return 1
	}
	defer lg.Close()
	items, err := honesty.Query(lg, *taskID)
	if err != nil {
		fmt.Fprintf(stderr, "honesty: query: %v\n", err)
		return 1
	}
	if *asJSON {
		return encodeJSON(stdout, items, stderr)
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, "no honesty decisions")
		return 0
	}
	for _, item := range items {
		fmt.Fprintf(stdout, "%s task=%s reason=%s\n", item.Kind, item.TaskID, item.Reason)
	}
	return 0
}

func encodeJSON(stdout io.Writer, v any, stderr io.Writer) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "json encode: %v\n", err)
		return 1
	}
	return 0
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
