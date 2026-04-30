package trust_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/beacon/trust"
	"github.com/RelayOne/r1/internal/beacon/trust/kinds"
)

type promptStub struct{ choice string }

func (p promptStub) Prompt(context.Context, trust.UserPromptRequest) (trust.UserPromptResponse, error) {
	return trust.UserPromptResponse{Choice: p.choice}, nil
}

type toolStub struct{}

func (toolStub) Dispatch(context.Context, string, json.RawMessage) (trust.ToolResult, error) {
	return trust.ToolResult{OK: true, LedgerNodeIDs: []string{"node-1"}}, nil
}

type sessionStub struct{ paused, rotated, resurged int }

func (s *sessionStub) Pause(context.Context, string) error           { s.paused++; return nil }
func (s *sessionStub) RotateSessionKey(context.Context) error        { s.rotated++; return nil }
func (s *sessionStub) ForceResurgence(context.Context, string) error { s.resurged++; return nil }

type attestorStub struct{}

func (attestorStub) Attest(context.Context) (trust.Attestation, error) {
	return trust.Attestation{BuildHash: "abc", AttestedAt: time.Now().UTC()}, nil
}

type ledgerStub struct {
	signal  []byte
	entries []trust.LedgerEntry
}

func (l *ledgerStub) Append(_ context.Context, signal []byte, entries []trust.LedgerEntry) ([]string, error) {
	l.signal = append([]byte(nil), signal...)
	l.entries = append([]trust.LedgerEntry(nil), entries...)
	return []string{"trust-1"}, nil
}

func signedFrame(t *testing.T, kind trust.SignalKind, payload any, issuedAt time.Time) (*trust.SignalFrame, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal payload: %v", err)
	}
	frame := &trust.SignalFrame{
		Version:     1,
		Nonce:       []byte("nonce-" + string(kind)),
		IssuerHubID: "hub-1",
		Kind:        kind,
		IssuedAt:    issuedAt,
		Expiry:      issuedAt.Add(time.Minute),
		Reason:      "rotate now",
		Payload:     raw,
	}
	if err := frame.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return frame, pub
}

func TestVerifyRejectsSignatureExpiryAndReplay(t *testing.T) {
	now := time.Now().UTC()
	frame, pub := signedFrame(t, trust.KindDisplayToUser, trust.DisplayToUserPayload{Title: "hello", Body: "world"}, now)
	root := trust.NewTrustRoot()
	root.Pin("hub-1", pub)
	replay := trust.NewMemoryReplayStore()
	if verdict, err := trust.Verify(frame, root, replay, now); verdict != trust.VerdictApplied || err != nil {
		t.Fatalf("Verify applied verdict=%s err=%v", verdict, err)
	}
	if verdict, err := trust.Verify(frame, root, replay, now); verdict != trust.VerdictRejectedReplay || err != trust.ErrReplaySignal {
		t.Fatalf("Verify replay verdict=%s err=%v", verdict, err)
	}
	expiredFrame, pub2 := signedFrame(t, trust.KindDisplayToUser, trust.DisplayToUserPayload{Title: "hello", Body: "world"}, now.Add(-2*time.Minute))
	root.Pin("hub-1", pub2)
	if verdict, err := trust.Verify(expiredFrame, root, trust.NewMemoryReplayStore(), now); verdict != trust.VerdictRejectedExpired || err != trust.ErrExpiredSignal {
		t.Fatalf("Verify expired verdict=%s err=%v", verdict, err)
	}
	frame.Signature[0] ^= 0xFF
	root.Pin("hub-1", pub)
	if verdict, err := trust.Verify(frame, root, trust.NewMemoryReplayStore(), now); verdict != trust.VerdictRejectedSignature || err != trust.ErrUnsignedSignal {
		t.Fatalf("Verify signature verdict=%s err=%v", verdict, err)
	}
}

func TestDispatcherProcessesAskExecAndAttestation(t *testing.T) {
	now := time.Now().UTC()
	ledger := &ledgerStub{}
	frame, pub := signedFrame(t, trust.KindAskExec, trust.AskExecPayload{Tool: "rotate_session_key", UserPrompt: "rotate?"}, now)
	root := trust.NewTrustRoot()
	root.Pin("hub-1", pub)
	dispatcher := trust.NewDispatcher(root, trust.NewMemoryReplayStore(), trust.Dependencies{
		UserPrompter:      promptStub{choice: "approve"},
		ToolDispatcher:    toolStub{},
		SessionController: &sessionStub{},
		Attestor:          attestorStub{},
		Now:               func() time.Time { return now },
	}, ledger)
	kinds.RegisterAll(dispatcher)
	if err := dispatcher.Process(context.Background(), frame); err != nil {
		t.Fatalf("Process ask exec: %v", err)
	}
	if len(ledger.entries) != 1 {
		t.Fatalf("expected derived ledger entry, got %d", len(ledger.entries))
	}
	ledger.entries = nil
	attestationFrame, pub2 := signedFrame(t, trust.KindAttestState, map[string]any{}, now.Add(time.Second))
	root.Pin("hub-1", pub2)
	if err := dispatcher.Process(context.Background(), attestationFrame); err != nil {
		t.Fatalf("Process attest state: %v", err)
	}
	if len(ledger.entries) != 1 || ledger.entries[0].NodeType != "device_attestation" {
		t.Fatalf("expected device_attestation entry, got %+v", ledger.entries)
	}
}
