// Package websearch is the multi-provider web-search adapter the
// feasibility gate uses to fetch external API documentation. Stoke's
// shippability contract refuses to build code against an external
// service when neither the SOW nor a web search produces usable
// documentation — no mocks are synthesized. This package is the
// "web search" half of that contract.
//
// Two provider implementations ship here:
//
//   - Tavily (cloud REST, needs TAVILY_API_KEY). Designed for doc
//     retrieval; returns cleaned excerpts + full page bodies.
//
//   - Shell (env-driven). Runs a user-configured command with the
//     query, captures stdout JSON. Lets an operator plug in any MCP
//     web-search server, Claude Code WebSearch tool via a CLI
//     wrapper, or a custom search pipeline without stoke needing
//     first-party support for every provider.
//
// Chain combines providers in fallback order: first success wins.
// DefaultFromEnv auto-assembles the chain based on which env vars
// are set; returns nil when no provider is configured, in which
// case the caller treats the absence as "no web search available"
// and falls back to SOW-only documentation enforcement.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Result is a single search hit.
type Result struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
	// Body is the full scraped page content when the provider
	// returns it. May be empty; callers should Fetch(URL) if they
	// need the full text.
	Body string `json:"body,omitempty"`
}

// Searcher is the cross-provider search interface.
type Searcher interface {
	// Name is a short identifier used in logs / telemetry.
	Name() string
	// Search returns up to maxResults hits for the query. Empty
	// slice + nil error means "no relevant results" — callers
	// should treat that as "try the next provider" when chaining.
	Search(ctx context.Context, query string, maxResults int) ([]Result, error)
}

// ErrUnavailable signals the provider is not configured in this
// environment (e.g. no API key). Callers in a Chain treat this as
// "try next provider" rather than as a hard failure.
var ErrUnavailable = errors.New("websearch: provider unavailable")

// Chain wraps multiple Searchers with first-success fallback.
// Providers are tried in order; the first one that returns at least
// one result wins. ErrUnavailable from any provider is swallowed —
// the chain treats it as "skip this one."
type Chain struct {
	Providers []Searcher
}

// Name returns a slash-joined list of provider names, useful for
// startup logs. E.g. "tavily/shell".
func (c *Chain) Name() string {
	if c == nil || len(c.Providers) == 0 {
		return "(empty chain)"
	}
	names := make([]string, 0, len(c.Providers))
	for _, p := range c.Providers {
		names = append(names, p.Name())
	}
	return strings.Join(names, "/")
}

// Search tries each provider in order; returns the first non-empty
// result set. When every provider returns empty results without
// error, Search returns an empty slice and nil error — "nothing
// relevant found" is a valid answer, not a failure.
func (c *Chain) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if c == nil || len(c.Providers) == 0 {
		return nil, ErrUnavailable
	}
	var lastErr error
	for _, p := range c.Providers {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		results, err := p.Search(ctx, query, maxResults)
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				continue
			}
			lastErr = err
			continue
		}
		if len(results) > 0 {
			return results, nil
		}
	}
	return nil, lastErr
}

// DefaultFromEnv returns a Chain auto-configured from env vars.
// Order: Tavily first (best doc retrieval when available), then
// Shell (catches MCP / custom wrappers). Returns nil when no
// provider is configured — caller must handle the "no web search
// available" case explicitly rather than relying on a broken
// fallback.
func DefaultFromEnv() Searcher {
	var providers []Searcher
	if t := TavilyFromEnv(); t != nil {
		providers = append(providers, t)
	}
	if s := ShellFromEnv(); s != nil {
		providers = append(providers, s)
	}
	if len(providers) == 0 {
		return nil
	}
	return &Chain{Providers: providers}
}

// -------- Tavily provider

// Tavily is a REST client for api.tavily.com/search. Configuration
// via NewTavily or TavilyFromEnv(). Default per-request timeout 20
// seconds — doc retrieval is latency-tolerant but we don't want the
// gate to hang forever when Tavily is degraded.
type Tavily struct {
	APIKey     string
	HTTPClient *http.Client
}

// NewTavily constructs a Tavily client. httpClient may be nil.
func NewTavily(apiKey string, httpClient *http.Client) *Tavily {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Tavily{APIKey: apiKey, HTTPClient: httpClient}
}

// TavilyFromEnv returns a *Tavily when TAVILY_API_KEY is set,
// nil otherwise.
func TavilyFromEnv() *Tavily {
	k := strings.TrimSpace(os.Getenv("TAVILY_API_KEY"))
	if k == "" {
		return nil
	}
	return NewTavily(k, nil)
}

func (t *Tavily) Name() string { return "tavily" }

type tavilyRequest struct {
	APIKey            string `json:"api_key"`
	Query             string `json:"query"`
	MaxResults        int    `json:"max_results"`
	IncludeAnswer     bool   `json:"include_answer"`
	IncludeRawContent bool   `json:"include_raw_content"`
	SearchDepth       string `json:"search_depth,omitempty"`
}

type tavilyResponse struct {
	Results []struct {
		URL        string `json:"url"`
		Title      string `json:"title"`
		Content    string `json:"content"`
		RawContent string `json:"raw_content"`
	} `json:"results"`
}

// Search posts to Tavily and returns up to maxResults hits. Returns
// ErrUnavailable when the API key is empty (defensive — caller should
// have ignored a nil Tavily from TavilyFromEnv already).
func (t *Tavily) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if t == nil || strings.TrimSpace(t.APIKey) == "" {
		return nil, ErrUnavailable
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	body, _ := json.Marshal(tavilyRequest{
		APIKey:            t.APIKey,
		Query:             query,
		MaxResults:        maxResults,
		IncludeAnswer:     false,
		IncludeRawContent: true,
		SearchDepth:       "advanced",
	})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.tavily.com/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("tavily: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tavily: HTTP %d: %s", resp.StatusCode, string(b))
	}
	var parsed tavilyResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("tavily: decode: %w", err)
	}
	out := make([]Result, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		out = append(out, Result{
			URL:     r.URL,
			Title:   r.Title,
			Excerpt: r.Content,
			Body:    r.RawContent,
		})
	}
	return out, nil
}

// -------- Shell provider (env-driven)

// Shell runs a user-configured command and parses its stdout as JSON.
// The command is resolved from WEBSEARCH_COMMAND env var at startup.
// The placeholder {{query}} in the command template is replaced with
// the URL-escaped query at call time. The command MUST emit a JSON
// array of {url,title,excerpt,body?} objects on stdout.
//
// Example WEBSEARCH_COMMAND values:
//
//   WEBSEARCH_COMMAND='my-mcp-tool search --format=json --query "{{query}}"'
//   WEBSEARCH_COMMAND='curl -s "https://my-search/?q={{query}}"'
//
// The 30-second timeout is intentionally long so a slow MCP call
// still completes; it's shorter than a SOW feasibility phase budget
// so a hung command doesn't block the gate forever.
type Shell struct {
	Command string
	Timeout time.Duration
}

// ShellFromEnv returns a *Shell when WEBSEARCH_COMMAND is set, nil
// otherwise. The command must contain the literal string "{{query}}"
// which will be replaced with the shell-escaped query at call time.
func ShellFromEnv() *Shell {
	c := strings.TrimSpace(os.Getenv("WEBSEARCH_COMMAND"))
	if c == "" {
		return nil
	}
	return &Shell{Command: c, Timeout: 30 * time.Second}
}

func (s *Shell) Name() string { return "shell" }

// Search invokes the configured command with the query substituted in.
// Returns ErrUnavailable when the command is unset.
func (s *Shell) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if s == nil || strings.TrimSpace(s.Command) == "" {
		return nil, ErrUnavailable
	}
	if !strings.Contains(s.Command, "{{query}}") {
		return nil, fmt.Errorf("websearch: WEBSEARCH_COMMAND missing {{query}} placeholder")
	}
	cmd := strings.ReplaceAll(s.Command, "{{query}}", shellEscape(query))
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, "bash", "-lc", cmd).Output()
	if err != nil {
		return nil, fmt.Errorf("websearch shell: %w", err)
	}
	var results []Result
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("websearch shell: expected JSON array on stdout: %w; output was: %s", err, truncateBytes(out, 500))
	}
	if maxResults > 0 && len(results) > maxResults {
		results = results[:maxResults]
	}
	return results, nil
}

// shellEscape returns a single-quoted shell-safe form of s. Avoids
// double-quoting because the Shell command might itself embed
// double-quoted segments.
func shellEscape(s string) string {
	// Replace ' with '\''
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}
