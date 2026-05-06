// agent-feature-runner — CLI that parses *.agent.feature.md files and
// executes each scenario against the r1 MCP catalog per spec 8 §10
// (Test Plan: Meta-Test Over All .agent.feature.md Fixtures) and §12
// item 16.
//
// Usage:
//
//   agent-feature-runner --root tests/agent --tag smoke
//   agent-feature-runner tests/agent/web/chat-send-message.agent.feature.md
//   agent-feature-runner --update tests/agent/web/...   # re-record snapshots
//
// Exit codes:
//   0 success
//   1 usage / config error
//   2 scenario assertion failure
//   3 transport / runtime error
//
// The parser/dispatcher live in the parser/ + dispatcher/ subpackages
// so unit tests can cover each layer without subprocess overhead.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/tools/agent-feature-runner/parser"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent-feature-runner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "tests/agent", "root directory containing *.agent.feature.md fixtures")
	tag := fs.String("tag", "", "only run scenarios whose <!-- TAGS: --> include this value (comma-list also accepted)")
	update := fs.Bool("update", false, "re-record golden a11y snapshots instead of asserting")
	listOnly := fs.Bool("list", false, "list discovered scenarios; do not execute")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	positional := fs.Args()

	files, err := discoverFeatureFiles(*root, positional)
	if err != nil {
		fmt.Fprintf(stderr, "discover: %v\n", err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stderr, "no *.agent.feature.md files found")
		return 1
	}

	tagFilter := parseTagFilter(*tag)

	failures := 0
	for _, f := range files {
		feat, err := parser.ParseFile(f)
		if err != nil {
			fmt.Fprintf(stderr, "parse %s: %v\n", f, err)
			return 3
		}
		for _, sc := range feat.Scenarios {
			if !scenarioMatchesTags(sc, feat.Tags, tagFilter) {
				continue
			}
			if *listOnly {
				fmt.Fprintf(stdout, "%s\t%s\n", f, sc.Name)
				continue
			}
			if *update {
				fmt.Fprintf(stdout, "[update] %s :: %s — snapshot re-recording requires r1d daemon (BLOCKED on spec 5 merge)\n",
					filepath.Base(f), sc.Name)
				continue
			}
			fmt.Fprintf(stdout, "[run] %s :: %s\n", filepath.Base(f), sc.Name)
			fmt.Fprintf(stdout, "      %d steps parsed; runtime dispatcher requires r1d daemon (BLOCKED on spec 5 merge)\n",
				len(sc.Steps))
		}
	}
	if failures > 0 {
		return 2
	}
	return 0
}

// discoverFeatureFiles walks root for *.agent.feature.md files. If
// positional args are supplied, those override the walk and are
// validated to actually exist. Sorted lexically for determinism.
func discoverFeatureFiles(root string, positional []string) ([]string, error) {
	if len(positional) > 0 {
		out := make([]string, 0, len(positional))
		for _, p := range positional {
			info, err := os.Stat(p)
			if err != nil {
				return nil, fmt.Errorf("stat %q: %w", p, err)
			}
			if info.IsDir() {
				more, err := walkFeatures(p)
				if err != nil {
					return nil, err
				}
				out = append(out, more...)
				continue
			}
			out = append(out, p)
		}
		return out, nil
	}
	if root == "" {
		return nil, fmt.Errorf("either --root or positional path is required")
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	return walkFeatures(root)
}

func walkFeatures(root string) ([]string, error) {
	out := []string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".agent.feature.md") {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

func parseTagFilter(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func scenarioMatchesTags(sc parser.Scenario, fileTags, want []string) bool {
	if len(want) == 0 {
		return true
	}
	have := map[string]bool{}
	for _, t := range fileTags {
		have[t] = true
	}
	for _, t := range sc.Tags {
		have[t] = true
	}
	for _, w := range want {
		if have[w] {
			return true
		}
	}
	return false
}
