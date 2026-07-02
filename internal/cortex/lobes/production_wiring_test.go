// production_wiring_test.go — regression test for audit A010 at the
// production lobe-list seam.
//
// Mirrors the exact construction internal/engine/native_runner.go's
// buildDeterministicCortex performs (memoryrecall, walkeeper with nil
// durable bus, rulecheck with nil durable bus, antitrunc with empty
// plan/spec paths, shared Workspace passed via cortex.Config.Workspace)
// and proves that with the A010 wiring:
//
//  1. MidturnNote hands the real conversation History to the Lobes, so
//     the antitrunc Lobe detects a truncation phrase in assistant text
//     and its critical Note lands in the returned [CORTEX NOTES] block.
//  2. MidturnNote returns well under the RoundDeadline even though the
//     daemon-style walkeeper/rulecheck Lobes never return from Run —
//     they are excluded from the Round barrier, so the per-turn ~2s
//     stall the audit measured is gone.
package lobesintegration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/cortex"
	antitrunclobe "github.com/RelayOne/r1/internal/cortex/lobes/antitrunc"
	"github.com/RelayOne/r1/internal/cortex/lobes/memoryrecall"
	"github.com/RelayOne/r1/internal/cortex/lobes/rulecheck"
	"github.com/RelayOne/r1/internal/cortex/lobes/walkeeper"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/wisdom"
)

func TestProductionLobeList_MidturnNoteDetectsTruncationWithoutBarrierStall(t *testing.T) {
	t.Parallel()

	eventBus := hub.New()
	ws := cortex.NewWorkspace(eventBus, nil)

	memStore, err := memory.NewStore(memory.Config{})
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	wisStore := wisdom.NewStore()

	// Exactly the lobe list buildDeterministicCortex wires in the
	// native runner: nil durable bus for walkeeper/rulecheck, empty
	// plan/spec paths for antitrunc.
	lobeList := []cortex.Lobe{
		memoryrecall.NewMemoryRecallLobe(ws, memStore, wisStore, eventBus),
		walkeeper.NewWALKeeperLobe(eventBus, nil, ws, walkeeper.WALFraming{}),
		rulecheck.NewRuleCheckLobe(nil, ws),
		antitrunclobe.NewAntiTruncLobe(ws, "", ""),
	}

	live, err := cortex.New(cortex.Config{
		SessionID:       "prod-wiring-test",
		EventBus:        eventBus,
		Provider:        newFakeProvider(10),
		Lobes:           lobeList,
		Workspace:       ws,
		PreWarmInterval: time.Hour,
		RoundDeadline:   30 * time.Second,
	})
	if err != nil {
		t.Fatalf("cortex.New: %v", err)
	}
	if err := live.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = live.Stop(context.Background()) }()

	history := []agentloop.Message{
		{
			Role: "user",
			Content: []agentloop.ContentBlock{
				{Type: "text", Text: "finish every task in the plan"},
			},
		},
		{
			Role: "assistant",
			Content: []agentloop.ContentBlock{
				{Type: "text", Text: "I'll stop here and hand off the remaining work."},
			},
		},
	}

	start := time.Now()
	out := live.MidturnNote(history, 1)
	elapsed := time.Since(start)

	// Barrier regression guard: with walkeeper/rulecheck excluded from
	// the Round barrier the round completes as soon as memoryrecall +
	// antitrunc return — milliseconds, not the 30s RoundDeadline. 5s
	// absorbs CI jitter while failing decisively on a barrier stall.
	if elapsed >= 5*time.Second {
		t.Fatalf("MidturnNote took %v with the production lobe list; daemon lobes are stalling the Round barrier (A010 regression)", elapsed)
	}

	// History regression guard: the antitrunc Lobe only sees assistant
	// text through LobeInput.History; before A010 that was always nil
	// and no production Lobe could ever publish a Note.
	if out == "" {
		t.Fatalf("MidturnNote returned empty; expected antitrunc critical Note from the truncation phrase in History")
	}
	if !strings.Contains(out, "antitrunc") {
		t.Errorf("MidturnNote output missing antitrunc LobeID: %q", out)
	}
	if !strings.Contains(out, "[ANTI-TRUNCATION]") {
		t.Errorf("MidturnNote output missing [ANTI-TRUNCATION] title: %q", out)
	}
	if !strings.Contains(out, string(cortex.SevCritical)) {
		t.Errorf("MidturnNote output missing critical severity tag: %q", out)
	}

	// PreEndTurnGate must now also fire on the unresolved critical Note.
	if gate := live.PreEndTurnGate(history); gate == "" {
		t.Errorf("PreEndTurnGate returned empty; want refusal block for the unresolved antitrunc critical Note")
	}
}
