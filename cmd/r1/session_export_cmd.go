// Package main — session_export_cmd.go
//
// Spec C1 §T3. `r1 session export <id> [-o file] [--force] [--park]`
// streams a .r1session bundle from the local daemon to either a file
// (-o <path>) or stdout (-o -).
//
// The CLI is a thin wrapper around the daemon's
// POST /api/session/{id}/migrate-out HTTP endpoint: it auth-flows
// via the daemon's bearer (loaded from ~/.r1/daemon.json), opens the
// output stream, and copies bytes verbatim. No bundle parsing
// happens on the CLI side — the daemon is the canonical bundle
// writer.

package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/RelayOne/r1/internal/daemondisco"
)

// runSessionExportCmd parses the export-verb flags and runs the
// streaming request. Returns a UNIX-style exit code (0 ok / 1 runtime
// / 2 usage).
func runSessionExportCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", `output path (- for stdout; default "<id>.r1session")`)
	force := fs.Bool("force", false, "interrupt a mid-turn session at the next quiet point")
	park := fs.Bool("park", false, "leave the source session in migrated-out state on success")
	addr := fs.String("addr", "", "daemon address (default: read ~/.r1/daemon.json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: r1 session export <id> [-o file] [--force] [--park]")
		return 2
	}
	sessionID := rest[0]
	outPath := *out
	if outPath == "" {
		outPath = sessionID + ".r1session"
	}

	// Resolve the daemon endpoint via the same path used by
	// daemonHTTP. We DON'T call daemonHTTP itself because it pretty-
	// prints JSON responses and we need raw bytes here.
	resolved, err := resolveDaemonEndpoint(*addr, "")
	if err != nil {
		fmt.Fprintf(stderr, "r1 session export: %v\n", err)
		return 1
	}

	q := url.Values{}
	if *force {
		q.Set("force", "1")
	}
	if *park {
		q.Set("park", "1")
	}
	target := fmt.Sprintf("http://%s/api/session/%s/migrate-out", resolved.Addr, url.PathEscape(sessionID))
	if encoded := q.Encode(); encoded != "" {
		target += "?" + encoded
	}
	req, err := http.NewRequest(http.MethodPost, target, nil)
	if err != nil {
		fmt.Fprintf(stderr, "r1 session export: build request: %v\n", err)
		return 1
	}
	if resolved.Token != "" {
		req.Header.Set("Authorization", "Bearer "+resolved.Token)
	}
	// Silence the linter — daemondisco may be used implicitly via
	// resolveDaemonEndpoint, but we import it for the side effect
	// of consistent error formatting across r1 client subcommands.
	_ = daemondisco.FileName

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "r1 session export: daemon unreachable: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(stderr, "r1 session export: daemon %d: %s\n", resp.StatusCode, body)
		return 1
	}

	var w io.Writer
	if outPath == "-" {
		w = stdout
	} else {
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintf(stderr, "r1 session export: create %s: %v\n", outPath, err)
			return 1
		}
		defer f.Close()
		w = f
	}
	written, err := io.Copy(w, resp.Body)
	if err != nil {
		fmt.Fprintf(stderr, "r1 session export: copy: %v\n", err)
		return 1
	}
	if outPath != "-" {
		fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", outPath, written)
	}
	return 0
}
