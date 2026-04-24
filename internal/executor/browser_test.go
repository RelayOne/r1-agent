package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/browser"
)

func TestBrowserExecutor_TaskType(t *testing.T) {
	e := NewBrowserExecutor()
	if e.TaskType() != TaskBrowser {
		t.Errorf("TaskType=%v", e.TaskType())
	}
}

func TestBrowserExecutor_Execute_EmptyURL(t *testing.T) {
	e := NewBrowserExecutor()
	_, err := e.Execute(context.Background(), Plan{Query: ""}, EffortStandard)
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestBrowserExecutor_Execute_InteractiveNotWired(t *testing.T) {
	e := NewBrowserExecutor()
	_, err := e.Execute(context.Background(), Plan{
		Query: "http://example.com",
		Extra: map[string]any{"interactive": true},
	}, EffortStandard)
	if err == nil {
		t.Fatal("expected not-wired error")
	}
	var notWired *ExecutorNotWiredError
	if !errors.As(err, &notWired) {
		t.Errorf("expected ExecutorNotWiredError, got %T: %v", err, err)
	}
}

func TestBrowserExecutor_Execute_Succeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`<html><head><title>T</title></head><body>ok</body></html>`))
	}))
	defer srv.Close()
	e := NewBrowserExecutor()
	d, err := e.Execute(context.Background(), Plan{Query: srv.URL}, EffortStandard)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	bd, ok := d.(BrowserDeliverable)
	if !ok {
		t.Fatalf("wrong deliverable type: %T", d)
	}
	if bd.Result.Status != 200 {
		t.Errorf("status=%d", bd.Result.Status)
	}
}

func TestBrowserExecutor_BuildCriteria_Base(t *testing.T) {
	e := NewBrowserExecutor()
	bd := BrowserDeliverable{Result: browser.FetchResult{Status: 200, Text: "hello world"}}
	acs := e.BuildCriteria(Task{}, bd)
	if len(acs) != 1 {
		t.Fatalf("want 1 AC (BROWSER-LOADED), got %d", len(acs))
	}
	if acs[0].ID != "BROWSER-LOADED" {
		t.Errorf("id=%q", acs[0].ID)
	}
	ok, _ := acs[0].VerifyFunc(context.Background())
	if !ok {
		t.Error("200 should pass BROWSER-LOADED")
	}
}

func TestBrowserExecutor_BuildCriteria_WithExpectedText(t *testing.T) {
	e := NewBrowserExecutor()
	bd := BrowserDeliverable{
		Result:       browser.FetchResult{Status: 200, Text: "hello world"},
		ExpectedText: "world",
	}
	acs := e.BuildCriteria(Task{}, bd)
	if len(acs) != 2 {
		t.Fatalf("want 2 ACs, got %d", len(acs))
	}
	ok, _ := acs[1].VerifyFunc(context.Background())
	if !ok {
		t.Error("expected_text 'world' should pass against 'hello world'")
	}
}

func TestBrowserExecutor_BuildCriteria_404Fails(t *testing.T) {
	e := NewBrowserExecutor()
	bd := BrowserDeliverable{Result: browser.FetchResult{Status: 404}}
	acs := e.BuildCriteria(Task{}, bd)
	ok, reason := acs[0].VerifyFunc(context.Background())
	if ok {
		t.Error("404 should fail BROWSER-LOADED")
	}
	if !strings.Contains(reason, "404") {
		t.Errorf("reason missing status: %q", reason)
	}
}

func TestBrowserExecutor_EnvFix(t *testing.T) {
	e := NewBrowserExecutor()
	fn := e.BuildEnvFixFunc()
	if !fn(context.Background(), "i/o timeout", "") {
		t.Error("timeout should be transient")
	}
	if fn(context.Background(), "404 not found", "") {
		t.Error("404 should NOT be transient")
	}
}

func TestBrowserDeliverable_Summary(t *testing.T) {
	d := BrowserDeliverable{Result: browser.FetchResult{URL: "http://x", Status: 200, BodyBytes: 42}}
	if !strings.Contains(d.Summary(), "http://x") {
		t.Errorf("summary missing URL: %q", d.Summary())
	}
	if d.Size() != 42 {
		t.Errorf("size=%d", d.Size())
	}
}
