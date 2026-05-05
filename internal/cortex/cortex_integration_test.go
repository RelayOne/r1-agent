package cortex

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
)

// echoIntegLobe is the deterministic Lobe used by TestCortexIntegration.
// Kind=KindDeterministic, ID="echo". Each Run publishes one SevInfo Note
// titled "echo" via the side-channel writable Workspace handle (the
// LobeInput.Workspace is the read-only adapter, so production-style
// Lobes that want to Publish carry a writable handle injected at
// construction — mirroring the EchoLobe pattern in lobe_test.go).
type echoIntegLobe struct {
	ws    *Workspace
	calls atomic.Int64
}

func (l *echoIntegLobe) ID() string          { return "echo" }
func (l *echoIntegLobe) Description() string { return "integration: deterministic echo lobe" }
func (l *echoIntegLobe) Kind() LobeKind      { return KindDeterministic }
func (l *echoIntegLobe) Run(ctx context.Context, in LobeInput) error {
	l.calls.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if l.ws == nil {
		return nil
	}
	return l.ws.Publish(Note{
		LobeID:   "echo",
		Severity: SevInfo,
		Title:    "echo",
	})
}

// warnIntegLobe is the LLM-classed Lobe used by TestCortexIntegration.
// Kind=KindLLM forces the runner to bind against LobeSemaphore before
// invoking Run; the lobe itself never calls the provider — it just
// Publishes a synthetic SevWarning Note. The fake provider on the
// Cortex remains untouched by this lobe; that is fine per spec item
// 25 (the integration test exercises the round/gate plumbing, not
// real model calls).
type warnIntegLobe struct {
	ws    *Workspace
	calls atomic.Int64
}

func (l *warnIntegLobe) ID() string          { return "warn" }
func (l *warnIntegLobe) Description() string { return "integration: LLM warn lobe" }
func (l *warnIntegLobe) Kind() LobeKind      { return KindLLM }
func (l *warnIntegLobe) Run(ctx context.Context, in LobeInput) error {
	l.calls.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if l.ws == nil {
		return nil
	}
	return l.ws.Publish(Note{
		LobeID:   "warn",
		Severity: SevWarning,
		Title:    "warn",
	})
}

// critIntegLobe is the LLM-classed Lobe used by TestCortexIntegration.
// Same Kind=KindLLM contract as warnIntegLobe; publishes a SevCritical
// Note that PreEndTurnGate must surface as a blocker.
type critIntegLobe struct {
	ws    *Workspace
	calls atomic.Int64
}

func (l *critIntegLobe) ID() string          { return "crit" }
func (l *critIntegLobe) Description() string { return "integration: LLM crit lobe" }
func (l *critIntegLobe) Kind() LobeKind      { return KindLLM }
func (l *critIntegLobe) Run(ctx context.Context, in LobeInput) error {
	l.calls.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if l.ws == nil {
		return nil
	}
	return l.ws.Publish(Note{
		LobeID:   "crit",
		Severity: SevCritical,
		Title:    "crit",
	})
}

// TestCortexIntegration is the end-to-end exercise demanded by spec
// item 25. It wires three fake Lobes (deterministic Info, LLM Warning,
// LLM Critical) into a real Cortex, drives one round via MidturnNote,
// and asserts the formatted block contains all three LobeIDs in
// Critical→Warning→Info order. It then exercises PreEndTurnGate's
// "block on unresolved Critical / clear after resolution" contract by
// publishing a follow-on Note with Resolves=critID. The whole test
// runs under -race with t.Parallel() enabled.
//
// The fake Workspace handles for each lobe are wired AFTER c.New so
// every Note Published during the round lands in the cortex's own
// Workspace (which is what MidturnNote drains). The pattern matches
// TestMidturnNoteFormat.
func TestCortexIntegration(t *testing.T) {
	t.Parallel()

	bus := hub.New()

	echo := &echoIntegLobe{}
	warn := &warnIntegLobe{}
	crit := &critIntegLobe{}

	c, err := New(Config{
		SessionID:       "integ-session",
		EventBus:        bus,
		Provider:        &startStopProvider{},
		Lobes:           []Lobe{echo, warn, crit},
		PreWarmInterval: time.Hour, // suppress pump churn during test
		RoundDeadline:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Inject the cortex's writable Workspace into each lobe so Publish
	// stamps the same round id MidturnNote sets via SetRound.
	echo.ws = c.workspace
	warn.ws = c.workspace
	crit.ws = c.workspace

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	// Drive one round.
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "hi"}}},
	}
	out := c.MidturnNote(msgs, 0)
	if out == "" {
		t.Fatal("MidturnNote returned empty; expected formatted block with all three lobes")
	}

	// All three LobeIDs must be present.
	for _, id := range []string{"echo", "warn", "crit"} {
		if !strings.Contains(out, id) {
			t.Errorf("MidturnNote missing LobeID %q in output:\n%s", id, out)
		}
	}

	// Severity ordering: Critical → Warning → Info. Index of "crit"
	// must precede "warn" must precede "echo" in the rendered block.
	critIdx := strings.Index(out, "crit")
	warnIdx := strings.Index(out, "warn")
	echoIdx := strings.Index(out, "echo")
	if critIdx < 0 || warnIdx < 0 || echoIdx < 0 {
		t.Fatalf("MidturnNote missing one of crit/warn/echo: %q", out)
	}
	if !(critIdx < warnIdx && warnIdx < echoIdx) {
		t.Errorf("severity order wrong: crit=%d warn=%d echo=%d\nfull output:\n%s",
			critIdx, warnIdx, echoIdx, out)
	}

	// PreEndTurnGate must block — critIntegLobe published an unresolved
	// SevCritical Note in the round above.
	gate := c.PreEndTurnGate(msgs)
	if gate == "" {
		t.Fatal("PreEndTurnGate should block on unresolved Critical, got empty")
	}
	if !strings.Contains(gate, "crit") {
		t.Errorf("PreEndTurnGate output should mention LobeID \"crit\": %q", gate)
	}

	// Locate the critical Note's ID in the workspace so we can publish
	// a resolving follow-on. Workspace.Publish takes Note by value and
	// assigns the ID internally — same recovery pattern as
	// TestPreEndTurnGateBlocks.
	snap := c.Workspace().Snapshot()
	var critID string
	for _, n := range snap {
		if n.Severity == SevCritical {
			critID = n.ID
			break
		}
	}
	if critID == "" {
		t.Fatalf("could not find SevCritical note in workspace snapshot (%d notes)", len(snap))
	}

	// Publish a resolving Note manually via the workspace.
	if err := c.Workspace().Publish(Note{
		LobeID:   "test-resolver",
		Title:    "resolves crit",
		Severity: SevInfo,
		Resolves: critID,
	}); err != nil {
		t.Fatalf("Publish resolver: %v", err)
	}

	// PreEndTurnGate must now be empty: the only critical Note has
	// been resolved.
	gate2 := c.PreEndTurnGate(msgs)
	if gate2 != "" {
		t.Fatalf("PreEndTurnGate after resolve = %q, want empty", gate2)
	}
}
