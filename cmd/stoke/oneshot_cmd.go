// CLI entry for `stoke --one-shot <verb> [--input <path>]`.
//
// Thin adapter over internal/oneshot — parses the flag, pipes
// the payload through oneshot.RunFromFile, exits with a
// well-defined code. Kept in its own file so the main.go switch
// dispatch stays compact.
//
// Spec: CLOUDSWARM-R1-INTEGRATION.md §5.6.1.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/RelayOne/r1/internal/oneshot"
)

// runOneShotCmd is invoked from main when argv[1] is
// --one-shot / one-shot. Expects argv[2]=<verb> with optional
// --input <path> (defaults to stdin "-").
//
// Exit codes:
//
//	0 — success, JSON response on stdout.
//	1 — runtime error (read / marshal / write failure).
//	2 — usage error (missing verb, unknown verb, bad flag).
func runOneShotCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(stderr, "usage: stoke --one-shot <verb> [--input <path>]\n")
		fmt.Fprintf(stderr, "verbs: %s\n", strings.Join(oneshot.SupportedVerbs, ", "))
		return 2
	}
	verb := args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("stoke --one-shot "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		inputPath string
		jsonMode  bool
	)
	fs.StringVar(&inputPath, "input", "-", "path to JSON request payload; '-' for stdin")
	fs.BoolVar(&jsonMode, "json", false, "emit JSON output (accepted for CloudSwarm compatibility; one-shot output is always JSON)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	_ = jsonMode

	if err := oneshot.RunFromFile(verb, inputPath, stdout); err != nil {
		if errors.Is(err, oneshot.ErrUnknownVerb) {
			fmt.Fprintf(stderr, "%v\n", err)
			return 2
		}
		// Emit a machine-readable error envelope on stderr so
		// CloudSwarm's supervisor can parse without grepping free text.
		errEnv := map[string]string{
			"verb":   verb,
			"status": "error",
			"error":  err.Error(),
		}
		if b, merr := json.Marshal(errEnv); merr == nil {
			fmt.Fprintln(stderr, string(b))
		} else {
			fmt.Fprintf(stderr, "oneshot error: %v\n", err)
		}
		return 1
	}
	return 0
}
