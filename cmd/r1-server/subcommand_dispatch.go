// Package main — subcommand_dispatch.go
//
// work-stoke T16: intercept os.Args before main() sees them so the
// existing flag-only entry point (serve) can keep its shape while we
// add sibling verbs like `import`. The dispatch lives in init() so
// it runs before any flag parsing kicks in, and is kept in its own
// file so auto-formatter / linter runs that touch main.go cannot
// accidentally strip the routing logic.
//
// Today's verbs:
//
//	r1-server import <bundle.tracebundle> [--data-dir DIR]
//	r1-server serve                              (explicit; also the default)
//	r1-server [--version|--any-flag]             (legacy flag-only form)
//
// Unknown subcommands exit 2 with a usage banner.

package main

import (
	"fmt"
	"os"
	"strings"
)

func init() {
	if len(os.Args) <= 1 {
		return
	}
	arg := os.Args[1]
	if strings.HasPrefix(arg, "-") {
		// Flag form — leave os.Args alone; main's flag.Parse handles it.
		return
	}
	switch arg {
	case "import":
		os.Exit(runImportCmd(os.Args[2:], os.Stdout, os.Stderr))
	case "serve":
		// Strip the verb so main's flag.Parse sees the legacy layout.
		os.Args = append(os.Args[:1], os.Args[2:]...)
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stderr, "usage: r1-server [serve] [--version] [flags]")
		fmt.Fprintln(os.Stderr, "       r1-server import <bundle.tracebundle> [--data-dir DIR]")
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "r1-server: unknown subcommand %q\n", arg)
		fmt.Fprintln(os.Stderr, "usage: r1-server [serve] [--version] [flags]")
		fmt.Fprintln(os.Stderr, "       r1-server import <bundle.tracebundle>")
		os.Exit(2)
	}
}
