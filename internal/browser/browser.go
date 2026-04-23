// Package browser is Stoke's MVP web-interaction layer. It covers
// the read-only use cases: fetch a URL, extract its text body,
// verify expected content. Interactive actions (click, type, wait,
// screenshot) ship in a follow-up that lands a go-rod / Playwright
// dependency; this package deliberately stays stdlib-only so Stoke
// + one API key remains a single binary with no browser requirement.
package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// FetchResult captures everything the MVP cares about from a page.
// The full-interaction follow-up adds ConsoleErrors, DOMSnapshot,
// ScreenshotPath.
type FetchResult struct {
	URL         string
	FinalURL    string // after redirects
	Status      int
	ContentType string
	BodyBytes   int
	Text        string // extracted plain-text body (HTML stripped)
	Title       string
}

// Client is the minimal HTTP-only browser. Future go-rod backend
// satisfies the same public surface so consumers don't change.
type Client struct {
	HTTP    *http.Client
	MaxBody int64 // 0 → 1MB default; hard cap on Read to prevent adversarial inflation
	UA      string
}

// NewClient returns a Client with sane defaults: 30s request
// deadline, 1MB body cap, realistic User-Agent so sites that 403
// bots don't reject us unnecessarily.
func NewClient() *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		MaxBody: 1 << 20,
		UA:      "StokeBrowser/0.1 (+https://stoke.dev)",
	}
}

// Fetch performs GET url and returns a populated FetchResult. Respects
// ctx cancellation. A 4xx/5xx is NOT an error — the status code is
// returned in the result so callers can classify.
func (c *Client) Fetch(ctx context.Context, url string) (FetchResult, error) {
	if strings.TrimSpace(url) == "" {
		return FetchResult{}, errors.New("browser.Fetch: empty url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("build request: %w", err)
	}
	if c.UA != "" {
		req.Header.Set("User-Agent", c.UA)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return FetchResult{}, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	cap := c.MaxBody
	if cap <= 0 {
		cap = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cap+1))
	if err != nil {
		return FetchResult{}, fmt.Errorf("read body: %w", err)
	}
	truncated := false
	if int64(len(body)) > cap {
		body = body[:cap]
		truncated = true
	}

	text := ExtractText(string(body))
	if truncated {
		text += "\n\n[truncated by browser.Client at " + fmt.Sprint(cap) + " bytes]"
	}
	return FetchResult{
		URL:         url,
		FinalURL:    resp.Request.URL.String(),
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		BodyBytes:   len(body),
		Text:        text,
		Title:       ExtractTitle(string(body)),
	}, nil
}

// VerifyContains returns (true, reason) when the fetched page's
// extracted Text contains expected (case-insensitive substring).
// reason carries a short diagnostic either way.
func VerifyContains(r FetchResult, expected string) (bool, string) {
	if expected == "" {
		return true, "no expected text configured — passing through"
	}
	if strings.Contains(strings.ToLower(r.Text), strings.ToLower(expected)) {
		return true, fmt.Sprintf("found %q in page text", expected)
	}
	return false, fmt.Sprintf("%q not found in page text (%d bytes extracted)", expected, len(r.Text))
}

// VerifyRegex returns (true, reason) when the extracted Text matches
// the given RE2 pattern. Returns false + compile-error reason on
// an invalid pattern so callers can surface it to the operator.
func VerifyRegex(r FetchResult, pattern string) (bool, string) {
	if pattern == "" {
		return true, "no regex configured — passing through"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Sprintf("regex compile error: %v", err)
	}
	if re.MatchString(r.Text) {
		return true, fmt.Sprintf("page matched pattern %q", pattern)
	}
	return false, fmt.Sprintf("page did not match pattern %q", pattern)
}

// tagStripper removes any HTML tag (angle-bracket delimited). It
// does NOT decode entities because the MVP use cases (keyword
// overlap for verification) tolerate raw entities; the full browser
// ships entity decoding.
var tagStripper = regexp.MustCompile(`<[^>]+>`)

// scriptStyle matches <script>...</script> or <style>...</style>
// blocks (case-insensitive, dotall). We strip these ahead of tag
// removal so inlined JS / CSS doesn't leak into the "page text."
var scriptStyle = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)

// whitespace collapses runs of whitespace into a single space.
var whitespace = regexp.MustCompile(`\s+`)

// ExtractText strips HTML tags and returns readable body text.
// Algorithm: drop <script>/<style> blocks entirely, strip all
// remaining tags, collapse whitespace.
func ExtractText(html string) string {
	clean := scriptStyle.ReplaceAllString(html, " ")
	clean = tagStripper.ReplaceAllString(clean, " ")
	clean = whitespace.ReplaceAllString(clean, " ")
	return strings.TrimSpace(clean)
}

// titlePattern matches the first <title>...</title> content.
var titlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// ogTitlePattern matches og:title meta when <title> is absent.
var ogTitlePattern = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["']`)

// ExtractTitle pulls the first <title> content or an og:title meta.
// Empty string when neither is present.
func ExtractTitle(html string) string {
	if m := titlePattern.FindStringSubmatch(html); len(m) > 1 {
		return whitespace.ReplaceAllString(strings.TrimSpace(m[1]), " ")
	}
	if m := ogTitlePattern.FindStringSubmatch(html); len(m) > 1 {
		return whitespace.ReplaceAllString(strings.TrimSpace(m[1]), " ")
	}
	return ""
}
