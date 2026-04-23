//go:build stoke_rod

// rod_real_test.go: integration-ish tests for the real go-rod
// Backend. Each test spins a local httptest.Server, drives a real
// headless Chromium via the RodClient, and asserts on the resulting
// ActionResult / FetchResult.
//
// Tests are tagged stoke_rod so they only run when callers opt in
// with `go test -tags stoke_rod`. They also honor -short: launching
// Chromium is slow and flaky in constrained CI, so `go test -short`
// skips the whole file.

package browser

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestRod is the shared setup: construct a real RodClient, skip
// on -short, and install a cleanup. Returns nil and skips when the
// Chromium launch fails (typical CI sandbox issue).
func newTestRod(t *testing.T) *RodClient {
	t.Helper()
	if testing.Short() {
		t.Skip("rod tests skipped in -short mode (Chromium launch)")
	}
	rc, err := NewRodClient(RodConfig{HeadlessMode: true, Timeout: 15 * time.Second})
	if err != nil {
		t.Skipf("rod construct failed: %v", err)
	}
	// Eagerly ensure the browser — surfaces sandbox/download issues
	// as a skip, not a test failure.
	if _, err := rc.ensureBrowser(); err != nil {
		_ = rc.Close()
		t.Skipf("rod browser launch failed (CI sandbox?): %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

// navPage is a tiny HTML page used across the navigation / extract
// tests. Single <h1 id="msg">, a <button>, an <input id="in">.
const navPage = `<!doctype html>
<html><head><title>rod-test</title></head>
<body>
<h1 id="msg">hello from rod</h1>
<button id="btn" onclick="document.getElementById('msg').innerText='clicked'">click me</button>
<input id="in" type="text"/>
<a id="lnk" href="https://example.com/target">linky</a>
</body></html>`

func startPageServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRod_Navigate(t *testing.T) {
	rc := newTestRod(t)
	srv := startPageServer(t, navPage)

	fr, err := rc.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(fr.Text, "hello from rod") {
		t.Errorf("page text missing H1 content: %q", fr.Text)
	}
	if fr.Title != "rod-test" {
		t.Errorf("title=%q, want rod-test", fr.Title)
	}
	if fr.URL == "" {
		t.Errorf("URL should be populated")
	}
}

func TestRod_Click(t *testing.T) {
	rc := newTestRod(t)
	srv := startPageServer(t, navPage)

	results, err := rc.RunActions(context.Background(), []Action{
		{Kind: ActionNavigate, URL: srv.URL},
		{Kind: ActionClick, Selector: "#btn"},
		{Kind: ActionExtractText, Selector: "#msg"},
	})
	if err != nil {
		t.Fatalf("RunActions: %v (results=%+v)", err, results)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}
	for i, r := range results {
		if !r.OK {
			t.Errorf("result %d (%s) not OK: %v", i, r.Kind, r.Err)
		}
	}
	extracted := results[2].Text
	if extracted != "clicked" {
		t.Errorf("after click, extracted text=%q, want %q", extracted, "clicked")
	}
}

func TestRod_Type(t *testing.T) {
	rc := newTestRod(t)
	srv := startPageServer(t, navPage)

	results, err := rc.RunActions(context.Background(), []Action{
		{Kind: ActionNavigate, URL: srv.URL},
		{Kind: ActionType, Selector: "#in", Text: "hello-typed"},
		{Kind: ActionExtractAttribute, Selector: "#in", Attribute: "value"},
	})
	if err != nil {
		t.Fatalf("RunActions: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}
	for i, r := range results {
		if !r.OK {
			t.Fatalf("result %d (%s) not OK: %v", i, r.Kind, r.Err)
		}
	}
	if got := results[2].Attribute; got != "hello-typed" {
		t.Errorf("input value=%q, want %q", got, "hello-typed")
	}
}

func TestRod_Screenshot(t *testing.T) {
	rc := newTestRod(t)
	srv := startPageServer(t, navPage)

	dir := t.TempDir()
	out := filepath.Join(dir, ".stoke", "browser", "task-42", "step1.png")

	results, err := rc.RunActions(context.Background(), []Action{
		{Kind: ActionNavigate, URL: srv.URL},
		{Kind: ActionScreenshot, OutputPath: out},
	})
	if err != nil {
		t.Fatalf("RunActions: %v", err)
	}
	if len(results) != 2 || !results[1].OK {
		t.Fatalf("screenshot action failed: %+v", results)
	}
	shot := results[1].ScreenshotPNG
	if len(shot) == 0 {
		t.Fatal("screenshot bytes empty")
	}
	// PNG magic number: 89 50 4E 47 0D 0A 1A 0A
	if !bytes.HasPrefix(shot, []byte{0x89, 0x50, 0x4E, 0x47}) {
		t.Errorf("bytes are not a PNG (first 4: % x)", shot[:4])
	}
	// File should exist at OutputPath too.
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("screenshot file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Error("screenshot file is empty")
	}
}

func TestRod_Extract(t *testing.T) {
	rc := newTestRod(t)
	srv := startPageServer(t, navPage)

	results, err := rc.RunActions(context.Background(), []Action{
		{Kind: ActionNavigate, URL: srv.URL},
		{Kind: ActionExtractText, Selector: "#msg"},
		{Kind: ActionExtractAttribute, Selector: "#lnk", Attribute: "href"},
	})
	if err != nil {
		t.Fatalf("RunActions: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}
	if results[1].Text != "hello from rod" {
		t.Errorf("extract_text=%q, want %q", results[1].Text, "hello from rod")
	}
	if results[2].Attribute != "https://example.com/target" {
		t.Errorf("extract_attribute=%q, want https://example.com/target", results[2].Attribute)
	}
}

func TestRod_ElementNotFound(t *testing.T) {
	rc := newTestRod(t)
	srv := startPageServer(t, navPage)

	results, err := rc.RunActions(context.Background(), []Action{
		{Kind: ActionNavigate, URL: srv.URL},
		{Kind: ActionClick, Selector: "#does-not-exist", Timeout: 500 * time.Millisecond},
	})
	if err == nil {
		t.Fatal("want error for missing selector")
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results with failure at index 1, got %+v", results)
	}
	if results[1].OK {
		t.Errorf("click on missing element should fail: %+v", results[1])
	}
}

func TestRod_Close(t *testing.T) {
	rc, err := NewRodClient(RodConfig{HeadlessMode: true})
	if err != nil {
		t.Skipf("construct failed: %v", err)
	}
	// Close without ever launching should be a no-op and not panic.
	if err := rc.Close(); err != nil {
		t.Errorf("Close on unlaunched client: %v", err)
	}
	// Double-close is safe.
	if err := rc.Close(); err != nil {
		t.Errorf("double Close: %v", err)
	}
}
