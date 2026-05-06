package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/RelayOne/r1/internal/rules"
	"github.com/RelayOne/r1/internal/rules/monitor"
)

func rulesCmd(args []string) {
	os.Exit(runRulesCmd(args, os.Stdout, os.Stderr))
}

func runRulesCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: r1 rules <verb> [flags]")
		fmt.Fprintln(stderr, "verbs: list, show, disable, tail")
		return 2
	}
	switch args[0] {
	case "list":
		return runRulesListCmd(args[1:], stdout, stderr)
	case "show":
		return runRulesShowCmd(args[1:], stdout, stderr)
	case "disable":
		return runRulesDisableCmd(args[1:], stdout, stderr)
	case "tail":
		return runRulesTailCmd(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "usage: r1 rules <verb> [flags]")
		fmt.Fprintln(stdout, "verbs:")
		fmt.Fprintln(stdout, "  list     list configured rules")
		fmt.Fprintln(stdout, "  show     show one rule by name or id")
		fmt.Fprintln(stdout, "  disable  disable one rule by name or id")
		fmt.Fprintln(stdout, "  tail     stream live rule decisions")
		return 0
	default:
		fmt.Fprintf(stderr, "rules: unknown verb %q\n", args[0])
		return 2
	}
}

func runRulesListCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	reg, err := openRulesRegistry(*repo)
	if err != nil {
		fmt.Fprintf(stderr, "rules list: %v\n", err)
		return 1
	}
	list, err := reg.List()
	if err != nil {
		fmt.Fprintf(stderr, "rules list: %v\n", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "no rules")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tSTRATEGY\tSCOPE\tTOOL_FILTER")
	for _, rule := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", rule.Name, rule.Status, rule.EnforcementStrategy, rule.Scope, rule.ToolFilter)
	}
	_ = tw.Flush()
	return 0
}

func runRulesShowCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: r1 rules show [--repo PATH] <name>")
		return 2
	}
	reg, err := openRulesRegistry(*repo)
	if err != nil {
		fmt.Fprintf(stderr, "rules show: %v\n", err)
		return 1
	}
	rule, err := reg.Resolve(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "rules show: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rule); err != nil {
			fmt.Fprintf(stderr, "rules show: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "name: %s\nid: %s\nstatus: %s\nscope: %s\nstrategy: %s\ntool_filter: %s\ntext: %s\n",
		rule.Name, rule.ID, rule.Status, rule.Scope, rule.EnforcementStrategy, rule.ToolFilter, rule.Text)
	return 0
}

func runRulesDisableCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules disable", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: r1 rules disable [--repo PATH] <name>")
		return 2
	}
	reg, err := openRulesRegistry(*repo)
	if err != nil {
		fmt.Fprintf(stderr, "rules disable: %v\n", err)
		return 1
	}
	if err := reg.Disable(fs.Arg(0)); err != nil {
		fmt.Fprintf(stderr, "rules disable: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "disabled %s\n", fs.Arg(0))
	return 0
}

func runRulesTailCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules tail", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	last := fs.Int("last", 20, "print the last N decisions before following (0 = all)")
	follow := fs.Bool("follow", true, "keep streaming appended decisions")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintf(stderr, "rules tail: resolve repo: %v\n", err)
		return 1
	}
	ctx, stop := signalContext(context.Background())
	defer stop()
	if err := monitor.NewRepo(absRepo).Tail(ctx, stdout, *follow, *last); err != nil && err != context.Canceled {
		fmt.Fprintf(stderr, "rules tail: %v\n", err)
		return 1
	}
	return 0
}

func openRulesRegistry(repo string) (*rules.Registry, error) {
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	return rules.NewRepoRegistry(absRepo, nil), nil
}

func runRulesCommand(absRepo, raw string, printf func(format string, args ...interface{})) string {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		printf("usage: /rules list | show <name> | disable <name> | tail")
		return "rules help shown"
	}

	switch parts[0] {
	case "list":
		var out, errBuf bytes.Buffer
		if code := runRulesCmd([]string{"list", "--repo", absRepo}, &out, &errBuf); code != 0 {
			printf(strings.TrimSpace(errBuf.String()))
			return "rules list failed"
		}
		printf(strings.TrimSpace(out.String()))
		return "rules listed"
	case "show":
		if len(parts) < 2 {
			printf("usage: /rules show <name>")
			return "rules show failed"
		}
		var out, errBuf bytes.Buffer
		if code := runRulesCmd([]string{"show", "--repo", absRepo, parts[1]}, &out, &errBuf); code != 0 {
			printf(strings.TrimSpace(errBuf.String()))
			return "rules show failed"
		}
		printf(strings.TrimSpace(out.String()))
		return "rule shown"
	case "disable", "pause":
		if len(parts) < 2 {
			printf("usage: /rules disable <name>")
			return "rules disable failed"
		}
		var out, errBuf bytes.Buffer
		if code := runRulesCmd([]string{"disable", "--repo", absRepo, parts[1]}, &out, &errBuf); code != 0 {
			printf(strings.TrimSpace(errBuf.String()))
			return "rules disable failed"
		}
		printf(strings.TrimSpace(out.String()))
		return "rule disabled"
	default:
		printf("unsupported rules command: %s", parts[0])
		return "rules command unsupported"
	}
}
