// browser.go: BrowserExecutor — non-interactive (Task 21 part 1) +
// interactive go-rod dispatch (work-stoke T18).
//
// Two paths:
//
//  1. plan.Extra["interactive"] is unset or false → stdlib Client
//     fetch + BrowserDeliverable. Identical to the part-1 MVP.
//
//  2. plan.Extra["interactive"] == true → dispatch to e.Rod
//     (browser.Backend). Actions come from plan.Extra["actions"]
//     as []browser.Action. When the action list has no leading
//     navigate, one is synthesized from plan.Query so operators
//     can send a bare click/extract list and have the URL travel
//     in Query. Returns BrowserInteractiveDeliverable with the
//     per-action results + the original URL/ACs.
//
//     Interactive mode requires a non-nil Rod backend — typically
//     constructed by the CLI via browser.NewRodClient(cfg) under
//     the stoke_rod build tag. Without a Rod backend attached,
//     Execute returns ErrExecutorNotWired pointing at the tag.
//
// BuildCriteria emits BROWSER-LOADED + expected_* ACs for the
// stdlib path, and BROWSER-ACTION-OK-<i>-<KIND> + expected_* ACs
// for the interactive path.

package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/browser"
	"github.com/ericmacdougall/stoke/internal/plan"
)

// BrowserExecutor satisfies Executor for TaskBrowser. Client is the
// stdlib-only fetcher (always non-nil after NewBrowserExecutor).
// Rod is the optional go-rod Backend used when
// plan.Extra["interactive"] == true. Construct one via
// browser.NewRodClient(cfg) (build tag stoke_rod) and assign it to
// Rod before routing interactive plans here.
type BrowserExecutor struct {
	Client *browser.Client
	Rod    browser.Backend
}

func NewBrowserExecutor() *BrowserExecutor {
	return &BrowserExecutor{Client: browser.NewClient()}
}

func (e *BrowserExecutor) TaskType() TaskType { return TaskBrowser }

func (e *BrowserExecutor) Execute(ctx context.Context, p Plan, _ EffortLevel) (Deliverable, error) {
	url := strings.TrimSpace(p.Query)
	if url == "" {
		return nil, errors.New("browser: empty URL in plan.Query")
	}
	if interactive, _ := p.Extra["interactive"].(bool); interactive {
		return e.executeInteractive(ctx, p, url)
	}
	if e.Client == nil {
		e.Client = browser.NewClient()
	}
	fr, err := e.Client.Fetch(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("browser fetch: %w", err)
	}
	return BrowserDeliverable{
		Result:        fr,
		ExpectedText:  str(p.Extra["expected_text"]),
		ExpectedRegex: str(p.Extra["expected_regex"]),
	}, nil
}

// executeInteractive runs plan.Extra["actions"] via the attached Rod
// backend. Without a backend, returns ErrExecutorNotWired pointing at
// the stoke_rod build tag so operators know the fix.
func (e *BrowserExecutor) executeInteractive(ctx context.Context, p Plan, url string) (Deliverable, error) {
	if e.Rod == nil {
		return nil, &ErrExecutorNotWired{
			Type:     TaskBrowser,
			FollowUp: "browser.NewRodClient(cfg) (build with -tags stoke_rod)",
		}
	}
	actions, err := coerceActions(p.Extra["actions"])
	if err != nil {
		return nil, err
	}
	// Synthesize a leading navigate from plan.Query when caller did
	// not include one — covers the common "give me a URL + a list of
	// clicks" shape without forcing callers to repeat the URL.
	if len(actions) == 0 || actions[0].Kind != browser.ActionNavigate {
		actions = append([]browser.Action{{Kind: browser.ActionNavigate, URL: url}}, actions...)
	}
	results, runErr := e.Rod.RunActions(ctx, actions)
	return BrowserInteractiveDeliverable{
		URL:           url,
		Actions:       actions,
		Results:       results,
		Err:           runErr,
		ExpectedText:  str(p.Extra["expected_text"]),
		ExpectedRegex: str(p.Extra["expected_regex"]),
	}, runErr
}

// coerceActions accepts either []browser.Action or []any (the JSON
// path) and returns a typed slice.
func coerceActions(v any) ([]browser.Action, error) {
	if v == nil {
		return nil, nil
	}
	if xs, ok := v.([]browser.Action); ok {
		return xs, nil
	}
	return nil, errors.New("browser: plan.Extra[\"actions\"] must be []browser.Action")
}

func (e *BrowserExecutor) BuildCriteria(_ Task, d Deliverable) []plan.AcceptanceCriterion {
	if id, ok := d.(BrowserInteractiveDeliverable); ok {
		return buildInteractiveCriteria(id)
	}
	bd, ok := d.(BrowserDeliverable)
	if !ok {
		return nil
	}
	result := bd.Result
	var out []plan.AcceptanceCriterion
	out = append(out, plan.AcceptanceCriterion{
		ID:          "BROWSER-LOADED",
		Description: "page loaded with 2xx status",
		VerifyFunc: func(ctx context.Context) (bool, string) {
			if result.Status >= 200 && result.Status < 300 {
				return true, fmt.Sprintf("status=%d", result.Status)
			}
			return false, fmt.Sprintf("status=%d, want 2xx", result.Status)
		},
	})
	if bd.ExpectedText != "" {
		expected := bd.ExpectedText
		out = append(out, plan.AcceptanceCriterion{
			ID:          "BROWSER-TEXT-MATCH",
			Description: fmt.Sprintf("page text contains %q", expected),
			VerifyFunc: func(ctx context.Context) (bool, string) {
				return browser.VerifyContains(result, expected)
			},
		})
	}
	if bd.ExpectedRegex != "" {
		pattern := bd.ExpectedRegex
		out = append(out, plan.AcceptanceCriterion{
			ID:          "BROWSER-REGEX-MATCH",
			Description: fmt.Sprintf("page text matches regex %q", pattern),
			VerifyFunc: func(ctx context.Context) (bool, string) {
				return browser.VerifyRegex(result, pattern)
			},
		})
	}
	return out
}

func (e *BrowserExecutor) BuildRepairFunc(_ Plan) RepairFunc {
	// Browser repair (re-navigate, wait-for-content, retry with
	// different selectors) is part 2 territory with the go-rod
	// backend. Return nil → descent falls through to T7 refactor or
	// T8 soft-pass.
	return nil
}

func (e *BrowserExecutor) BuildEnvFixFunc() EnvFixFunc {
	return func(_ context.Context, rootCause, stderr string) bool {
		low := strings.ToLower(rootCause + " " + stderr)
		for _, transient := range []string{
			"timeout", "timed out", "i/o timeout",
			"connection refused", "connection reset",
			"temporary failure", "no such host",
			" 502 ", " 503 ", " 504 ",
		} {
			if strings.Contains(low, transient) {
				return true
			}
		}
		return false
	}
}

// BrowserDeliverable wraps the FetchResult + the caller-configured
// expected_text / expected_regex hints so BuildCriteria can emit
// corresponding ACs without re-reading plan.Extra.
type BrowserDeliverable struct {
	Result        browser.FetchResult
	ExpectedText  string
	ExpectedRegex string
}

func (d BrowserDeliverable) Summary() string {
	return fmt.Sprintf("browser: %s (%d bytes, status %d)",
		d.Result.URL, d.Result.BodyBytes, d.Result.Status)
}

func (d BrowserDeliverable) Size() int { return d.Result.BodyBytes }

// str is a small helper for optional string keys out of an any map.
func str(v any) string {
	s, _ := v.(string)
	return s
}

// BrowserInteractiveDeliverable wraps the outcome of an interactive
// action run. Err is the top-level error when RunActions returned
// non-nil; per-action detail lives in Results.
type BrowserInteractiveDeliverable struct {
	URL           string
	Actions       []browser.Action
	Results       []browser.ActionResult
	Err           error
	ExpectedText  string
	ExpectedRegex string
}

// Summary returns a short human description for logs + TUI.
func (d BrowserInteractiveDeliverable) Summary() string {
	passed := 0
	for _, r := range d.Results {
		if r.OK {
			passed++
		}
	}
	status := "ok"
	if d.Err != nil {
		status = "err: " + d.Err.Error()
	}
	return fmt.Sprintf("browser[interactive]: %s (%d/%d actions %s)",
		d.URL, passed, len(d.Actions), status)
}

// Size returns the number of actions executed — used by convergence
// for empty-output guards.
func (d BrowserInteractiveDeliverable) Size() int { return len(d.Results) }

// LastScreenshot returns the PNG bytes of the last screenshot action
// in the run, or nil if none was captured.
func (d BrowserInteractiveDeliverable) LastScreenshot() []byte {
	for i := len(d.Results) - 1; i >= 0; i-- {
		if d.Results[i].Kind == browser.ActionScreenshot && len(d.Results[i].ScreenshotPNG) > 0 {
			return d.Results[i].ScreenshotPNG
		}
	}
	return nil
}

// CombinedText returns the concatenated extract-text output across
// all extract_text / extract_attribute actions, joined by "\n".
func (d BrowserInteractiveDeliverable) CombinedText() string {
	var parts []string
	for _, r := range d.Results {
		if r.Kind == browser.ActionExtractText || r.Kind == browser.ActionExtractAttribute {
			if r.Text != "" {
				parts = append(parts, r.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// buildInteractiveCriteria emits one AC per action (pass iff
// Results[i].OK) plus expected_text / expected_regex ACs that run
// against CombinedText().
func buildInteractiveCriteria(d BrowserInteractiveDeliverable) []plan.AcceptanceCriterion {
	var out []plan.AcceptanceCriterion
	for i, a := range d.Actions {
		i, a := i, a
		// Only emit ACs for actions that actually ran (Results may
		// be shorter than Actions when RunActions aborted early).
		var ok bool
		if i < len(d.Results) {
			ok = d.Results[i].OK
		}
		id := fmt.Sprintf("BROWSER-ACTION-%d-%s", i, strings.ToUpper(strings.ReplaceAll(string(a.Kind), "_", "-")))
		desc := fmt.Sprintf("action %d (%s) succeeded", i, a.Kind)
		result := ok
		reason := "ok"
		if !ok && i < len(d.Results) && d.Results[i].Err != nil {
			reason = d.Results[i].Err.Error()
		} else if !ok {
			reason = "action did not complete"
		}
		out = append(out, plan.AcceptanceCriterion{
			ID:          id,
			Description: desc,
			VerifyFunc: func(ctx context.Context) (bool, string) {
				return result, reason
			},
		})
	}
	if d.ExpectedText != "" {
		expected := d.ExpectedText
		combined := d.CombinedText()
		out = append(out, plan.AcceptanceCriterion{
			ID:          "BROWSER-TEXT-MATCH",
			Description: fmt.Sprintf("extracted text contains %q", expected),
			VerifyFunc: func(ctx context.Context) (bool, string) {
				if strings.Contains(strings.ToLower(combined), strings.ToLower(expected)) {
					return true, fmt.Sprintf("found %q", expected)
				}
				return false, fmt.Sprintf("%q not found in %d bytes of extracted text", expected, len(combined))
			},
		})
	}
	if d.ExpectedRegex != "" {
		pattern := d.ExpectedRegex
		combined := d.CombinedText()
		out = append(out, plan.AcceptanceCriterion{
			ID:          "BROWSER-REGEX-MATCH",
			Description: fmt.Sprintf("extracted text matches regex %q", pattern),
			VerifyFunc: func(ctx context.Context) (bool, string) {
				ok, reason := browser.VerifyRegex(browser.FetchResult{Text: combined}, pattern)
				return ok, reason
			},
		})
	}
	return out
}

// Compile-time interface assertions.
var (
	_ Executor    = (*BrowserExecutor)(nil)
	_ Deliverable = BrowserInteractiveDeliverable{}
)
