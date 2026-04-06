package hub

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAuditLogAppendAndVerify(t *testing.T) {
	log := NewChainedAuditLog()

	log.Append(ChainedAuditEntry{
		Timestamp: time.Now(),
		EventType: EventToolPreUse,
		Action:    "allow",
		RuleID:    "rule-1",
		Details:   "first entry",
	})
	log.Append(ChainedAuditEntry{
		Timestamp: time.Now(),
		EventType: EventToolFileWrite,
		Action:    "deny",
		RuleID:    "rule-2",
		Details:   "blocked write",
	})
	log.Append(ChainedAuditEntry{
		Timestamp: time.Now(),
		EventType: EventGitPreCommit,
		Action:    "allow",
		RuleID:    "rule-3",
	})

	entries := log.Entries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Verify sequence numbers
	for i, e := range entries {
		if e.Sequence != uint64(i) {
			t.Fatalf("entry %d: expected seq %d, got %d", i, i, e.Sequence)
		}
	}

	// Verify chain linkage
	if entries[0].PrevHash != "" {
		t.Fatal("first entry should have empty PrevHash")
	}
	if entries[1].PrevHash != entries[0].Hash {
		t.Fatal("second entry PrevHash should equal first entry Hash")
	}
	if entries[2].PrevHash != entries[1].Hash {
		t.Fatal("third entry PrevHash should equal second entry Hash")
	}

	// Verify chain integrity
	if err := log.Verify(); err != nil {
		t.Fatalf("Verify failed on valid chain: %v", err)
	}
}

func TestAuditLogTamperDetection(t *testing.T) {
	log := NewChainedAuditLog()

	log.Append(ChainedAuditEntry{
		Timestamp: time.Now(),
		EventType: EventToolPreUse,
		Action:    "allow",
		RuleID:    "rule-1",
	})
	log.Append(ChainedAuditEntry{
		Timestamp: time.Now(),
		EventType: EventToolFileWrite,
		Action:    "deny",
		RuleID:    "rule-2",
	})
	log.Append(ChainedAuditEntry{
		Timestamp: time.Now(),
		EventType: EventGitPreCommit,
		Action:    "allow",
		RuleID:    "rule-3",
	})

	// Verify intact chain first
	if err := log.Verify(); err != nil {
		t.Fatalf("Verify failed before tamper: %v", err)
	}

	// Tamper with the second entry's action
	log.mu.Lock()
	log.entries[1].Action = "allow" // was "deny"
	log.mu.Unlock()

	err := log.Verify()
	if err == nil {
		t.Fatal("expected Verify to detect tampering")
	}
	t.Logf("tamper detected: %v", err)

	// Test tampering with hash directly
	log2 := NewChainedAuditLog()
	log2.Append(ChainedAuditEntry{
		Timestamp: time.Now(),
		EventType: EventToolPreUse,
		Action:    "allow",
		RuleID:    "rule-1",
	})
	log2.Append(ChainedAuditEntry{
		Timestamp: time.Now(),
		EventType: EventToolFileWrite,
		Action:    "deny",
		RuleID:    "rule-2",
	})

	// Tamper with first entry's hash (breaks chain for second entry)
	log2.mu.Lock()
	log2.entries[0].Hash = "tampered-hash"
	log2.mu.Unlock()

	err = log2.Verify()
	if err == nil {
		t.Fatal("expected Verify to detect hash tampering")
	}
	t.Logf("hash tamper detected: %v", err)
}

func TestAuditLogSince(t *testing.T) {
	log := NewChainedAuditLog()

	for i := 0; i < 5; i++ {
		log.Append(ChainedAuditEntry{
			Timestamp: time.Now(),
			EventType: EventToolPreUse,
			Action:    "allow",
			RuleID:    "rule",
		})
	}

	// Since seq 2 should return entries 3, 4
	entries := log.Since(2)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after seq 2, got %d", len(entries))
	}
	if entries[0].Sequence != 3 {
		t.Fatalf("expected first entry seq 3, got %d", entries[0].Sequence)
	}
	if entries[1].Sequence != 4 {
		t.Fatalf("expected second entry seq 4, got %d", entries[1].Sequence)
	}

	// Since last seq should return nothing
	entries = log.Since(4)
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after last seq, got %d", len(entries))
	}

	// Since 0 should return entries 1..4
	entries = log.Since(0)
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries after seq 0, got %d", len(entries))
	}
}

func TestGateStrictFailClosed(t *testing.T) {
	b := New()
	b.Register(Subscriber{
		ID:     "strict-panicker",
		Events: []EventType{EventToolBashExec},
		Mode:   ModeGateStrict,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			panic("strict boom")
		},
	})

	resp := b.Emit(context.Background(), &Event{Type: EventToolBashExec})
	if resp.Decision != Deny {
		t.Fatalf("expected Deny after panic in strict gate, got %s", resp.Decision)
	}
	if resp.Reason == "" {
		t.Fatal("expected a reason on deny")
	}
}

func TestGateStrictDenyOnError(t *testing.T) {
	b := New()

	// A strict gate that returns an error-like deny
	b.Register(Subscriber{
		ID:     "strict-denier",
		Events: []EventType{EventToolFileWrite},
		Mode:   ModeGateStrict,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			err := errors.New("policy violation")
			return &HookResponse{Decision: Deny, Reason: err.Error()}
		},
	})

	resp := b.Emit(context.Background(), &Event{Type: EventToolFileWrite})
	if resp.Decision != Deny {
		t.Fatalf("expected Deny, got %s", resp.Decision)
	}
	if resp.Reason != "policy violation" {
		t.Fatalf("expected reason 'policy violation', got %q", resp.Reason)
	}
}

func TestGateStrictTimeoutDenies(t *testing.T) {
	b := New()
	b.Register(Subscriber{
		ID:     "strict-slow",
		Events: []EventType{EventToolBashExec},
		Mode:   ModeGateStrict,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			// Block longer than the 5s timeout - use context to detect
			<-ctx.Done()
			return &HookResponse{Decision: Allow}
		},
	})

	// Use a context with a short deadline to speed up the test
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp := b.Emit(ctx, &Event{Type: EventToolBashExec})
	if resp.Decision != Deny {
		t.Fatalf("expected Deny on timeout for strict gate, got %s", resp.Decision)
	}
}

func TestGateAdvisoryFailOpen(t *testing.T) {
	b := New()
	b.Register(Subscriber{
		ID:     "advisory-panicker",
		Events: []EventType{EventToolBashExec},
		Mode:   ModeGate,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			panic("advisory boom")
		},
	})

	resp := b.Emit(context.Background(), &Event{Type: EventToolBashExec})
	// Advisory gate: panic -> Abstain (not Deny), so final result is Allow
	if resp.Decision != Allow {
		t.Fatalf("expected Allow after panic in advisory gate, got %s", resp.Decision)
	}
}

func TestChainedAuditWiredIntoBus(t *testing.T) {
	b := New()
	b.ChainedAudit = NewChainedAuditLog()

	b.Register(Subscriber{
		ID:     "gate-allow",
		Events: []EventType{EventToolPreUse},
		Mode:   ModeGate,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			return &HookResponse{Decision: Allow}
		},
	})
	b.Register(Subscriber{
		ID:     "gate-deny",
		Events: []EventType{EventToolFileWrite},
		Mode:   ModeGateStrict,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			return &HookResponse{Decision: Deny, Reason: "blocked"}
		},
	})

	b.Emit(context.Background(), &Event{Type: EventToolPreUse})
	b.Emit(context.Background(), &Event{Type: EventToolFileWrite})

	entries := b.ChainedAudit.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 chained audit entries, got %d", len(entries))
	}
	if entries[0].Action != "allow" {
		t.Fatalf("expected first action 'allow', got %q", entries[0].Action)
	}
	if entries[1].Action != "deny" {
		t.Fatalf("expected second action 'deny', got %q", entries[1].Action)
	}

	// Verify chain integrity
	if err := b.ChainedAudit.Verify(); err != nil {
		t.Fatalf("chained audit verify failed: %v", err)
	}
}
