package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWALAppendAndTail(t *testing.T) {
	w, err := OpenWAL(filepath.Join(t.TempDir(), "daemon.wal"))
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	defer w.Close()

	if err := w.Append(NewIntent("t-1", "w-1", "starting handler promotion")); err != nil {
		t.Fatalf("append intent: %v", err)
	}
	if err := w.Append(NewDone("t-1", "w-1", "promoted 4 handlers", map[string]string{
		"pr":        "PR #255",
		"file_line": "packages/agents/src/agents/email-reply-drafter-agent.ts:1",
	})); err != nil {
		t.Fatalf("append done: %v", err)
	}
	if err := w.Append(NewBlocked("t-2", "w-2", "rate-limit on anthropic")); err != nil {
		t.Fatalf("append blocked: %v", err)
	}

	got, err := w.Tail(10)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}
	if got[0].Type != "intent" {
		t.Errorf("got[0].Type = %q, want intent", got[0].Type)
	}
	if got[1].Type != "done" {
		t.Errorf("got[1].Type = %q, want done", got[1].Type)
	}
	if got[2].Type != "blocked" {
		t.Errorf("got[2].Type = %q, want blocked", got[2].Type)
	}
	if got[1].Evidence["pr"] != "PR #255" {
		t.Errorf("evidence[pr] = %q, want PR #255", got[1].Evidence["pr"])
	}
	if got[1].Evidence["file_line"] == "" {
		t.Errorf("evidence[file_line] empty")
	}
}

func TestWALTailRespectsCap(t *testing.T) {
	w, err := OpenWAL(filepath.Join(t.TempDir(), "daemon.wal"))
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	defer w.Close()
	for i := 0; i < 50; i++ {
		if err := w.Append(NewIntent("t", "w", "tick")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	got, err := w.Tail(10)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("expected 10, got %d", len(got))
	}
	if got[0].Message != "tick" || got[9].Message != "tick" {
		t.Errorf("messages corrupted: %v ... %v", got[0].Message, got[9].Message)
	}
}

func TestWALMissingTypeFails(t *testing.T) {
	w, err := OpenWAL(filepath.Join(t.TempDir(), "daemon.wal"))
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	defer w.Close()
	if err := w.Append(WALEvent{Message: "no type"}); err == nil {
		t.Fatalf("expected error on missing type")
	}
}

func TestWALConcurrentAppend(t *testing.T) {
	w, err := OpenWAL(filepath.Join(t.TempDir(), "daemon.wal"))
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	defer w.Close()
	const n = 100
	done := make(chan struct{}, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			if err := w.Append(NewIntent("t", "w", "concurrent")); err != nil {
				errs <- err
			}
		}()
	}
	drainN(done, n)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent append err: %v", err)
	}
	got, err := w.Tail(n + 10)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(got) != n {
		t.Fatalf("expected %d events, got %d", n, len(got))
	}
}

func TestWALMissingFileTailReturnsNil(t *testing.T) {
	w := &WAL{path: filepath.Join(t.TempDir(), "nope.wal")}
	got, err := w.Tail(10)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing file, got %+v", got)
	}
}

func TestWALCorruptLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	if err := w.Append(NewIntent("t", "w", "good")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.Write([]byte("not-json-at-all\n")); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close inject: %v", err)
	}

	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("re-OpenWAL: %v", err)
	}
	defer w2.Close()
	if err := w2.Append(NewIntent("t", "w", "second good")); err != nil {
		t.Fatalf("append after corrupt: %v", err)
	}

	got, err := w2.Tail(10)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 valid events (corrupt skipped), got %d", len(got))
	}
	if got[0].Message != "good" || got[1].Message != "second good" {
		t.Errorf("messages: got[0]=%q got[1]=%q", got[0].Message, got[1].Message)
	}
}
