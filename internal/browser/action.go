// action.go: Action + ActionResult types.
//
// The eight interactive primitives from spec 17 §3.4. All actions
// share one struct; Kind discriminates and Validate() rejects
// malformed instances early, before the rod backend dispatches them.
// No build tag — these types are the executor contract, stdlib-only,
// always compiled.

package browser

import (
	"errors"
	"fmt"
	"time"
)

// ActionKind enumerates the interactive primitives. The string form
// is used by parseActionFlag for CLI --action arguments and by
// BuildCriteria when forming BROWSER-ACTION-SUCCESS-<i>-<KIND> AC
// IDs.
type ActionKind string

const (
	ActionNavigate           ActionKind = "navigate"
	ActionClick              ActionKind = "click"
	ActionType               ActionKind = "type"
	ActionWaitForSelector    ActionKind = "wait_for_selector"
	ActionWaitForNetworkIdle ActionKind = "wait_for_network_idle"
	ActionScreenshot         ActionKind = "screenshot"
	ActionExtractText        ActionKind = "extract_text"
	ActionExtractAttribute   ActionKind = "extract_attribute"
)

// AllActionKinds returns the eight kinds in stable order — used by
// tests and CLI help text.
func AllActionKinds() []ActionKind {
	return []ActionKind{
		ActionNavigate,
		ActionClick,
		ActionType,
		ActionWaitForSelector,
		ActionWaitForNetworkIdle,
		ActionScreenshot,
		ActionExtractText,
		ActionExtractAttribute,
	}
}

// Action describes a single interactive step. Fields are shared
// across kinds; not all fields apply to every kind. Validate()
// enforces per-kind required fields.
type Action struct {
	Kind       ActionKind
	URL        string        // navigate
	Selector   string        // click / type / wait_for_selector / extract_*
	Text       string        // type
	Attribute  string        // extract_attribute
	OutputPath string        // screenshot (optional; bytes always returned in ActionResult)
	Timeout    time.Duration // per-action; 0 → default (10s for waits, 30s for navigate)
}

// DefaultTimeout returns the per-kind default when a.Timeout == 0.
// navigate + wait_for_network_idle default to 30s (network-bound);
// everything else defaults to 10s (CPU-bound DOM query).
func (a Action) DefaultTimeout() time.Duration {
	if a.Timeout > 0 {
		return a.Timeout
	}
	switch a.Kind {
	case ActionNavigate, ActionWaitForNetworkIdle:
		return 30 * time.Second
	default:
		return 10 * time.Second
	}
}

// Validate rejects malformed actions before they reach the backend.
// Per-kind rules:
//   - navigate requires URL.
//   - click / type / wait_for_selector / extract_* require Selector.
//   - type additionally requires Text.
//   - extract_attribute additionally requires Attribute.
//   - wait_for_network_idle + screenshot have no required fields.
func (a Action) Validate() error {
	switch a.Kind {
	case ActionNavigate:
		if a.URL == "" {
			return errors.New("browser.Action: navigate requires URL")
		}
	case ActionClick:
		if a.Selector == "" {
			return errors.New("browser.Action: click requires Selector")
		}
	case ActionType:
		if a.Selector == "" {
			return errors.New("browser.Action: type requires Selector")
		}
		if a.Text == "" {
			return errors.New("browser.Action: type requires Text")
		}
	case ActionWaitForSelector:
		if a.Selector == "" {
			return errors.New("browser.Action: wait_for_selector requires Selector")
		}
	case ActionWaitForNetworkIdle:
		// no required fields
	case ActionScreenshot:
		// no required fields
	case ActionExtractText:
		if a.Selector == "" {
			return errors.New("browser.Action: extract_text requires Selector")
		}
	case ActionExtractAttribute:
		if a.Selector == "" {
			return errors.New("browser.Action: extract_attribute requires Selector")
		}
		if a.Attribute == "" {
			return errors.New("browser.Action: extract_attribute requires Attribute")
		}
	default:
		return fmt.Errorf("browser.Action: unknown kind %q", a.Kind)
	}
	return nil
}

// ActionResult captures the outcome of one action. OK is the
// top-level success flag; Err carries the structured error when
// OK==false. Text / Attribute / ScreenshotPNG / URL are populated
// per-kind (empty for kinds that don't produce that field).
type ActionResult struct {
	Kind          ActionKind
	OK            bool
	Err           error
	Text          string // extract_text / extract_attribute
	Attribute     string // extract_attribute (same as Text)
	ScreenshotPNG []byte // screenshot
	URL           string // navigate (final URL after redirects)
	DurationMs    int64
}

// Summary returns a one-line human description of the result. Used
// by BrowserInteractiveDeliverable.Summary() and in log lines.
func (r ActionResult) Summary() string {
	status := "ok"
	if !r.OK {
		status = "fail"
		if r.Err != nil {
			status = "fail: " + r.Err.Error()
		}
	}
	return fmt.Sprintf("[%s] %s (%dms)", r.Kind, status, r.DurationMs)
}
