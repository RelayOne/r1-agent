package trust

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type LedgerWriter interface {
	Append(context.Context, []byte, []LedgerEntry) ([]string, error)
}

type Dispatcher struct {
	root   *TrustRoot
	replay ReplayStore
	deps   Dependencies
	writer LedgerWriter
	kinds  map[SignalKind]KindHandler
}

func NewDispatcher(root *TrustRoot, replay ReplayStore, deps Dependencies, writer LedgerWriter) *Dispatcher {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Dispatcher{
		root:   root,
		replay: replay,
		deps:   deps,
		writer: writer,
		kinds:  make(map[SignalKind]KindHandler),
	}
}

func (d *Dispatcher) Register(h KindHandler) {
	d.kinds[h.Kind()] = h
}

func (d *Dispatcher) Process(ctx context.Context, frame *SignalFrame) error {
	verdict, err := Verify(frame, d.root, d.replay, d.deps.Now())
	if verdict != VerdictApplied {
		return d.append(ctx, frame, verdict, errString(err), nil)
	}
	handler, ok := d.kinds[frame.Kind]
	if !ok {
		return d.append(ctx, frame, VerdictRejectedUnknown, "no handler registered", nil)
	}
	result, handleErr := handler.Handle(ctx, frame, d.deps)
	if handleErr != nil {
		return d.append(ctx, frame, VerdictRejectedMalformed, handleErr.Error(), nil)
	}
	return d.append(ctx, frame, result.Verdict, result.Notes, result.LedgerEntries)
}

func (d *Dispatcher) append(ctx context.Context, frame *SignalFrame, verdict Verdict, notes string, entries []LedgerEntry) error {
	if d.writer == nil {
		return nil
	}
	body, err := json.Marshal(map[string]any{
		"node_type":      "trust_signal",
		"schema_version": 1,
		"issuer_hub_id":  frame.IssuerHubID,
		"kind":           frame.Kind,
		"nonce":          frame.Nonce,
		"reason":         frame.Reason,
		"issued_at":      frame.IssuedAt,
		"received_at":    d.deps.Now(),
		"verdict":        verdict,
		"notes":          notes,
	})
	if err != nil {
		return fmt.Errorf("trust: marshal ledger node: %w", err)
	}
	_, err = d.writer.Append(ctx, body, entries)
	return err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
