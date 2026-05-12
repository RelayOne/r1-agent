// Package main — session_migrate_cmd.go
//
// Spec C1 §T16. `r1 session migrate <id> --to <dest-url>` pipes the
// local daemon's migrate-out response into <dest-url>'s migrate-in
// over a single HTTP request — the operator's pre-staging gives the
// destination's URL and bearer; this command stitches the two halves
// without touching disk on the operator's machine.
//
// Resilience: on dest-side failure the source remains in the
// migrating-out state (see internal/server/sessionhub.BeginMigrateOut)
// so re-running this command after fixing the dest issue is safe.
// The CLI itself stays stateless — it doesn't track retry state.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// remoteDaemonConfig is the shape we expect under
// ~/.r1/config.json's remote_daemons map.
type remoteDaemonConfig struct {
	Bearer string `json:"bearer"`
}

// configFile is the on-disk shape of ~/.r1/config.json that this
// command reads from. We intentionally redeclare it inline rather
// than depending on internal/config — that package has its own
// schema we don't want to perturb, and the migration-CLI's needs
// are tiny.
type configFile struct {
	RemoteDaemons map[string]remoteDaemonConfig `json:"remote_daemons"`
}

// runSessionMigrateCmd parses flags, pipes the bundle.
func runSessionMigrateCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	destURL := fs.String("to", "", "destination daemon URL (e.g. http://dest:8080)")
	force := fs.Bool("force", false, "interrupt a mid-turn source session")
	park := fs.Bool("park", false, "park the source session in migrated-out state")
	bearer := fs.String("bearer", "", "destination bearer (defaults to ~/.r1/config.json)")
	addr := fs.String("addr", "", "source daemon address (default: ~/.r1/daemon.json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 || *destURL == "" {
		fmt.Fprintln(stderr, "usage: r1 session migrate <id> --to <dest-url>")
		return 2
	}
	sessionID := rest[0]

	destBearer := *bearer
	if destBearer == "" {
		if b, err := lookupRemoteBearer(*destURL); err == nil {
			destBearer = b
		}
	}

	// 1. Open the source-side migrate-out stream.
	resolved, err := resolveDaemonEndpoint(*addr, "")
	if err != nil {
		fmt.Fprintf(stderr, "r1 session migrate: source endpoint: %v\n", err)
		return 1
	}
	q := url.Values{}
	if *force {
		q.Set("force", "1")
	}
	if *park {
		q.Set("park", "1")
	}
	srcURL := fmt.Sprintf("http://%s/api/session/%s/migrate-out", resolved.Addr, url.PathEscape(sessionID))
	if encoded := q.Encode(); encoded != "" {
		srcURL += "?" + encoded
	}
	srcReq, err := http.NewRequest(http.MethodPost, srcURL, nil)
	if err != nil {
		fmt.Fprintf(stderr, "r1 session migrate: build source request: %v\n", err)
		return 1
	}
	if resolved.Token != "" {
		srcReq.Header.Set("Authorization", "Bearer "+resolved.Token)
	}
	srcResp, err := (&http.Client{}).Do(srcReq)
	if err != nil {
		fmt.Fprintf(stderr, "r1 session migrate: source unreachable: %v\n", err)
		return 1
	}
	defer srcResp.Body.Close()
	if srcResp.StatusCode >= 400 {
		body, _ := io.ReadAll(srcResp.Body)
		fmt.Fprintf(stderr, "r1 session migrate: source %d: %s\n", srcResp.StatusCode, body)
		return 1
	}

	// 2. Pipe the source response body straight into the dest's
	// migrate-in. The Go HTTP stack supports streaming bodies
	// natively, so we set ContentLength to -1 to indicate chunked.
	destEndpoint := strings.TrimRight(*destURL, "/") + "/api/session/migrate-in"
	destReq, err := http.NewRequest(http.MethodPost, destEndpoint, srcResp.Body)
	if err != nil {
		fmt.Fprintf(stderr, "r1 session migrate: build dest request: %v\n", err)
		return 1
	}
	destReq.Header.Set("Content-Type", "application/gzip")
	if destBearer != "" {
		destReq.Header.Set("Authorization", "Bearer "+destBearer)
	}
	destResp, err := (&http.Client{}).Do(destReq)
	if err != nil {
		fmt.Fprintf(stderr, "r1 session migrate: dest unreachable: %v\n", err)
		return 1
	}
	defer destResp.Body.Close()
	destBody, _ := io.ReadAll(destResp.Body)
	if destResp.StatusCode >= 400 {
		fmt.Fprintf(stderr, "r1 session migrate: dest %d: %s\n", destResp.StatusCode, destBody)
		return 1
	}
	// Pretty-print success body.
	var pretty map[string]any
	if err := json.Unmarshal(destBody, &pretty); err != nil {
		fmt.Fprintln(stdout, string(destBody))
		return 0
	}
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Fprintln(stdout, string(out))
	return 0
}

// lookupRemoteBearer reads ~/.r1/config.json for a stored remote-
// daemon bearer keyed by destURL. Returns the bearer string or an
// error if the file is missing / key is absent.
func lookupRemoteBearer(destURL string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".r1", "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var cfg configFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", err
	}
	if rd, ok := cfg.RemoteDaemons[destURL]; ok && rd.Bearer != "" {
		return rd.Bearer, nil
	}
	// Try a normalized key (trailing slash insensitive).
	norm := strings.TrimRight(destURL, "/")
	if rd, ok := cfg.RemoteDaemons[norm]; ok && rd.Bearer != "" {
		return rd.Bearer, nil
	}
	return "", fmt.Errorf("no bearer for %s in %s", destURL, path)
}
