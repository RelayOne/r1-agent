// Package browseragent implements an autonomous, screenshot-driven
// web-task agent on top of internal/browser. The model receives the
// natural-language objective, then a perceive-plan-act loop runs:
// take a screenshot, ask the LLM to propose the next action, execute
// it against the browser, check whether the goal is met, repeat.
//
// Inspiration: Manus AI's general-purpose autonomous web operator.
// We do not depend on or call out to Manus; we implement the same
// loop shape locally so an R1 mission can drive a real browser to
// complete a multi-step web task end to end.
//
// Design principles:
//   - LLM access is behind an interface (Planner) so tests can supply
//     a fake; production code wires Anthropic via auth-core or
//     RelayGate at the call site.
//   - The browser is also behind an interface (Driver) so the loop is
//     testable without a real Chromium. The default Driver wraps
//     internal/browser.Backend.
//   - Loops terminate. A hard step cap (default 20) and a context
//     deadline guard against runaway agents. Each step records a
//     screenshot path so a human can audit the trajectory.
//   - Errors at any step are reported back to the planner as
//     observations rather than killing the loop, so the agent can
//     recover (e.g. selector mismatch → fall back to coordinate).
//
// The package deliberately ships zero LLM-vendor dependencies. The
// caller wires whichever provider is appropriate; this keeps the
// browser-operator skill usable from CloudSwarm or local CLI without
// a fixed provider lock-in.
package browseragent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/browser"
)

// MaxStepsDefault is the hard cap on planner iterations per Run.
// Set high enough for realistic multi-step web tasks (sign-up flows,
// search → click → extract) but low enough that a confused agent
// surfaces failure quickly. Caller can override via Config.MaxSteps.
const MaxStepsDefault = 20

// Action is a planner-emitted instruction. Kind matches the browser
// primitives plus a "done" sentinel that ends the loop with a final
// answer. Field requirements per kind are validated by Validate().
type Action struct {
	Kind     ActionKind `json:"kind"`
	URL      string     `json:"url,omitempty"`
	Selector string     `json:"selector,omitempty"`
	Text     string     `json:"text,omitempty"`
	Reason   string     `json:"reason,omitempty"` // planner's rationale (audit log)
	Answer   string     `json:"answer,omitempty"` // populated when Kind==Done
}

// ActionKind enumerates planner-emittable primitives. Subset of
// browser.ActionKind plus the loop sentinels.
type ActionKind string

const (
	KindNavigate    ActionKind = "navigate"
	KindClick       ActionKind = "click"
	KindEnterText   ActionKind = "type"
	KindWaitFor     ActionKind = "wait_for"
	KindExtractText ActionKind = "extract"
	KindScreenshot  ActionKind = "screenshot"
	// KindDone marks the agent satisfied — the loop returns Answer.
	KindDone ActionKind = "done"
	// KindGiveUp marks the agent unable to proceed — loop returns
	// an error containing Reason.
	KindGiveUp ActionKind = "give_up"
)

// Validate rejects malformed planner actions before they hit the
// driver. Uses the same per-kind required-field rules as
// browser.Action so the planner can't smuggle invalid intents.
func (a Action) Validate() error {
	switch a.Kind {
	case KindNavigate:
		if strings.TrimSpace(a.URL) == "" {
			return errors.New("browseragent: navigate requires url")
		}
	case KindClick, KindWaitFor, KindExtractText:
		if strings.TrimSpace(a.Selector) == "" {
			return fmt.Errorf("browseragent: %s requires selector", a.Kind)
		}
	case KindEnterText:
		if strings.TrimSpace(a.Selector) == "" {
			return errors.New("browseragent: type requires selector")
		}
		// empty text allowed (clear field)
	case KindScreenshot, KindDone, KindGiveUp:
		// no required fields
	default:
		return fmt.Errorf("browseragent: unknown action kind %q", a.Kind)
	}
	return nil
}

// Observation is what the planner sees after each step: the action
// just executed, whether it succeeded, any extracted text, and the
// path of the latest screenshot.
type Observation struct {
	Step           int
	LastAction     Action
	OK             bool
	ErrText        string // populated when OK==false
	Text           string // populated by extract / get_html / wait observations
	ScreenshotPath string // disk path or empty when not captured
	URL            string // current URL after navigation
}

// Planner proposes the next Action given the objective + observation
// history. Implementations call out to an LLM (Anthropic / RelayGate
// / etc.). Returning KindDone or KindGiveUp ends the loop.
//
// The planner MUST be deterministic-enough to terminate: callers rely
// on the step cap + give-up sentinel as the runaway guard.
type Planner interface {
	Next(ctx context.Context, objective string, history []Observation) (Action, error)
}

// Driver is the side-effecting browser interface. The default
// implementation wraps internal/browser.Backend; tests inject a stub.
type Driver interface {
	Navigate(ctx context.Context, url string) (Observation, error)
	Click(ctx context.Context, selector string) (Observation, error)
	Type(ctx context.Context, selector, text string) (Observation, error)
	WaitFor(ctx context.Context, selector string, timeout time.Duration) (Observation, error)
	Extract(ctx context.Context, selector string) (Observation, error)
	Screenshot(ctx context.Context, savePath string) (Observation, error)
}

// Config tunes the agent. Zero values are sensible defaults.
type Config struct {
	// MaxSteps caps planner iterations. 0 → MaxStepsDefault.
	MaxSteps int
	// StepDeadline bounds a single step's execution time. 0 → 60s.
	StepDeadline time.Duration
	// ScreenshotDir is where step screenshots are saved (when set).
	// Empty disables disk persistence; bytes still flow back to the
	// planner via Observation.ScreenshotPath when the driver supports
	// it.
	ScreenshotDir string
	// PerceiveEveryStep, when true, takes a screenshot before each
	// planner call (the canonical Manus loop). When false, the agent
	// only takes screenshots when explicitly requested by an action.
	PerceiveEveryStep bool
	// Logger receives one line per loop iteration. nil → no logging.
	Logger func(format string, args ...any)
}

// Result is the loop outcome.
type Result struct {
	Success      bool
	Answer       string        // planner's final answer when Success==true
	Steps        int           // total iterations the loop ran
	Observations []Observation // full trajectory for audit
	GaveUpReason string        // populated when planner returned give_up
}

// Run executes the perceive-plan-act loop until the planner emits
// done/give_up, the step cap is reached, or ctx is cancelled.
//
// Termination invariants:
//   - Always returns within MaxSteps planner calls.
//   - ctx cancellation aborts at the next step boundary.
//   - A planner-side error or driver-side fatal aborts the loop and
//     surfaces via the returned error; partial Result is still
//     populated for audit.
func Run(ctx context.Context, objective string, planner Planner, drv Driver, cfg Config) (Result, error) {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = MaxStepsDefault
	}
	if cfg.StepDeadline <= 0 {
		cfg.StepDeadline = 60 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = func(string, ...any) {}
	}
	if planner == nil {
		return Result{}, errors.New("browseragent: nil Planner")
	}
	if drv == nil {
		return Result{}, errors.New("browseragent: nil Driver")
	}
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return Result{}, errors.New("browseragent: empty objective")
	}

	res := Result{}
	history := make([]Observation, 0, cfg.MaxSteps)

	for step := 1; step <= cfg.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			res.Steps = step - 1
			res.Observations = history
			return res, fmt.Errorf("context cancelled at step %d: %w", step, err)
		}

		// Optional: perceive (screenshot) before each plan call.
		if cfg.PerceiveEveryStep {
			perceiveCtx, cancel := context.WithTimeout(ctx, cfg.StepDeadline)
			obs, err := drv.Screenshot(perceiveCtx, screenshotPath(cfg, step, "perceive"))
			cancel()
			if err == nil {
				obs.Step = step
				history = append(history, obs)
			} else {
				cfg.Logger("step %d perceive failed: %v", step, err)
			}
		}

		action, err := planner.Next(ctx, objective, history)
		if err != nil {
			res.Steps = step - 1
			res.Observations = history
			return res, fmt.Errorf("planner failed at step %d: %w", step, err)
		}
		if vErr := action.Validate(); vErr != nil {
			res.Steps = step - 1
			res.Observations = history
			return res, fmt.Errorf("planner emitted invalid action at step %d: %w", step, vErr)
		}

		cfg.Logger("step %d: %s (%s)", step, action.Kind, action.Reason)

		// Terminal sentinels.
		if action.Kind == KindDone {
			res.Success = true
			res.Answer = action.Answer
			res.Steps = step
			res.Observations = history
			return res, nil
		}
		if action.Kind == KindGiveUp {
			res.Success = false
			res.GaveUpReason = action.Reason
			res.Steps = step
			res.Observations = history
			return res, nil
		}

		// Execute via the driver under a per-step deadline.
		stepCtx, cancel := context.WithTimeout(ctx, cfg.StepDeadline)
		obs := executeAction(stepCtx, drv, action, screenshotPath(cfg, step, string(action.Kind)))
		cancel()
		obs.Step = step
		obs.LastAction = action
		history = append(history, obs)
	}

	res.Steps = cfg.MaxSteps
	res.Observations = history
	res.GaveUpReason = fmt.Sprintf("step cap (%d) reached without done/give_up", cfg.MaxSteps)
	return res, nil
}

// executeAction dispatches one action to the driver and returns the
// observation. Driver errors become OK=false observations rather than
// fatal so the planner can recover.
func executeAction(ctx context.Context, drv Driver, a Action, savePath string) Observation {
	var (
		obs Observation
		err error
	)
	switch a.Kind {
	case KindNavigate:
		obs, err = drv.Navigate(ctx, a.URL)
	case KindClick:
		obs, err = drv.Click(ctx, a.Selector)
	case KindEnterText:
		obs, err = drv.Type(ctx, a.Selector, a.Text)
	case KindWaitFor:
		obs, err = drv.WaitFor(ctx, a.Selector, 0) // 0 → driver default
	case KindExtractText:
		obs, err = drv.Extract(ctx, a.Selector)
	case KindScreenshot:
		obs, err = drv.Screenshot(ctx, savePath)
	default:
		err = fmt.Errorf("browseragent: unsupported action %q", a.Kind)
	}
	if err != nil {
		obs.OK = false
		obs.ErrText = err.Error()
	}
	return obs
}

// screenshotPath returns "<dir>/step-NNNN-<kind>.png" or "" if no dir.
func screenshotPath(cfg Config, step int, kind string) string {
	if cfg.ScreenshotDir == "" {
		return ""
	}
	return fmt.Sprintf("%s/step-%04d-%s.png", strings.TrimRight(cfg.ScreenshotDir, "/"), step, kind)
}

// --- Default Driver: wraps internal/browser.Backend ---

// BackendDriver is the production Driver. It serialises browser
// actions through one Backend (rod or stdlib). The same backend can
// be shared across many planner runs — Run does not Close it.
type BackendDriver struct {
	Backend browser.Backend
	// WaitTimeout overrides the per-action wait deadline. 0 → 10s.
	WaitTimeout time.Duration
}

// Navigate runs a single navigate action and returns the resulting
// Observation. URL is the post-redirect final URL when available.
func (d *BackendDriver) Navigate(ctx context.Context, url string) (Observation, error) {
	results, err := d.Backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionNavigate, URL: url},
	})
	return obsFromRun(results, err)
}

// Click clicks the first element matching selector.
func (d *BackendDriver) Click(ctx context.Context, selector string) (Observation, error) {
	results, err := d.Backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionClick, Selector: selector},
	})
	return obsFromRun(results, err)
}

// Type enters text into the first element matching selector.
func (d *BackendDriver) Type(ctx context.Context, selector, text string) (Observation, error) {
	results, err := d.Backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionType, Selector: selector, Text: text},
	})
	return obsFromRun(results, err)
}

// WaitFor blocks until selector appears, bounded by timeout.
func (d *BackendDriver) WaitFor(ctx context.Context, selector string, timeout time.Duration) (Observation, error) {
	if timeout <= 0 {
		timeout = d.WaitTimeout
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	results, err := d.Backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionWaitForSelector, Selector: selector, Timeout: timeout},
	})
	return obsFromRun(results, err)
}

// Extract returns the text of the first element matching selector.
func (d *BackendDriver) Extract(ctx context.Context, selector string) (Observation, error) {
	results, err := d.Backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionExtractText, Selector: selector},
	})
	return obsFromRun(results, err)
}

// Screenshot captures the viewport to savePath (when non-empty).
func (d *BackendDriver) Screenshot(ctx context.Context, savePath string) (Observation, error) {
	results, err := d.Backend.RunActions(ctx, []browser.Action{
		{Kind: browser.ActionScreenshot, OutputPath: savePath},
	})
	obs, oerr := obsFromRun(results, err)
	if savePath != "" {
		obs.ScreenshotPath = savePath
	}
	return obs, oerr
}

// obsFromRun converts a single-action RunActions outcome into an
// Observation. err is the runtime error from RunActions itself; per-
// action failures arrive in results[0].Err.
func obsFromRun(results []browser.ActionResult, err error) (Observation, error) {
	obs := Observation{}
	if err != nil {
		obs.OK = false
		obs.ErrText = err.Error()
		return obs, nil
	}
	if len(results) == 0 {
		obs.OK = false
		obs.ErrText = "no result"
		return obs, nil
	}
	r0 := results[0]
	obs.OK = r0.OK
	if !r0.OK && r0.Err != nil {
		obs.ErrText = r0.Err.Error()
	}
	obs.Text = r0.Text
	obs.URL = r0.URL
	return obs, nil
}

// Compile-time assertion that BackendDriver satisfies Driver.
var _ Driver = (*BackendDriver)(nil)
