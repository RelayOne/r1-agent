//go:build stoke_rod

// rod_real.go: real go-rod-backed RodClient.
//
// Compiled only when the `stoke_rod` build tag is set. Without the
// tag, rod_stub.go provides a no-op RodClient that errors out with
// ErrChromeLaunchFailed — this keeps the default single-binary
// distribution free of a Chromium dependency.
//
// Design:
//   - One headless browser per RodClient, lazily launched on first
//     call (defers the Chromium download / spawn cost until needed).
//   - Each RunActions / Fetch call opens a fresh *rod.Page, runs its
//     work, then closes the page. Pages are cheap to create once the
//     browser is hot; serializing at the client level keeps the
//     deterministic action ordering the executor expects.
//   - Screenshots land in .stoke/browser/<task-id>/<step>.png when
//     Action.OutputPath is set; bytes are also returned via
//     ActionResult.ScreenshotPNG regardless.
//   - launcher.New().Bin(cfg.ChromePath) is used when ChromePath (or
//     CHROME_PATH env) is set; otherwise launcher auto-downloads.

package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// init replaces the stub factory with the real rod-backed one. Runs
// at package load time whenever the stoke_rod build tag is set.
func init() {
	rodClientFactory = realRodFactory
}

// RodClient is the real go-rod Backend. Lazily owns one headless
// Chromium; RunActions / Fetch mint a fresh page per call.
type RodClient struct {
	cfg RodConfig

	mu       sync.Mutex
	launcher *launcher.Launcher
	browser  *rod.Browser
	closed   bool
}

// realRodFactory constructs the real RodClient. Never launches
// Chromium here — that's deferred to the first Fetch/RunActions so
// construction stays cheap and the binary does not try to download
// Chromium at CLI init.
func realRodFactory(cfg RodConfig) (*RodClient, error) {
	if cfg.PoolSize <= 0 {
		cfg.PoolSize = 1
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.ChromePath == "" {
		cfg.ChromePath = os.Getenv("CHROME_PATH")
	}
	return &RodClient{cfg: cfg}, nil
}

// ensureBrowser starts Chromium the first time it's needed.
func (r *RodClient) ensureBrowser() (*rod.Browser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, &ErrChromeLaunchFailed{Cause: errors.New("RodClient closed")}
	}
	if r.browser != nil {
		return r.browser, nil
	}
	l := launcher.New().Headless(r.cfg.HeadlessMode || !osEnvSet("STOKE_ROD_HEADED"))
	if r.cfg.ChromePath != "" {
		l = l.Bin(r.cfg.ChromePath)
	}
	url, err := l.Launch()
	if err != nil {
		return nil, &ErrChromeLaunchFailed{Cause: err}
	}
	br := rod.New().ControlURL(url)
	if err := br.Connect(); err != nil {
		l.Cleanup()
		return nil, &ErrChromeLaunchFailed{Cause: err}
	}
	r.launcher = l
	r.browser = br
	return br, nil
}

// Fetch performs a navigate + extract against a fresh page. Matches
// Backend.Fetch semantics: non-2xx is not an error, it is returned in
// FetchResult.Status.
func (r *RodClient) Fetch(ctx context.Context, url string) (FetchResult, error) {
	if strings.TrimSpace(url) == "" {
		return FetchResult{}, errors.New("browser.RodClient.Fetch: empty url")
	}
	br, err := r.ensureBrowser()
	if err != nil {
		return FetchResult{}, err
	}
	page, err := br.Context(ctx).Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return FetchResult{}, &ErrNavigationFailed{URL: url, Cause: err}
	}
	defer func() { _ = page.Close() }()

	page = page.Timeout(r.cfg.Timeout)

	// Capture top-level response status via network event.
	var status int
	var finalURL string
	go page.EachEvent(func(e *proto.NetworkResponseReceived) bool {
		if e.Type == proto.NetworkResourceTypeDocument && status == 0 {
			status = e.Response.Status
			finalURL = e.Response.URL
			return true
		}
		return false
	})()

	if err := page.Navigate(url); err != nil {
		return FetchResult{}, &ErrNavigationFailed{URL: url, Cause: err}
	}
	if err := page.WaitLoad(); err != nil {
		return FetchResult{}, &ErrNavigationFailed{URL: url, Cause: err}
	}

	info, err := page.Info()
	if err == nil && finalURL == "" {
		finalURL = info.URL
	}
	title := ""
	if info != nil {
		title = info.Title
	}

	// Extract textContent from the body; rod strips tags for us.
	text := ""
	body, bodyErr := page.Element("body")
	if bodyErr == nil && body != nil {
		if t, terr := body.Text(); terr == nil {
			text = t
		}
	}
	// Fallback: grab the full HTML if body element missing (e.g.,
	// early-SPA). Run ExtractText to strip tags.
	if text == "" {
		if html, herr := page.HTML(); herr == nil {
			text = ExtractText(html)
		}
	}

	return FetchResult{
		URL:       url,
		FinalURL:  finalURL,
		Status:    fallbackStatus(status, url),
		Text:      text,
		Title:     title,
		BodyBytes: len(text),
	}, nil
}

// fallbackStatus returns a synthetic 200 when the network layer did
// not report a status (common for file:// or data: URLs). Chromium
// emits NetworkResponseReceived for http(s) only.
func fallbackStatus(s int, url string) int {
	if s != 0 {
		return s
	}
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		// http(s) with no response event ⇒ treat as 0; caller decides.
		return 0
	}
	return 200
}

// RunActions executes the interactive action list in order on a
// single fresh page. Screenshots are materialised to disk when
// OutputPath is set; bytes are always returned on the result.
func (r *RodClient) RunActions(ctx context.Context, actions []Action) ([]ActionResult, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	br, err := r.ensureBrowser()
	if err != nil {
		return nil, err
	}
	page, err := br.Context(ctx).Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, &ErrChromeLaunchFailed{Cause: err}
	}
	defer func() { _ = page.Close() }()

	out := make([]ActionResult, 0, len(actions))
	for _, a := range actions {
		if err := a.Validate(); err != nil {
			return out, err
		}
		start := time.Now()
		res := r.dispatch(page, a)
		res.DurationMs = time.Since(start).Milliseconds()
		out = append(out, res)
		if !res.OK {
			return out, res.Err
		}
	}
	return out, nil
}

// dispatch routes one action to the appropriate rod call. All
// per-action timeouts are honored via Page.Timeout(...).
func (r *RodClient) dispatch(page *rod.Page, a Action) ActionResult {
	timeout := a.DefaultTimeout()
	p := page.Timeout(timeout)
	res := ActionResult{Kind: a.Kind}

	switch a.Kind {
	case ActionNavigate:
		if err := p.Navigate(a.URL); err != nil {
			res.Err = &ErrNavigationFailed{URL: a.URL, Cause: err}
			return res
		}
		if err := p.WaitLoad(); err != nil {
			res.Err = &ErrNavigationFailed{URL: a.URL, Cause: err}
			return res
		}
		if info, err := p.Info(); err == nil {
			res.URL = info.URL
		} else {
			res.URL = a.URL
		}
		res.OK = true
		return res

	case ActionClick:
		el, err := p.Element(a.Selector)
		if err != nil {
			res.Err = mapRodErr(a.Kind, a.Selector, err)
			return res
		}
		if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
			res.Err = mapRodErr(a.Kind, a.Selector, err)
			return res
		}
		res.OK = true
		return res

	case ActionType:
		el, err := p.Element(a.Selector)
		if err != nil {
			res.Err = mapRodErr(a.Kind, a.Selector, err)
			return res
		}
		if err := el.Input(a.Text); err != nil {
			res.Err = mapRodErr(a.Kind, a.Selector, err)
			return res
		}
		res.Text = a.Text
		res.OK = true
		return res

	case ActionWaitForSelector:
		if _, err := p.Element(a.Selector); err != nil {
			res.Err = mapRodErr(a.Kind, a.Selector, err)
			return res
		}
		res.OK = true
		return res

	case ActionWaitForNetworkIdle:
		wait := p.WaitRequestIdle(500*time.Millisecond, nil, nil, nil)
		wait()
		res.OK = true
		return res

	case ActionScreenshot:
		buf, err := p.Screenshot(false, &proto.PageCaptureScreenshot{
			Format: proto.PageCaptureScreenshotFormatPng,
		})
		if err != nil {
			res.Err = &ErrActionTimeout{Kind: string(a.Kind), Cause: err}
			return res
		}
		res.ScreenshotPNG = buf
		if a.OutputPath != "" {
			if werr := writeScreenshot(a.OutputPath, buf); werr != nil {
				res.Err = fmt.Errorf("browser: screenshot write: %w", werr)
				return res
			}
		}
		res.OK = true
		return res

	case ActionExtractText:
		el, err := p.Element(a.Selector)
		if err != nil {
			res.Err = mapRodErr(a.Kind, a.Selector, err)
			return res
		}
		txt, err := el.Text()
		if err != nil {
			res.Err = mapRodErr(a.Kind, a.Selector, err)
			return res
		}
		res.Text = txt
		res.OK = true
		return res

	case ActionExtractAttribute:
		el, err := p.Element(a.Selector)
		if err != nil {
			res.Err = mapRodErr(a.Kind, a.Selector, err)
			return res
		}
		val, err := el.Attribute(a.Attribute)
		if err != nil {
			res.Err = mapRodErr(a.Kind, a.Selector, err)
			return res
		}
		if val != nil {
			res.Attribute = *val
			res.Text = *val
		}
		res.OK = true
		return res
	}

	res.Err = fmt.Errorf("browser: unsupported action kind %q", a.Kind)
	return res
}

// mapRodErr classifies a rod error into the Stoke error taxonomy.
// rod surfaces timeouts as context.DeadlineExceeded-wrapped errors;
// everything else is reported as ElementNotFound.
func mapRodErr(kind ActionKind, selector string, err error) error {
	if err == nil {
		return nil
	}
	// rod timeouts wrap context.DeadlineExceeded.
	if errors.Is(err, context.DeadlineExceeded) {
		return &ErrActionTimeout{Kind: string(kind), Selector: selector, Cause: err}
	}
	return &ErrElementNotFound{Selector: selector, Cause: err}
}

// Close shuts down the headless browser and cleans up the launcher's
// user-data dir.
func (r *RodClient) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	var firstErr error
	if r.browser != nil {
		if err := r.browser.Close(); err != nil {
			firstErr = err
		}
		r.browser = nil
	}
	if r.launcher != nil {
		r.launcher.Cleanup()
		r.launcher = nil
	}
	return firstErr
}

// Compile-time assertion: real RodClient also satisfies Backend.
var _ Backend = (*RodClient)(nil)

// writeScreenshot writes PNG bytes under .stoke/browser/... creating
// intermediate directories as needed.
func writeScreenshot(path string, png []byte) error {
	dir := dirOf(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, png, 0o644)
}

// dirOf is a tiny filepath.Dir shim that avoids pulling filepath into
// the top-of-file imports for a single call site.
func dirOf(path string) string {
	idx := strings.LastIndexAny(path, "/\\")
	if idx < 0 {
		return ""
	}
	return path[:idx]
}

// osEnvSet reports whether the named env var is set to any non-empty
// value.
func osEnvSet(name string) bool {
	return os.Getenv(name) != ""
}

// tinyHTTPGet is a diagnostic helper used only by internal smoke
// tests; lives here so it can share the http.Client timeout shape
// with the stdlib Fetch path. Intentionally unexported.
func tinyHTTPGet(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
