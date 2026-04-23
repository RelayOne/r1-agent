// Package mcp — discovery.go — best-effort probe of a remote MCP
// server's /.well-known/mcp.json to cross-check the operator's
// configured transport + advertised tool list against what the
// server publishes. See specs/mcp-client.md §Discovery.
//
// Contract (spec §Discovery, checklist item 6):
//   - GETs cfg.URL + "/.well-known/mcp.json" honoring any deadline
//     on the caller-provided context; falls back to a 500ms total
//     timeout when the context has none.
//   - HTTP 404, connection refused, and timeouts are NON-fatal:
//     returns (nil, nil) + logs a single line via log.Printf so
//     operators can see probes that went nowhere.
//   - Transport mismatch between WellKnown.Transport and
//     cfg.Transport IS fatal: the operator's yaml disagrees with
//     what the server publishes, which is an alignment bug worth
//     surfacing loudly.
//   - Bad JSON body (on an otherwise-successful 200) IS fatal for
//     the same reason: the server is broken or someone is MITM'ing
//     the probe.
//
// Boundaries (spec §Boundaries, checklist item 6):
//   - No fsnotify / new deps: stdlib net/http + encoding/json only.
//   - Does not fetch tool schemas; that is a post-initialize concern
//     handled by Client.ListTools in the transport files.
//   - Not wired into any transport directly; the registry (MCP-8)
//     will call Discover before Client.Initialize.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultDiscoveryTimeout is the total deadline applied to a Discover
// call when the caller-provided context has no deadline of its own.
// Kept short per spec §Discovery (“Failure to fetch is non-fatal”)
// so a dead/slow server can never stall startup.
const defaultDiscoveryTimeout = 500 * time.Millisecond

// maxDiscoveryBody caps the size of a well-known response we will
// ingest. The real mcp.json payloads are ~1-4 KiB; the cap keeps a
// malicious/misconfigured server from exhausting memory during a
// probe.
const maxDiscoveryBody = 64 * 1024

// WellKnown is the parsed /.well-known/mcp.json payload. Field
// shape follows the working draft referenced in specs/mcp-client.md
// §Discovery (Ekamoira 2026 guide):
//
//	{
//	  "version":     "2025-11-25",
//	  "transport":   "streamable_http",
//	  "tools":       ["list_issues", "create_issue", ...],
//	  "description": "GitHub MCP server"
//	}
//
// Only the four fields the spec cross-checks against are parsed;
// unknown keys are ignored so a future server revision does not
// break the probe.
type WellKnown struct {
	Version     string   `json:"version"`
	Transport   string   `json:"transport"`
	Tools       []string `json:"tools"`
	Description string   `json:"description"`
}

// Discover GETs cfg.URL + "/.well-known/mcp.json" and returns the
// parsed payload. See package doc for the full contract; the quick
// summary:
//
//   - nil httpClient is allowed; falls back to http.DefaultClient.
//   - ctx governs the request. If it has no deadline, a 500ms total
//     deadline is applied.
//   - 404 / timeout / connection-refused -> (nil, nil) + log.Printf.
//     All other HTTP errors (>=400 except 404) return an error.
//   - Transport mismatch vs cfg.Transport -> descriptive error.
//   - Malformed JSON -> error (server lied about its content-type).
//
// Only meaningful for non-stdio transports (stdio servers have no
// URL), but the function tolerates an empty URL by returning
// (nil, nil) and logging so callers need not special-case.
func Discover(ctx context.Context, cfg ServerConfig, httpClient *http.Client) (*WellKnown, error) {
	if cfg.URL == "" {
		// stdio or mis-configured http server; nothing to probe.
		return nil, nil
	}

	probeURL, err := buildWellKnownURL(cfg.URL)
	if err != nil {
		// Bad URL in config IS fatal — operator typo, worth
		// surfacing up to the registry loader.
		return nil, fmt.Errorf("mcp: discovery: bad url for %q: %w", cfg.Name, err)
	}

	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	// Apply the 500ms floor only when the caller did not bound ctx.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultDiscoveryTimeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: discovery: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		// Connection refused, DNS fail, timeout, TLS error — all
		// non-fatal per spec. The registry fall-through to
		// initialize + tools/list is the source of truth.
		if isNonFatalTransportErr(err) {
			log.Printf("mcp: well-known discovery: %s: %v", cfg.Name, err)
			return nil, nil
		}
		return nil, fmt.Errorf("mcp: discovery: %s: %w", cfg.Name, err)
	}
	defer resp.Body.Close()

	// 404 = server does not publish a manifest. Non-fatal.
	if resp.StatusCode == http.StatusNotFound {
		log.Printf("mcp: well-known discovery: %s: %v", cfg.Name,
			fmt.Errorf("404 not found at %s", probeURL))
		return nil, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp: discovery: %s: unexpected status %d", cfg.Name, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryBody+1))
	if err != nil {
		if isNonFatalTransportErr(err) {
			log.Printf("mcp: well-known discovery: %s: %v", cfg.Name, err)
			return nil, nil
		}
		return nil, fmt.Errorf("mcp: discovery: %s: read body: %w", cfg.Name, err)
	}
	if len(body) > maxDiscoveryBody {
		return nil, fmt.Errorf("mcp: discovery: %s: body exceeds %d byte cap", cfg.Name, maxDiscoveryBody)
	}

	var wk WellKnown
	if err := json.Unmarshal(body, &wk); err != nil {
		return nil, fmt.Errorf("mcp: discovery: %s: malformed mcp.json: %w", cfg.Name, err)
	}

	// Transport cross-check. Empty Transport in the payload means
	// the server did not advertise — treat as no-opinion, pass.
	if wk.Transport != "" && cfg.Transport != "" && !transportEqual(wk.Transport, cfg.Transport) {
		return nil, fmt.Errorf(
			"mcp: discovery: %s: transport mismatch: config=%q but server advertises %q",
			cfg.Name, cfg.Transport, wk.Transport,
		)
	}

	return &wk, nil
}

// buildWellKnownURL normalizes cfg.URL and appends the well-known
// path, preserving scheme + host but discarding any query string on
// the configured URL (the probe path is absolute on the host root).
func buildWellKnownURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("url missing scheme or host: %q", raw)
	}
	u.Path = "/.well-known/mcp.json"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// transportEqual normalizes transport strings before comparison.
// mcp.json drafts have used both "streamable-http" and
// "streamable_http"; Stoke's own config uses the underscore form
// (see ServerConfig.Transport). Normalization keeps the cross-check
// robust against that churn.
func transportEqual(a, b string) bool {
	return normalizeTransport(a) == normalizeTransport(b)
}

func normalizeTransport(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

// isNonFatalTransportErr returns true for the classes of network
// failure the spec explicitly flags as non-fatal: context deadline,
// connection refused, DNS resolve miss, etc. Anything else (e.g.
// malformed response caught by http.Client) is treated as a real
// error.
func isNonFatalTransportErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	// net.OpError / *url.Error wrappers stringify to include
	// "connection refused", "no such host", "i/o timeout" etc.
	// Matching on substring keeps the check stdlib-only without
	// pulling in syscall-specific errno checks.
	msg := err.Error()
	for _, needle := range []string{
		"connection refused",
		"no such host",
		"i/o timeout",
		"connection reset",
		"network is unreachable",
		"timeout",
		"EOF",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
