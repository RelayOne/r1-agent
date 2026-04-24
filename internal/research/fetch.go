// fetch.go defines the Fetcher indirection used by VerifyClaim and
// ResearchExecutor. The interface exists so tests can plug a
// deterministic StubFetcher without reaching for net/http, and so
// future work can substitute a browser-backed fetcher (for
// JavaScript-rendered sites) without changing the verifier call site.
//
// The production HTTPFetcher is a small, self-contained client that:
//   - enforces a host allowlist (STOKE_RESEARCH_ALLOWLIST, comma-sep,
//     suffix-matched) when set; otherwise permits any HTTPS host
//     other than private / loopback ranges;
//   - caps response body at MaxBodyBytes (default 2 MiB) to avoid
//     memory blowup on PDF / binary resources;
//   - uses a 20-second per-request timeout so a slow cited host
//     cannot stall an entire descent pass.
//
// The HTTPFetcher is intentionally not wired into the package's
// defaults — callers construct it explicitly so test harnesses can
// default to StubFetcher.

package research

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/r1env"
)

// Fetcher is the minimal interface the verifier (and the executor)
// needs to confirm a claim's cited URL. Production wires an
// HTTPFetcher; tests wire a StubFetcher.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (body string, err error)
}

// MaxBodyBytes caps how much of any single page we read. Pages above
// this limit are truncated (verification runs on the prefix). 2 MiB
// is enough for every doc site we've seen while bounding worst-case
// memory to a manageable fraction of a single goroutine's stack+heap.
const MaxBodyBytes = 2 * 1024 * 1024

// HTTPFetcher is the production Fetcher. All fields are optional;
// callers typically construct via NewHTTPFetcher.
type HTTPFetcher struct {
	Client     *http.Client
	Allowlist  []string // host suffixes; empty = allow all non-private
	UserAgent  string
	MaxBody    int64
	RequireTLS bool // if true, reject http:// URLs
}

// NewHTTPFetcher returns an HTTPFetcher with sensible defaults. Reads
// STOKE_RESEARCH_ALLOWLIST as a comma-separated suffix list. Honors
// STOKE_RESEARCH_REQUIRE_TLS=1 for strict https-only mode.
func NewHTTPFetcher() *HTTPFetcher {
	allow := []string{}
	if raw := strings.TrimSpace(r1env.Get("R1_RESEARCH_ALLOWLIST", "STOKE_RESEARCH_ALLOWLIST")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			p := strings.ToLower(strings.TrimSpace(part))
			if p != "" {
				allow = append(allow, p)
			}
		}
	}
	return &HTTPFetcher{
		Client:     &http.Client{Timeout: 20 * time.Second},
		Allowlist:  allow,
		UserAgent:  "stoke-research/1.0 (+https://github.com/RelayOne/r1)",
		MaxBody:    MaxBodyBytes,
		RequireTLS: r1env.Get("R1_RESEARCH_REQUIRE_TLS", "STOKE_RESEARCH_REQUIRE_TLS") == "1",
	}
}

// Fetch implements Fetcher against a real HTTP server.
func (h *HTTPFetcher) Fetch(ctx context.Context, rawURL string) (string, error) {
	if h == nil {
		return "", fmt.Errorf("research: HTTPFetcher is nil")
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if h.RequireTLS && u.Scheme != "https" {
		return "", fmt.Errorf("https required, got %q", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("url has no host")
	}
	if isPrivateHost(host) {
		return "", fmt.Errorf("host %q is private/loopback and not allowed", host)
	}
	if len(h.Allowlist) > 0 && !hostOnAllowlist(host, h.Allowlist) {
		return "", fmt.Errorf("host %q not on allowlist", host)
	}

	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	if h.UserAgent != "" {
		req.Header.Set("User-Agent", h.UserAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http %d from %s", resp.StatusCode, u.String())
	}

	limit := h.MaxBody
	if limit <= 0 {
		limit = MaxBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return string(body), nil
}

// hostOnAllowlist reports whether host ends with one of the allowlist
// suffixes. Matching is case-insensitive and requires a host-boundary
// (so "evilapi.com" does not match a "api.com" suffix unless "api.com"
// appears as a full suffix component).
func hostOnAllowlist(host string, allow []string) bool {
	host = strings.ToLower(host)
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

// isPrivateHost reports whether host is a loopback or private-range
// address (IPv4 RFC 1918, link-local, loopback; IPv6 loopback and ULA).
// Hostnames that resolve to a private IP are accepted at this layer —
// the goal here is to catch literal private addresses in cited URLs
// (e.g. http://127.0.0.1/, http://192.168.1.1/).
func isPrivateHost(host string) bool {
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // hostname; allow
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	return false
}

// StubFetcher is a deterministic in-memory Fetcher for tests. The
// Pages map keys are URLs (exact match) and values are the response
// bodies. An unmapped URL returns the Err value (default: not-found).
type StubFetcher struct {
	Pages map[string]string
	Err   error // returned when a URL is not in Pages; default: not-found error
}

// Fetch implements Fetcher against the in-memory Pages map.
func (s *StubFetcher) Fetch(ctx context.Context, url string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("research: StubFetcher is nil")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	if body, ok := s.Pages[url]; ok {
		return body, nil
	}
	if s.Err != nil {
		return "", s.Err
	}
	return "", fmt.Errorf("stub: no page for %q", url)
}
