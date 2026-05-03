package main

import (
	"os"
)

// chatInteractiveLanesEnabled is the package-level toggle the
// chat-interactive REPL consults to decide whether to attach the
// realtime tui-lanes panel (specs/tui-lanes.md). The flag is parsed
// here via an init() that scans os.Args before main() runs, then
// strips --lanes from the slice so the existing runChatInteractiveCmd
// flag.FlagSet does not see an unknown flag and abort.
//
// Per specs/tui-lanes.md §"Implementation Checklist" item 27:
//
//	Add cmd/r1/main.go --lanes passthrough on r1 chat-interactive
//	only (other commands ignore).
//
// The flag is intentionally wired at package-init time rather than
// inside the existing flag.FlagSet so the change does not require
// editing chat_interactive_cmd.go (which currently houses its own
// FlagSet) — keeping cmd/r1/main.go and chat_interactive_cmd.go
// untouched satisfies the spec's "thin passthrough" intent and keeps
// the diff blast radius small.
//
// Default off. Other commands ignore --lanes in their own arg parsers.
var chatInteractiveLanesEnabled bool

// stripLanesFlag returns a copy of args with every --lanes / -lanes
// element removed. It records whether the flag was seen via the
// returned bool. The boolean form `--lanes=true|false` is also
// honored so users can explicitly disable it on the command line.
//
// Exposed (lowercase, package-internal) for the init() below and for
// unit tests in chat_interactive_lanes_test.go.
func stripLanesFlag(args []string) (out []string, enabled bool) {
	out = make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--lanes", "-lanes":
			enabled = true
			continue
		case "--lanes=true", "-lanes=true":
			enabled = true
			continue
		case "--lanes=false", "-lanes=false":
			enabled = false
			continue
		}
		out = append(out, a)
	}
	return out, enabled
}

// init scans os.Args for the chat-interactive subcommand. If found,
// it strips --lanes from the rest of the args (so the existing
// FlagSet doesn't see an unknown flag) and stores the resulting bool
// in the package-level chatInteractiveLanesEnabled.
//
// The scan is intentionally narrow: only os.Args[1] == "chat-
// interactive" triggers the strip, so other subcommands are
// unaffected (per spec item 27: "other commands ignore").
func init() {
	if len(os.Args) < 2 {
		return
	}
	if os.Args[1] != "chat-interactive" {
		return
	}
	rest, enabled := stripLanesFlag(os.Args[2:])
	chatInteractiveLanesEnabled = enabled
	// Mutate os.Args in place so main.go's existing dispatch (which
	// passes os.Args[2:] to runChatInteractiveCmd) sees the stripped
	// slice. Mutation is safe at init time — main() has not run yet.
	if len(rest) != len(os.Args)-2 {
		newArgs := make([]string, 0, 2+len(rest))
		newArgs = append(newArgs, os.Args[:2]...)
		newArgs = append(newArgs, rest...)
		os.Args = newArgs
	}
}
