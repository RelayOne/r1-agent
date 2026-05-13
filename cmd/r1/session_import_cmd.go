// Package main — session_import_cmd.go
//
// Spec C1 §T8. `r1 session import <file>` streams a .r1session bundle
// from a local file into the daemon's POST /api/session/migrate-in
// endpoint and prints the destination's JSON response (new_session_id
// + chain_root_hash + idempotent flag).

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

// runSessionImportCmd parses flags + opens the bundle file + posts.
// Returns 0 / 1 / 2 like the other CLI subcommands.
func runSessionImportCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "", "daemon address (default: read ~/.r1/daemon.json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: r1 session import <file>")
		return 2
	}
	path := rest[0]

	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "r1 session import: open %s: %v\n", path, err)
		return 1
	}
	defer f.Close()

	resolved, err := resolveDaemonEndpoint(*addr, "")
	if err != nil {
		fmt.Fprintf(stderr, "r1 session import: %v\n", err)
		return 1
	}
	target := fmt.Sprintf("http://%s/api/session/migrate-in", resolved.Addr)
	req, err := http.NewRequest(http.MethodPost, target, f)
	if err != nil {
		fmt.Fprintf(stderr, "r1 session import: build request: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/gzip")
	if resolved.Token != "" {
		req.Header.Set("Authorization", "Bearer "+resolved.Token)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "r1 session import: daemon unreachable: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fmt.Fprintf(stderr, "r1 session import: daemon %d: %s\n", resp.StatusCode, body)
		return 1
	}
	// Pretty-print the JSON success body so the operator sees the
	// new_session_id and chain_root_hash directly.
	var pretty map[string]any
	if err := json.Unmarshal(body, &pretty); err != nil {
		// Fall through with the raw bytes.
		fmt.Fprintln(stdout, string(body))
		return 0
	}
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Fprintln(stdout, string(out))
	return 0
}
