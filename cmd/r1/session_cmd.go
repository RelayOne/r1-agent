// Package main — session_cmd.go
//
// Spec C1 §T3 + §T8 + §T16. Dispatches the `r1 session <verb>` CLI
// subcommand family. Three verbs land in this drop:
//
//   - r1 session export <id> [-o file] [--force] [--park]
//   - r1 session import <file>
//   - r1 session migrate <id> --to <dest-url>
//
// Each verb's implementation lives in its own file:
// session_export_cmd.go / session_import_cmd.go /
// session_migrate_cmd.go. The dispatcher here keeps main.go's
// switch statement small (matching cmd/r1/sessions.go's idiom for
// the existing read-only `r1 sessions` family).

package main

import (
	"fmt"
	"os"
)

// sessionCmd is the `r1 session` subcommand entry point. The first
// positional arg is the verb; remaining args are forwarded to the
// verb-specific runner.
func sessionCmd(args []string) {
	if len(args) == 0 {
		args = []string{"help"}
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "export":
		os.Exit(runSessionExportCmd(rest, os.Stdout, os.Stderr))
	case "import":
		os.Exit(runSessionImportCmd(rest, os.Stdout, os.Stderr))
	case "migrate":
		os.Exit(runSessionMigrateCmd(rest, os.Stdout, os.Stderr))
	case "help", "-h", "--help":
		printSessionHelp(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown session subcommand: %q\n", verb)
		printSessionHelp(os.Stderr)
		os.Exit(2)
	}
}

func printSessionHelp(w *os.File) {
	fmt.Fprintln(w, "r1 session <verb> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  export <id> [-o file] [--force] [--park]")
	fmt.Fprintln(w, "      Export the session to a .r1session bundle. Default -o is")
	fmt.Fprintln(w, "      <id>.r1session in the current directory; -o - streams to")
	fmt.Fprintln(w, "      stdout. --force interrupts a mid-turn session; --park")
	fmt.Fprintln(w, "      leaves the source in migrated-out state on success.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  import <file>")
	fmt.Fprintln(w, "      Import a .r1session bundle into the local daemon. Prints")
	fmt.Fprintln(w, "      the destination's new_session_id + verified chain root.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  migrate <id> --to <dest-url>")
	fmt.Fprintln(w, "      One-step migrate: pipes the local daemon's migrate-out")
	fmt.Fprintln(w, "      response into <dest-url>/api/session/migrate-in over a")
	fmt.Fprintln(w, "      single HTTP request. The remote bearer is loaded from")
	fmt.Fprintln(w, "      ~/.r1/config.json remote_daemons[<dest-url>].bearer.")
}
