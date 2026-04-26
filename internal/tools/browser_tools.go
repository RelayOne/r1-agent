// browser_tools.go — browser_session / browser_navigate / browser_click /
// browser_type / browser_screenshot / browser_extract / browser_eval /
// browser_close tools.
//
// T-R1P-001: Playwright-parity browser automation tools backed by the
// internal/browser package (go-rod under the stoke_rod build tag;
// graceful stub otherwise).
//
// T-R1P-002: Manus-style long-running browser operator with screenshot
// capture. The model calls browser_session once, issues a sequence of
// actions (navigate / click / type / eval / screenshot), and closes
// the session when done. Each screenshot is written to
// .r1/browser/<session-id>/step-<n>.png and base64-encoded in the
// tool response so vision-capable models can reason about the page.
//
// Session model:
//   - browser_session  → creates a named session ID, stores config
//   - browser_navigate → loads a URL in the session's backend
//   - browser_click    → clicks a CSS selector
//   - browser_type     → types text into a CSS selector
//   - browser_screenshot → captures the viewport, saves PNG
//   - browser_extract  → extracts text or attribute from a selector
//   - browser_eval     → evaluates JavaScript (rod eval only; stub returns error)
//   - browser_close    → closes and disposes the session
//
// Graceful degradation: when the stoke_rod tag is absent the stdlib
// Client is used; navigate + fetch work; interactive actions return a
// friendly message so the model can fall back to web_fetch.

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/browser"
)

// browserSession holds a live backend + per-session step counter.
type browserSession struct {
	id      string
	backend browser.Backend
	step    int
	outDir  string // .r1/browser/<id>
	mu      sync.Mutex
}

// browserSessions is the registry of live sessions for this process.
// Keyed by session ID. Sessions are safe to look up concurrently; each
// session's actions are serialised by its own mu.
var (
	browserSessionsMu sync.Mutex
	browserSessions   = map[string]*browserSession{}
)

// getBrowserSession returns an existing session or an error.
func getBrowserSession(id string) (*browserSession, error) {
	browserSessionsMu.Lock()
	defer browserSessionsMu.Unlock()
	s, ok := browserSessions[id]
	if !ok {
		return nil, fmt.Errorf("browser session %q not found; call browser_session first", id)
	}
	return s, nil
}

// newBrowserSessionID returns a short deterministic ID for display.
func newBrowserSessionID() string {
	return fmt.Sprintf("brs-%d", time.Now().UnixMilli())
}

// --- handlers ---

// handleBrowserSession creates a new browser session.
// T-R1P-001 / T-R1P-002
func (r *Registry) handleBrowserSession(input json.RawMessage) (string, error) {
	var args struct {
		ID      string `json:"id"`       // optional; auto-generated if empty
		Headless *bool `json:"headless"` // default true
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	id := strings.TrimSpace(args.ID)
	if id == "" {
		id = newBrowserSessionID()
	}

	headless := true
	if args.Headless != nil {
		headless = *args.Headless
	}

	// Attempt to use the rod-backed client (stoke_rod tag). Fall back
	// to the stdlib client gracefully when rod is not compiled in.
	var backend browser.Backend
	rodClient, err := browser.NewRodClient(browser.RodConfig{
		HeadlessMode: headless,
		Timeout:      30 * time.Second,
	})
	if err != nil {
		// No rod — use stdlib. Interactive actions will surface a
		// friendly notice, but navigate + fetch work.
		backend = browser.NewClient()
	} else {
		backend = rodClient
	}

	// Create output directory for screenshots.
	outDir := filepath.Join(r.workDir, ".r1", "browser", id)
	if mkErr := os.MkdirAll(outDir, 0o755); mkErr != nil {
		// Non-fatal: screenshots just won't be saved to disk.
		outDir = ""
	}

	s := &browserSession{
		id:      id,
		backend: backend,
		outDir:  outDir,
	}

	browserSessionsMu.Lock()
	browserSessions[id] = s
	browserSessionsMu.Unlock()

	msg := fmt.Sprintf("browser session %q opened", id)
	if err != nil {
		msg += " (stdlib fallback — interactive actions unavailable; install chromium + rebuild with -tags stoke_rod)"
	}
	return msg, nil
}

// handleBrowserNavigate navigates the session to a URL.
func (r *Registry) handleBrowserNavigate(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Session string `json:"session"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.URL) == "" {
		return "", fmt.Errorf("url is required")
	}
	s, err := getBrowserSession(args.Session)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	results, err := s.backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionNavigate, URL: args.URL},
	})
	if err != nil {
		return fmt.Sprintf("navigate error: %v", err), nil
	}
	if len(results) == 0 || !results[0].OK {
		return fmt.Sprintf("navigate failed: %v", results[0].Err), nil
	}
	r0 := results[0]
	return fmt.Sprintf("navigated to %s (final: %s)", args.URL, r0.URL), nil
}

// handleBrowserClick clicks a CSS selector in the session.
func (r *Registry) handleBrowserClick(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Session  string `json:"session"`
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Selector) == "" {
		return "", fmt.Errorf("selector is required")
	}
	s, err := getBrowserSession(args.Session)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	results, runErr := s.backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionClick, Selector: args.Selector},
	})
	if runErr != nil {
		return fmt.Sprintf("click error: %v", runErr), nil
	}
	if len(results) == 0 || !results[0].OK {
		return fmt.Sprintf("click failed: %v", results[0].Err), nil
	}
	return fmt.Sprintf("clicked %q (%dms)", args.Selector, results[0].DurationMs), nil
}

// handleBrowserType types text into a CSS selector.
func (r *Registry) handleBrowserType(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Session  string `json:"session"`
		Selector string `json:"selector"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Selector) == "" {
		return "", fmt.Errorf("selector is required")
	}
	s, err := getBrowserSession(args.Session)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	results, runErr := s.backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionType, Selector: args.Selector, Text: args.Text},
	})
	if runErr != nil {
		return fmt.Sprintf("type error: %v", runErr), nil
	}
	if len(results) == 0 || !results[0].OK {
		return fmt.Sprintf("type failed: %v", results[0].Err), nil
	}
	return fmt.Sprintf("typed %d chars into %q (%dms)", len(args.Text), args.Selector, results[0].DurationMs), nil
}

// handleBrowserScreenshot captures the viewport.
// T-R1P-002: Manus-style operator — returns PNG as base64 data URI so
// vision-capable models can inspect the page.
func (r *Registry) handleBrowserScreenshot(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	s, err := getBrowserSession(args.Session)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.step++
	stepN := s.step
	outPath := ""
	if s.outDir != "" {
		outPath = filepath.Join(s.outDir, fmt.Sprintf("step-%04d.png", stepN))
	}

	results, runErr := s.backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionScreenshot, OutputPath: outPath},
	})
	if runErr != nil {
		return fmt.Sprintf("screenshot error: %v", runErr), nil
	}
	if len(results) == 0 || !results[0].OK {
		return fmt.Sprintf("screenshot failed: %v", results[0].Err), nil
	}

	png := results[0].ScreenshotPNG
	b64 := base64.StdEncoding.EncodeToString(png)
	dataURI := "data:image/png;base64," + b64

	msg := fmt.Sprintf("screenshot captured: step %d, %d bytes", stepN, len(png))
	if outPath != "" {
		msg += fmt.Sprintf(", saved to %s", outPath)
	}
	msg += "\n" + dataURI
	return msg, nil
}

// handleBrowserExtract extracts text or attribute from a selector.
func (r *Registry) handleBrowserExtract(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Session   string `json:"session"`
		Selector  string `json:"selector"`
		Attribute string `json:"attribute"` // empty = extract text
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Selector) == "" {
		return "", fmt.Errorf("selector is required")
	}
	s, err := getBrowserSession(args.Session)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var action browser.Action
	if args.Attribute != "" {
		action = browser.Action{Kind: browser.ActionExtractAttribute, Selector: args.Selector, Attribute: args.Attribute}
	} else {
		action = browser.Action{Kind: browser.ActionExtractText, Selector: args.Selector}
	}

	results, runErr := s.backend.RunActions(ctx, []browser.Action{action})
	if runErr != nil {
		return fmt.Sprintf("extract error: %v", runErr), nil
	}
	if len(results) == 0 || !results[0].OK {
		return fmt.Sprintf("extract failed: %v", results[0].Err), nil
	}
	return results[0].Text, nil
}

// handleBrowserEval evaluates JavaScript in the session page.
// Only meaningful with the stoke_rod backend; the stdlib backend
// returns a graceful not-supported message so the model can adapt.
func (r *Registry) handleBrowserEval(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Session string `json:"session"`
		Script  string `json:"script"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Script) == "" {
		return "", fmt.Errorf("script is required")
	}
	s, err := getBrowserSession(args.Session)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// JavaScript eval is exposed via the stoke_rod backend only.
	// The stdlib fallback and no-rod stub both return a notice.
	type jsEvaluator interface {
		EvalScript(ctx context.Context, script string) (string, error)
	}
	if ev, ok := s.backend.(jsEvaluator); ok {
		res, evalErr := ev.EvalScript(ctx, args.Script)
		if evalErr != nil {
			return fmt.Sprintf("eval error: %v", evalErr), nil
		}
		return res, nil
	}
	return "browser_eval requires the stoke_rod build tag (rebuild with -tags stoke_rod ./cmd/stoke); " +
		"JavaScript evaluation not available in stdlib-only mode", nil
}

// handleBrowserWaitFor blocks until a CSS selector appears (or timeout).
// T-R1P-001: completes the supervisor-spec primitive set (wait_for).
func (r *Registry) handleBrowserWaitFor(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Session   string `json:"session"`
		Selector  string `json:"selector"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Selector) == "" {
		return "", fmt.Errorf("selector is required")
	}
	s, err := getBrowserSession(args.Session)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	timeout := time.Duration(args.TimeoutMs) * time.Millisecond
	results, runErr := s.backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionWaitForSelector, Selector: args.Selector, Timeout: timeout},
	})
	if runErr != nil {
		return fmt.Sprintf("wait_for error: %v", runErr), nil
	}
	if len(results) == 0 || !results[0].OK {
		return fmt.Sprintf("wait_for failed: %v", results[0].Err), nil
	}
	return fmt.Sprintf("selector %q became present (%dms)", args.Selector, results[0].DurationMs), nil
}

// handleBrowserGetHTML returns the page's outer HTML.
// T-R1P-001: completes the supervisor-spec primitive set (get_html).
//
// Implementation: routes through eval(document.documentElement.outerHTML)
// when the backend supports JS evaluation (rod); falls back to extract on
// the html element otherwise. Output is byte-capped to keep the agent
// loop's context window healthy.
func (r *Registry) handleBrowserGetHTML(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Session string `json:"session"`
		MaxKB   int    `json:"max_kb"` // optional cap (default 256KB)
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	s, err := getBrowserSession(args.Session)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	maxBytes := args.MaxKB * 1024
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}

	// Prefer JS eval when the backend supports it (rod path).
	type jsEvaluator interface {
		EvalScript(ctx context.Context, script string) (string, error)
	}
	if ev, ok := s.backend.(jsEvaluator); ok {
		html, evalErr := ev.EvalScript(ctx, "document.documentElement.outerHTML")
		if evalErr == nil {
			if len(html) > maxBytes {
				html = html[:maxBytes] + "\n[truncated]"
			}
			return html, nil
		}
		// fall through to extract on stdlib
	}

	// stdlib fallback: extract text of <html> (no real HTML available).
	results, runErr := s.backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionExtractText, Selector: "html"},
	})
	if runErr != nil {
		return fmt.Sprintf("get_html error: %v", runErr), nil
	}
	if len(results) == 0 || !results[0].OK {
		return fmt.Sprintf("get_html failed: %v", results[0].Err), nil
	}
	out := results[0].Text
	if len(out) > maxBytes {
		out = out[:maxBytes] + "\n[truncated]"
	}
	return out, nil
}

// handleBrowserClose closes and disposes a session.
func (r *Registry) handleBrowserClose(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	s, err := getBrowserSession(args.Session)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	closeErr := s.backend.Close()

	browserSessionsMu.Lock()
	delete(browserSessions, args.Session)
	browserSessionsMu.Unlock()

	if closeErr != nil {
		return fmt.Sprintf("browser session %q closed (with error: %v)", args.Session, closeErr), nil
	}
	return fmt.Sprintf("browser session %q closed (%d screenshots captured)", args.Session, s.step), nil
}
