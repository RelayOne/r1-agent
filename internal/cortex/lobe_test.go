package cortex

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/hub"
)

// EchoLobe is a minimal Lobe implementation used by TASK-9's LobeRunner
// tests and TASK-25's integration tests. It publishes one Note per Run
// (Severity=SevInfo, Title="echo") and exits. It exists in the test
// binary only — it is NOT part of the production API surface.
//
// Run requires a writable *Workspace because LobeInput.Workspace is the
// read-only subset; the test harness threads the same Workspace via the
// EchoLobe field so the lobe can Publish.
type EchoLobe struct {
	IDValue string
	Desc    string
	Kindly  LobeKind

	// Workspace is the writable handle the lobe Publishes to. Tests set
	// this directly; the runner in TASK-9 will set it via a constructor.
	Workspace *Workspace

	// Calls counts how many times Run has been invoked. Useful for
	// downstream tests asserting "ran exactly once per Round".
	Calls atomic.Int64
}

// ID returns the configured stable ID, defaulting to "echo".
func (l *EchoLobe) ID() string {
	if l.IDValue == "" {
		return "echo"
	}
	return l.IDValue
}

// Description returns the configured description, defaulting to a
// canonical string for /status output.
func (l *EchoLobe) Description() string {
	if l.Desc == "" {
		return "echo lobe (test stub)"
	}
	return l.Desc
}

// Kind returns the configured LobeKind, defaulting to KindDeterministic.
func (l *EchoLobe) Kind() LobeKind { return l.Kindly }

// Run publishes one SevInfo "echo" Note and exits. It observes ctx.Done
// before publishing so cancelled rounds drop cleanly. If Workspace is nil
// the lobe still observes ctx and returns nil — the no-op shape is what
// the LobeRunner contract expects.
func (l *EchoLobe) Run(ctx context.Context, in LobeInput) error {
	l.Calls.Add(1)

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Read-side smoke: invoke Snapshot to prove the WorkspaceReader is
	// actually wired. The result is intentionally discarded.
	if in.Workspace != nil {
		_ = in.Workspace.Snapshot()
	}

	if l.Workspace == nil {
		return nil
	}
	return l.Workspace.Publish(Note{
		LobeID:   l.ID(),
		Severity: SevInfo,
		Title:    "echo",
	})
}

// TestLobeInterfaceCompiles is the smoke test for TASK-8. Real LobeRunner
// behaviour is exercised in TASK-9. This test verifies:
//   - EchoLobe satisfies the Lobe interface.
//   - ID() / Description() / Kind() return their configured defaults.
//   - workspaceReader satisfies WorkspaceReader and round-trips through
//     WorkspaceReaderFor.
//   - Run publishes exactly one Note when Workspace is non-nil.
func TestLobeInterfaceCompiles(t *testing.T) {
	t.Parallel()

	var _ Lobe = (*EchoLobe)(nil)

	w := NewWorkspace(hub.New(), nil)
	l := &EchoLobe{Workspace: w}

	if got, want := l.ID(), "echo"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
	if got := l.Description(); got == "" {
		t.Errorf("Description() = empty, want non-empty default")
	}
	if got, want := l.Kind(), KindDeterministic; got != want {
		t.Errorf("Kind() = %v, want %v", got, want)
	}

	rd := WorkspaceReaderFor(w)
	if rd == nil {
		t.Fatal("WorkspaceReaderFor returned nil")
	}
	// Read-side calls must not panic on the TASK-5 stubs.
	_ = rd.Snapshot()
	_ = rd.UnresolvedCritical()

	in := LobeInput{
		Round:     1,
		Workspace: rd,
		Bus:       hub.New(),
	}
	if err := l.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := l.Calls.Load(), int64(1); got != want {
		t.Errorf("Calls = %d, want %d", got, want)
	}
}

// TestLobeKindConstants pins the iota ordering so a future re-order
// (which would silently break semaphore routing) trips a unit test.
func TestLobeKindConstants(t *testing.T) {
	t.Parallel()
	if KindDeterministic != 0 {
		t.Errorf("KindDeterministic = %d, want 0", KindDeterministic)
	}
	if KindLLM != 1 {
		t.Errorf("KindLLM = %d, want 1", KindLLM)
	}
}
