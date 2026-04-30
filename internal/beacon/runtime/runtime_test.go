package runtime

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/beacon/trust"
	"github.com/RelayOne/r1/internal/operator"
	"github.com/RelayOne/r1/internal/sessionctl"
)

type fakeOperator struct {
	choice  string
	message string
}

func (f *fakeOperator) Ask(ctx context.Context, prompt string, options []operator.Option) (string, error) {
	f.message = prompt
	return f.choice, nil
}
func (f *fakeOperator) Notify(kind operator.NotifyKind, message string) { f.message = message }

type fakeTools struct {
	rotated, resumed bool
	revoked          string
}

func (f *fakeTools) RotateSessionKey(context.Context) error { f.rotated = true; return nil }
func (f *fakeTools) Resume(context.Context) error           { f.resumed = true; return nil }
func (f *fakeTools) RevokeToken(_ context.Context, tokenID string) error {
	f.revoked = tokenID
	return nil
}

type fakeSignaler struct{ paused bool }

func (f *fakeSignaler) Pause(int) error  { f.paused = true; return nil }
func (f *fakeSignaler) Resume(int) error { return nil }

func TestRuntimeProcessesAskExec(t *testing.T) {
	t.Parallel()
	root := trust.NewTrustRoot()
	pub, priv, _ := ed25519.GenerateKey(nil)
	root.Pin("hub-1", pub)
	router := sessionctl.NewApprovalRouter()
	op := &fakeOperator{choice: "approve"}
	tools := &fakeTools{}
	rt, err := New(Config{
		Root:           root,
		Router:         router,
		Operator:       op,
		Tools:          tools,
		Signaler:       &fakeSignaler{},
		PGID:           123,
		CurrentTime:    func() time.Time { return time.Unix(1700000000, 0).UTC() },
		OfflineTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	payload, _ := json.Marshal(trust.AskExecPayload{
		Tool:       "rotate_session_key",
		UserPrompt: "rotate now?",
	})
	frame := &trust.SignalFrame{
		Version:     1,
		Nonce:       []byte("nonce-1"),
		IssuerHubID: "hub-1",
		Kind:        trust.KindAskExec,
		IssuedAt:    time.Unix(1700000000, 0).UTC(),
		Expiry:      time.Unix(1700000060, 0).UTC(),
		Reason:      "rotate",
		Payload:     payload,
	}
	if err := frame.Sign(priv); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if err := rt.Process(context.Background(), frame); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if !tools.rotated {
		t.Fatal("expected rotate_session_key tool to run")
	}
}
