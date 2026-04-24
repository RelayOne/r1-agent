package operator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestTerminal_Ask_FreeText(t *testing.T) {
	in := strings.NewReader("hello\n")
	var out, errW bytes.Buffer
	term := NewTerminalFrom(in, &out, &errW)

	got, err := term.Ask(context.Background(), "say something", nil)
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if got != "hello" {
		t.Fatalf("Ask result: got %q, want %q", got, "hello")
	}
	if !strings.Contains(out.String(), "say something") {
		t.Fatalf("prompt not written to out; got %q", out.String())
	}
}

func TestTerminal_Ask_WithOptions_TrimsNewline(t *testing.T) {
	in := strings.NewReader("retry\n")
	var out, errW bytes.Buffer
	term := NewTerminalFrom(in, &out, &errW)

	opts := []Option{{Label: "retry"}, {Label: "skip"}}
	got, err := term.Ask(context.Background(), "choose:", opts)
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if got != "retry" {
		t.Fatalf("Ask result: got %q, want %q", got, "retry")
	}
	if !strings.Contains(out.String(), "[retry]") {
		t.Fatalf("options not written; got %q", out.String())
	}
	if !strings.Contains(out.String(), "[skip]") {
		t.Fatalf("options not written; got %q", out.String())
	}
}

func TestTerminal_Ask_CtxCanceled(t *testing.T) {
	// An io.Pipe without a writer write will block forever — perfect for
	// simulating a stalled stdin.
	pr, pw := io.Pipe()
	defer pw.Close()
	var out, errW bytes.Buffer
	term := NewTerminalFrom(pr, &out, &errW)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the call starts.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	got, err := term.Ask(ctx, "stall", nil)
	if err == nil {
		t.Fatalf("expected error from canceled ctx, got nil (result=%q)", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty result on cancel, got %q", got)
	}
}

func TestTerminal_Notify_Prefixes(t *testing.T) {
	var out, errW bytes.Buffer
	term := NewTerminalFrom(strings.NewReader(""), &out, &errW)

	term.Notify(KindError, "bad")
	if !strings.Contains(out.String(), "ERROR: bad") {
		t.Fatalf("expected ERROR prefix; got %q", out.String())
	}

	out.Reset()
	term.Notify(KindWarn, "careful")
	if !strings.Contains(out.String(), "WARN: careful") {
		t.Fatalf("expected WARN prefix; got %q", out.String())
	}

	out.Reset()
	term.Notify(KindSuccess, "done")
	if !strings.Contains(out.String(), "OK: done") {
		t.Fatalf("expected OK prefix; got %q", out.String())
	}

	out.Reset()
	term.Notify(KindInfo, "fyi")
	if !strings.Contains(out.String(), "fyi") {
		t.Fatalf("expected info message; got %q", out.String())
	}
	// Info has no prefix.
	if strings.Contains(out.String(), "INFO:") {
		t.Fatalf("info should not have prefix; got %q", out.String())
	}
}

func TestNoOp_Ask_ReturnsDefault(t *testing.T) {
	n := &NoOp{Default: "yes"}
	got, err := n.Ask(context.Background(), "whatever", []Option{{Label: "yes"}, {Label: "no"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "yes" {
		t.Fatalf("NoOp.Ask: got %q, want %q", got, "yes")
	}
}

func TestNoOp_Notify_Silent(t *testing.T) {
	n := &NoOp{}
	// Should not panic for any kind.
	n.Notify(KindInfo, "x")
	n.Notify(KindWarn, "x")
	n.Notify(KindError, "x")
	n.Notify(KindSuccess, "x")
}

// Compile-time assertions that implementations satisfy Operator.
var (
	_ Operator = (*Terminal)(nil)
	_ Operator = (*NoOp)(nil)
)
