// browseragent_test.go — unit tests for the autonomous browser
// operator. The tests stub both the Planner (LLM) and the Driver
// (browser) so the loop runs end-to-end with no Chromium dependency.

package browseragent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/browser"
)

// scriptedPlanner returns a fixed sequence of actions, terminating
// with KindDone or KindGiveUp. Tests use it to drive the loop
// deterministically.
type scriptedPlanner struct {
	actions []Action
	idx     int
	calls   int
}

func (p *scriptedPlanner) Next(_ context.Context, _ string, _ []Observation) (Action, error) {
	p.calls++
	if p.idx >= len(p.actions) {
		return Action{Kind: KindGiveUp, Reason: "script exhausted"}, nil
	}
	a := p.actions[p.idx]
	p.idx++
	return a, nil
}

// fakeDriver is a minimal in-memory Driver. Tracks call order so
// tests can assert the planner-driven action sequence.
type fakeDriver struct {
	calls       []string
	failKinds   map[string]bool // when set, named kind returns OK=false
	textForExt  map[string]string
	currentURL  string
	screenshots int
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		failKinds:  map[string]bool{},
		textForExt: map[string]string{},
	}
}

func (d *fakeDriver) record(kind string) Observation {
	d.calls = append(d.calls, kind)
	if d.failKinds[kind] {
		return Observation{OK: false, ErrText: "scripted-fail"}
	}
	return Observation{OK: true}
}

func (d *fakeDriver) Navigate(_ context.Context, url string) (Observation, error) {
	obs := d.record("navigate")
	if obs.OK {
		d.currentURL = url
		obs.URL = url
	}
	return obs, nil
}
func (d *fakeDriver) Click(_ context.Context, sel string) (Observation, error) {
	return d.record("click:" + sel), nil
}
func (d *fakeDriver) Type(_ context.Context, sel, _ string) (Observation, error) {
	return d.record("type:" + sel), nil
}
func (d *fakeDriver) WaitFor(_ context.Context, sel string, _ time.Duration) (Observation, error) {
	return d.record("wait:" + sel), nil
}
func (d *fakeDriver) Extract(_ context.Context, sel string) (Observation, error) {
	obs := d.record("extract:" + sel)
	if obs.OK {
		obs.Text = d.textForExt[sel]
	}
	return obs, nil
}
func (d *fakeDriver) Screenshot(_ context.Context, savePath string) (Observation, error) {
	d.screenshots++
	obs := d.record("screenshot")
	if obs.OK && savePath != "" {
		obs.ScreenshotPath = savePath
	}
	return obs, nil
}

// TestRunHappyPath exercises the canonical Manus-style loop:
// navigate → wait_for → extract → done. Asserts both the planner
// got called the right number of times and the driver received the
// expected actions in order.
func TestRunHappyPath(t *testing.T) {
	planner := &scriptedPlanner{
		actions: []Action{
			{Kind: KindNavigate, URL: "https://example.com", Reason: "open landing"},
			{Kind: KindWaitFor, Selector: "h1", Reason: "wait for header"},
			{Kind: KindExtractText, Selector: "h1", Reason: "read title"},
			{Kind: KindDone, Answer: "found the headline", Reason: "objective met"},
		},
	}
	drv := newFakeDriver()
	drv.textForExt["h1"] = "Hello World"

	res, err := Run(context.Background(), "find the headline", planner, drv, Config{
		MaxSteps:     10,
		StepDeadline: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected Success, got %+v", res)
	}
	if res.Answer != "found the headline" {
		t.Errorf("answer mismatch: got %q", res.Answer)
	}
	if res.Steps != 4 {
		t.Errorf("expected 4 steps, got %d", res.Steps)
	}
	wantCalls := []string{"navigate", "wait:h1", "extract:h1"}
	if len(drv.calls) != len(wantCalls) {
		t.Fatalf("driver calls = %v, want %v", drv.calls, wantCalls)
	}
	for i, want := range wantCalls {
		if drv.calls[i] != want {
			t.Errorf("call[%d] = %q, want %q", i, drv.calls[i], want)
		}
	}
}

// TestRunStepCap proves the loop terminates when the planner never
// emits a terminal action. MaxSteps=3 should produce exactly 3
// planner calls + a non-success Result with a step-cap GaveUpReason.
func TestRunStepCap(t *testing.T) {
	planner := &scriptedPlanner{
		actions: []Action{
			{Kind: KindScreenshot},
			{Kind: KindScreenshot},
			{Kind: KindScreenshot},
			{Kind: KindScreenshot}, // never reached
		},
	}
	drv := newFakeDriver()

	res, err := Run(context.Background(), "watch forever", planner, drv, Config{
		MaxSteps:     3,
		StepDeadline: time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success {
		t.Fatal("step-capped run should not be Success")
	}
	if res.Steps != 3 {
		t.Errorf("expected 3 steps, got %d", res.Steps)
	}
	if !strings.Contains(res.GaveUpReason, "step cap") {
		t.Errorf("GaveUpReason missing step-cap text: %q", res.GaveUpReason)
	}
	if planner.calls != 3 {
		t.Errorf("planner.calls = %d, want 3", planner.calls)
	}
}

// TestRunGiveUp confirms the give_up sentinel ends the loop and
// surfaces the planner's reason.
func TestRunGiveUp(t *testing.T) {
	planner := &scriptedPlanner{
		actions: []Action{
			{Kind: KindNavigate, URL: "https://example.com"},
			{Kind: KindGiveUp, Reason: "captcha detected"},
		},
	}
	drv := newFakeDriver()

	res, err := Run(context.Background(), "submit form", planner, drv, Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success {
		t.Fatal("give_up should produce non-success Result")
	}
	if res.GaveUpReason != "captcha detected" {
		t.Errorf("GaveUpReason = %q, want 'captcha detected'", res.GaveUpReason)
	}
}

// TestRunDriverFailureContinues confirms a failing driver step does
// NOT abort the loop — the planner sees the failure as an
// observation and can recover.
func TestRunDriverFailureContinues(t *testing.T) {
	planner := &scriptedPlanner{
		actions: []Action{
			{Kind: KindClick, Selector: "#missing", Reason: "first try"},
			{Kind: KindClick, Selector: "#fallback", Reason: "recover"},
			{Kind: KindDone, Answer: "recovered"},
		},
	}
	drv := newFakeDriver()
	drv.failKinds["click:#missing"] = true

	res, err := Run(context.Background(), "click the button", planner, drv, Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected recovery Success, got %+v", res)
	}
	if len(res.Observations) < 2 {
		t.Fatalf("expected at least 2 observations, got %d", len(res.Observations))
	}
	if res.Observations[0].OK {
		t.Error("first observation should be failure")
	}
	if !res.Observations[1].OK {
		t.Error("second observation should be recovery success")
	}
}

// TestRunRejectsInvalidAction confirms the loop refuses to execute a
// planner action that would crash the driver (missing required
// fields).
func TestRunRejectsInvalidAction(t *testing.T) {
	planner := &scriptedPlanner{
		actions: []Action{
			{Kind: KindNavigate}, // missing URL
		},
	}
	drv := newFakeDriver()

	_, err := Run(context.Background(), "go somewhere", planner, drv, Config{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "navigate requires url") {
		t.Errorf("error should mention required url, got: %v", err)
	}
}

// TestRunPlannerError surfaces planner failures with step context.
func TestRunPlannerError(t *testing.T) {
	planner := &errorPlanner{err: errors.New("rate limited")}
	drv := newFakeDriver()

	_, err := Run(context.Background(), "anything", planner, drv, Config{})
	if err == nil {
		t.Fatal("expected planner error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error should propagate planner cause: %v", err)
	}
}

type errorPlanner struct{ err error }

func (p *errorPlanner) Next(context.Context, string, []Observation) (Action, error) {
	return Action{}, p.err
}

// TestRunRejectsEmptyObjective documents the precondition.
func TestRunRejectsEmptyObjective(t *testing.T) {
	_, err := Run(context.Background(), "  ", &scriptedPlanner{}, newFakeDriver(), Config{})
	if err == nil {
		t.Fatal("expected error for empty objective")
	}
}

// TestActionValidate exhaustively covers the per-kind required-field
// rules — locks the contract the planner has to honour.
func TestActionValidate(t *testing.T) {
	cases := []struct {
		name    string
		a       Action
		wantErr bool
	}{
		{"navigate ok", Action{Kind: KindNavigate, URL: "https://x"}, false},
		{"navigate no url", Action{Kind: KindNavigate}, true},
		{"click ok", Action{Kind: KindClick, Selector: "#go"}, false},
		{"click no selector", Action{Kind: KindClick}, true},
		{"type ok empty text", Action{Kind: KindEnterText, Selector: "#in"}, false},
		{"type no selector", Action{Kind: KindEnterText, Text: "hi"}, true},
		{"wait_for ok", Action{Kind: KindWaitFor, Selector: ".ready"}, false},
		{"extract no selector", Action{Kind: KindExtractText}, true},
		{"screenshot ok", Action{Kind: KindScreenshot}, false},
		{"done ok", Action{Kind: KindDone, Answer: "yes"}, false},
		{"give_up ok", Action{Kind: KindGiveUp, Reason: "blocked"}, false},
		{"unknown kind", Action{Kind: "fly"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.a.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// TestBackendDriverRoundTrip wires the real BackendDriver against a
// stdlib browser.Client + httptest.Server, confirming the driver
// adapter speaks the Backend correctly. No Chromium needed.
func TestBackendDriverRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`<html><body><p id="msg">round trip</p></body></html>`))
	}))
	defer srv.Close()

	drv := &BackendDriver{
		Backend:     browser.NewClient(),
		WaitTimeout: 2 * time.Second,
	}

	obs, err := drv.Navigate(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Navigate err: %v", err)
	}
	if !obs.OK {
		t.Fatalf("Navigate not OK: %+v", obs)
	}
	if !strings.Contains(obs.URL, srv.URL) {
		t.Errorf("expected URL to contain %q, got %q", srv.URL, obs.URL)
	}

	// Click is unsupported on the stdlib client — should produce an
	// OK=false observation, NOT a Go error (so the planner can recover).
	obs2, err2 := drv.Click(context.Background(), "#go")
	if err2 != nil {
		t.Fatalf("Click should not return Go error on stdlib backend: %v", err2)
	}
	if obs2.OK {
		t.Error("stdlib backend should not report Click as OK")
	}
}
