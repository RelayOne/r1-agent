// Package main: r1 cortex memory audit subcommand.
//
// Spec: specs/cortex-concerns.md item 31. Reads the curator audit log
// at ~/.r1/cortex/curator-audit.jsonl and pretty-prints it as a
// fixed-column table:
//
//	TIMESTAMP            | CATEGORY      | CONTENT (truncated)              | DECISION
//
// Used by operators to spot-check the curator's auto-apply decisions
// without grepping JSONL by hand. The CLI is read-only — operators
// edit the underlying memory.Store via `r1 init` / direct memory file
// edits, not via this command.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/RelayOne/r1/internal/cortex/lobes/memorycurator"
)

// auditDefaultRelPath is the curator audit log path relative to $HOME.
// Matches the path the curator writes to (privacy.go AuditLogPath
// default). Centralised here so the CLI and the writer agree.
const auditDefaultRelPath = ".r1/cortex/curator-audit.jsonl"

// cortexCmd dispatches the cortex parent subcommand. Today only
// `r1 cortex memory audit` is wired; future cortex subcommands land
// alongside it in this same dispatch.
func cortexCmd(args []string) {
	os.Exit(runCortexCmd(args, os.Stdout, os.Stderr))
}

// runCortexCmd is the testable entry point: takes args + io.Writer
// pair so tests can capture output. Returns an exit code.
func runCortexCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: r1 cortex <memory> ...")
		return 2
	}
	switch args[0] {
	case "memory":
		return runCortexMemoryCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "cortex: unknown subcommand %q\n", args[0])
		return 2
	}
}

// runCortexMemoryCmd dispatches the `r1 cortex memory <verb>` family.
// Today only `audit` is wired.
func runCortexMemoryCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: r1 cortex memory <audit>")
		return 2
	}
	switch args[0] {
	case "audit":
		return runCortexMemoryAuditCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "cortex memory: unknown verb %q\n", args[0])
		return 2
	}
}

// runCortexMemoryAuditCmd reads the curator audit log JSONL and prints
// one row per entry as a fixed-column table.
//
// Path resolution: $HOME is read from the environment at call time
// (NOT cached at package init) so tests can override HOME via
// t.Setenv before invoking. Production callers see HOME populated by
// the shell.
func runCortexMemoryAuditCmd(args []string, stdout, stderr io.Writer) int {
	_ = args // no flags today; reserved for --path override

	home := os.Getenv("HOME")
	if home == "" {
		fmt.Fprintln(stderr, "cortex memory audit: $HOME unset")
		return 2
	}
	path := filepath.Join(home, auditDefaultRelPath)

	f, err := os.Open(path) // #nosec G304 -- path is fixed under $HOME.
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(stdout, "(no curator audit log at "+path+")")
			return 0
		}
		fmt.Fprintf(stderr, "open %s: %v\n", path, err)
		return 1
	}
	defer f.Close()

	// Header row. Column widths are tuned so a typical 60-char fact
	// content fits without horizontal scroll on an 80-col terminal.
	fmt.Fprintf(stdout, "%-25s | %-13s | %-60s | %s\n",
		"TIMESTAMP", "CATEGORY", "CONTENT", "DECISION")
	fmt.Fprintln(stdout, "------------------------- | ------------- | ------------------------------------------------------------ | --------------")

	scanner := bufio.NewScanner(f)
	// Some audit lines may be longer than the default 64KiB token;
	// bump the buffer to 1MiB. Realistic content is ≤200 chars so
	// this is a defensive ceiling, not a tight bound.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	count := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ent memorycurator.AuditEntry
		if err := json.Unmarshal(line, &ent); err != nil {
			fmt.Fprintf(stderr, "skip malformed line: %v\n", err)
			continue
		}
		content := ent.Content
		if len(content) > 80 {
			content = content[:77] + "..."
		}
		// Truncate to 60 column width for the column too (the 80-char
		// pre-truncation above is a content-cap; this is a column-cap).
		if len(content) > 60 {
			content = content[:57] + "..."
		}
		fmt.Fprintf(stdout, "%-25s | %-13s | %-60s | %s\n",
			ent.Timestamp, ent.Category, content, ent.Decision)
		count++
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(stderr, "scan %s: %v\n", path, err)
		return 1
	}

	fmt.Fprintf(stdout, "\n%d audit entries.\n", count)
	return 0
}
