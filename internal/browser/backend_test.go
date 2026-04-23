package browser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_SatisfiesBackend(t *testing.T) {
	t.Parallel()
	var _ Backend = NewClient()
}

func TestClientRunActions_NavigateOnly(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`<html><body>Page A</body></html>`))
	}))
	defer srv.Close()

	c := NewClient()
	results, err := c.RunActions(context.Background(), []Action{
		{Kind: ActionNavigate, URL: srv.URL},
	})
	if err != nil {
		t.Fatalf("RunActions: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.OK || r.Kind != ActionNavigate {
		t.Errorf("bad result: %+v", r)
	}
	if !strings.Contains(r.Text, "Page A") {
		t.Errorf("text missing page body: %q", r.Text)
	}
	if r.URL == "" {
		t.Errorf("URL should be populated: %+v", r)
	}
}

func TestClientRunActions_RejectsInteractive(t *testing.T) {
	t.Parallel()
	c := NewClient()
	cases := []Action{
		{Kind: ActionClick, Selector: "#x"},
		{Kind: ActionType, Selector: "#x", Text: "y"},
		{Kind: ActionWaitForSelector, Selector: "#x"},
		{Kind: ActionWaitForNetworkIdle},
		{Kind: ActionScreenshot},
		{Kind: ActionExtractText, Selector: "h1"},
		{Kind: ActionExtractAttribute, Selector: "a", Attribute: "href"},
	}
	for _, a := range cases {
		a := a
		t.Run(string(a.Kind), func(t *testing.T) {
			t.Parallel()
			_, err := c.RunActions(context.Background(), []Action{a})
			if err == nil {
				t.Fatalf("want ErrInteractiveUnsupported, got nil")
			}
			var eu *ErrInteractiveUnsupported
			if !errors.As(err, &eu) {
				t.Fatalf("want ErrInteractiveUnsupported, got %T: %v", err, err)
			}
			if eu.Kind != a.Kind {
				t.Errorf("Kind = %q, want %q", eu.Kind, a.Kind)
			}
		})
	}
}

func TestClientRunActions_ValidationError(t *testing.T) {
	t.Parallel()
	c := NewClient()
	_, err := c.RunActions(context.Background(), []Action{{Kind: ActionNavigate}}) // missing URL
	if err == nil || !strings.Contains(err.Error(), "requires URL") {
		t.Fatalf("want validation error, got %v", err)
	}
}

func TestClientRunActions_EmptyInput(t *testing.T) {
	t.Parallel()
	c := NewClient()
	r, err := c.RunActions(context.Background(), nil)
	if err != nil || r != nil {
		t.Errorf("empty input should be no-op, got results=%v err=%v", r, err)
	}
}

func TestClientRunActions_NavigateError(t *testing.T) {
	t.Parallel()
	c := NewClient()
	results, err := c.RunActions(context.Background(), []Action{
		{Kind: ActionNavigate, URL: "http://127.0.0.1:1/unreachable"},
	})
	if err == nil {
		t.Fatal("want navigation error")
	}
	var enav *ErrNavigationFailed
	if !errors.As(err, &enav) {
		t.Fatalf("want ErrNavigationFailed, got %T: %v", err, err)
	}
	if len(results) != 1 || results[0].OK {
		t.Errorf("result should be populated with OK=false: %+v", results)
	}
}

func TestClient_Close(t *testing.T) {
	t.Parallel()
	if err := NewClient().Close(); err != nil {
		t.Errorf("Close should be no-op: %v", err)
	}
}

func TestNewRodClient_NoTagReturnsError(t *testing.T) {
	t.Parallel()
	// Without the stoke_rod build tag, NewRodClient must return a
	// clear ErrChromeLaunchFailed — this is the single-binary
	// kill-switch.
	c, err := NewRodClient(RodConfig{PoolSize: 3})
	if err == nil {
		t.Fatalf("want error without stoke_rod tag, got client %v", c)
	}
	var elaunch *ErrChromeLaunchFailed
	if !errors.As(err, &elaunch) {
		t.Fatalf("want ErrChromeLaunchFailed, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "stoke_rod") {
		t.Errorf("error should mention stoke_rod tag: %q", err.Error())
	}
}

func TestRodClientStub_MethodsErr(t *testing.T) {
	t.Parallel()
	// Construct the stub type directly (bypassing NewRodClient).
	// Its Fetch/RunActions must also error clearly; Close is a no-op.
	r := &RodClient{}
	if _, err := r.Fetch(context.Background(), "https://example.com"); err == nil {
		t.Errorf("stub Fetch should error")
	}
	if _, err := r.RunActions(context.Background(), []Action{{Kind: ActionClick, Selector: "x"}}); err == nil {
		t.Errorf("stub RunActions should error")
	}
	if err := r.Close(); err != nil {
		t.Errorf("stub Close should no-op: %v", err)
	}
	// Compile-time: satisfies Backend
	var _ Backend = r
}
