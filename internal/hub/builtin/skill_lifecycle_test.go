package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
)

func TestEmitSkillUnloaded_AppendsNode(t *testing.T) {
	tmp := t.TempDir()
	led, err := ledger.New(filepath.Join(tmp, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	id, err := EmitSkillUnloaded(context.Background(), led, &nodes.SkillUnloaded{
		SkillRef:          "alpha",
		LoadRef:           "load-1",
		StanceID:          "st-1",
		StanceRole:        "cto",
		Reason:            "compactor_evicted",
		BudgetTokensFreed: 1500,
		CreatedAt:         time.Now().UTC(),
		Version:           1,
	})
	if err != nil {
		t.Fatalf("EmitSkillUnloaded: %v", err)
	}
	if id == "" {
		t.Fatal("EmitSkillUnloaded returned empty NodeID")
	}
	chainPath := filepath.Join(tmp, "ledger", "chain", string(id)+".json")
	if _, err := os.Stat(chainPath); err != nil {
		t.Errorf("chain tier file missing: %v", err)
	}
}

func TestEmitSkillUnloaded_NilLedgerIsNoOp(t *testing.T) {
	id, err := EmitSkillUnloaded(context.Background(), nil, &nodes.SkillUnloaded{
		SkillRef: "alpha", Reason: "scope_exit",
	})
	if err != nil {
		t.Errorf("nil ledger should be no-op without error: %v", err)
	}
	if id != "" {
		t.Errorf("nil ledger should return empty NodeID, got %q", id)
	}
}

func TestEmitSkillUnloaded_RejectsInvalid(t *testing.T) {
	tmp := t.TempDir()
	led, err := ledger.New(filepath.Join(tmp, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	cases := []*nodes.SkillUnloaded{
		nil,
		{}, // every required field empty
		{SkillRef: "x", Reason: "invalid_reason"},
	}
	for i, n := range cases {
		_, err := EmitSkillUnloaded(context.Background(), led, n)
		if i == 0 {
			if err != nil {
				t.Errorf("nil node should be no-op without error: %v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("case %d: expected validation error, got nil", i)
		}
	}
}

func TestEmitSkillUnloaded_RoundtripPayload(t *testing.T) {
	tmp := t.TempDir()
	led, err := ledger.New(filepath.Join(tmp, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	want := &nodes.SkillUnloaded{
		SkillRef:   "beta",
		LoadRef:    "load-2",
		StanceID:   "st-2",
		StanceRole: "dev",
		Reason:     "scope_exit",
		CreatedAt:  time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		Version:    1,
	}
	id, err := EmitSkillUnloaded(context.Background(), led, want)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	// Read back via the content tier (chain holds metadata only).
	contentPath := filepath.Join(tmp, "ledger", "content", string(id)+".json")
	raw, err := os.ReadFile(contentPath)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	var wrap struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		t.Fatalf("unmarshal wrap: %v", err)
	}
	body := wrap.Content
	if len(body) == 0 {
		body = raw
	}
	var got nodes.SkillUnloaded
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal SkillUnloaded: %v", err)
	}
	if got.SkillRef != want.SkillRef || got.Reason != want.Reason || got.StanceRole != want.StanceRole {
		t.Errorf("roundtrip mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}
