// web_tools.go — web_fetch and web_search tool handlers.
//
// T-R1P-007: WebFetch tool — fetches a URL and returns the extracted text body.
// T-R1P-008: WebSearch tool — searches the web via the configured provider chain.
//
// Both tools are registered in Definitions() and dispatched by Handle().
// The tools degrade gracefully: web_search returns an informational string
// (not a Go error) when no provider is configured so the model can report
// "search unavailable" and continue with other tools.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/browser"
	"github.com/RelayOne/r1/internal/websearch"
)

// handleWebFetch implements the web_fetch tool (T-R1P-007).
func (r *Registry) handleWebFetch(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		URL     string `json:"url"`
		Timeout int    `json:"timeout"` // milliseconds
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.URL) == "" {
		return "", fmt.Errorf("url is required")
	}

	c := browser.NewClient()
	if args.Timeout > 0 {
		ms := args.Timeout
		if ms > 60000 {
			ms = 60000
		}
		c.HTTP.Timeout = time.Duration(ms) * time.Millisecond
	}

	result, err := c.Fetch(ctx, args.URL)
	if err != nil {
		return fmt.Sprintf("web_fetch error: %v", err), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "URL: %s\n", result.FinalURL)
	if result.Title != "" {
		fmt.Fprintf(&sb, "Title: %s\n", result.Title)
	}
	fmt.Fprintf(&sb, "Status: %d\nContent-Type: %s\nBody (%d bytes):\n\n%s",
		result.Status, result.ContentType, result.BodyBytes, result.Text)
	return sb.String(), nil
}

// handleWebSearch implements the web_search tool (T-R1P-008).
func (r *Registry) handleWebSearch(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("query is required")
	}

	max := args.MaxResults
	if max <= 0 {
		max = 5
	}
	if max > 10 {
		max = 10
	}

	searcher := websearch.DefaultFromEnv()
	if searcher == nil {
		return "web_search unavailable: no search provider configured. Set TAVILY_API_KEY or WEBSEARCH_COMMAND.", nil
	}

	results, err := searcher.Search(ctx, args.Query, max)
	if err != nil {
		return fmt.Sprintf("web_search error: %v", err), nil
	}
	if len(results) == 0 {
		return fmt.Sprintf("web_search: no results found for %q", args.Query), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Search results for %q (%d results):\n\n", args.Query, len(results))
	for i, res := range results {
		fmt.Fprintf(&sb, "%d. %s\n   URL: %s\n   %s\n\n", i+1, res.Title, res.URL, res.Excerpt)
	}
	return sb.String(), nil
}
