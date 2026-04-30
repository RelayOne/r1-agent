package kinds

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/beacon/trust"
)

type DisplayToUser struct{}

func (DisplayToUser) Kind() trust.SignalKind { return trust.KindDisplayToUser }
func (DisplayToUser) Handle(ctx context.Context, frame *trust.SignalFrame, deps trust.Dependencies) (trust.HandlerResult, error) {
	var payload trust.DisplayToUserPayload
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return trust.HandlerResult{}, err
	}
	if err := payload.Validate(); err != nil {
		return trust.HandlerResult{}, err
	}
	if deps.UserPrompter != nil {
		_, _ = deps.UserPrompter.Prompt(ctx, trust.UserPromptRequest{Title: payload.Title, Body: payload.Body})
	}
	return trust.HandlerResult{Verdict: trust.VerdictApplied, Notes: payload.Title}, nil
}

type AskExec struct{}

func (AskExec) Kind() trust.SignalKind { return trust.KindAskExec }
func (AskExec) Handle(ctx context.Context, frame *trust.SignalFrame, deps trust.Dependencies) (trust.HandlerResult, error) {
	var payload trust.AskExecPayload
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return trust.HandlerResult{}, err
	}
	if err := payload.Validate(); err != nil {
		return trust.HandlerResult{}, err
	}
	if deps.UserPrompter == nil || deps.ToolDispatcher == nil {
		return trust.HandlerResult{Verdict: trust.VerdictUserDeclined, Notes: "operator or tool dispatcher unavailable"}, nil
	}
	resp, err := deps.UserPrompter.Prompt(ctx, trust.UserPromptRequest{
		Title:   frame.Reason,
		Body:    payload.UserPrompt,
		Choices: []string{"approve", "decline"},
	})
	if err != nil || resp.Choice != "approve" {
		return trust.HandlerResult{Verdict: trust.VerdictUserDeclined, Notes: "operator declined"}, nil
	}
	result, err := deps.ToolDispatcher.Dispatch(ctx, payload.Tool, payload.Args)
	if err != nil {
		return trust.HandlerResult{Verdict: trust.VerdictApplied, Notes: err.Error()}, nil
	}
	derived := make([]trust.LedgerEntry, 0, len(result.LedgerNodeIDs))
	for _, nodeID := range result.LedgerNodeIDs {
		derived = append(derived, trust.LedgerEntry{
			NodeType:  "tool_call_ref",
			Marshaled: []byte(fmt.Sprintf(`{"ref":%q}`, nodeID)),
		})
	}
	return trust.HandlerResult{Verdict: trust.VerdictApplied, Notes: "tool executed", LedgerEntries: derived}, nil
}

type Pause struct{}

func (Pause) Kind() trust.SignalKind { return trust.KindPause }
func (Pause) Handle(ctx context.Context, frame *trust.SignalFrame, deps trust.Dependencies) (trust.HandlerResult, error) {
	var payload trust.PausePayload
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return trust.HandlerResult{}, err
	}
	if err := payload.Validate(); err != nil {
		return trust.HandlerResult{}, err
	}
	if deps.SessionController == nil {
		return trust.HandlerResult{}, fmt.Errorf("pause: no session controller configured")
	}
	if err := deps.SessionController.Pause(ctx, payload.Reason); err != nil {
		return trust.HandlerResult{}, err
	}
	return trust.HandlerResult{Verdict: trust.VerdictApplied, Notes: "session paused"}, nil
}

type RotateSessionKey struct{}

func (RotateSessionKey) Kind() trust.SignalKind { return trust.KindRotateSessionKey }
func (RotateSessionKey) Handle(ctx context.Context, frame *trust.SignalFrame, deps trust.Dependencies) (trust.HandlerResult, error) {
	if deps.SessionController == nil {
		return trust.HandlerResult{}, fmt.Errorf("rotate_session_key: no session controller configured")
	}
	if err := deps.SessionController.RotateSessionKey(ctx); err != nil {
		return trust.HandlerResult{}, err
	}
	return trust.HandlerResult{Verdict: trust.VerdictApplied, Notes: "session key rotated"}, nil
}

type ForceResurgence struct{}

func (ForceResurgence) Kind() trust.SignalKind { return trust.KindForceResurgence }
func (ForceResurgence) Handle(ctx context.Context, frame *trust.SignalFrame, deps trust.Dependencies) (trust.HandlerResult, error) {
	if deps.SessionController == nil {
		return trust.HandlerResult{}, fmt.Errorf("force_resurgence: no session controller configured")
	}
	if err := deps.SessionController.ForceResurgence(ctx, frame.Reason); err != nil {
		return trust.HandlerResult{}, err
	}
	return trust.HandlerResult{Verdict: trust.VerdictApplied, Notes: "resurgence forced"}, nil
}

type AttestState struct{}

func (AttestState) Kind() trust.SignalKind { return trust.KindAttestState }
func (AttestState) Handle(ctx context.Context, frame *trust.SignalFrame, deps trust.Dependencies) (trust.HandlerResult, error) {
	if deps.Attestor == nil {
		return trust.HandlerResult{}, fmt.Errorf("attest_state: no attestor configured")
	}
	attestation, err := deps.Attestor.Attest(ctx)
	if err != nil {
		return trust.HandlerResult{}, err
	}
	body, err := json.Marshal(map[string]any{
		"build_hash":                attestation.BuildHash,
		"constitution_hash":         attestation.ConstitutionHash,
		"ledger_root_hash":          attestation.LedgerRootHash,
		"active_token_fingerprints": attestation.ActiveTokenFingerprints,
		"platform":                  attestation.Platform,
		"beacon_version":            attestation.BeaconVersion,
		"attested_at":               attestation.AttestedAt,
	})
	if err != nil {
		return trust.HandlerResult{}, err
	}
	return trust.HandlerResult{
		Verdict: trust.VerdictApplied,
		Notes:   "attestation produced",
		LedgerEntries: []trust.LedgerEntry{{
			NodeType:  "device_attestation",
			Marshaled: body,
		}},
	}, nil
}

type OfflineReview struct{}

func (OfflineReview) Kind() trust.SignalKind { return trust.KindOfflineReview }
func (OfflineReview) Handle(ctx context.Context, frame *trust.SignalFrame, deps trust.Dependencies) (trust.HandlerResult, error) {
	if deps.UserPrompter == nil {
		return trust.HandlerResult{Verdict: trust.VerdictApplied, Notes: "offline review requested"}, nil
	}
	resp, err := deps.UserPrompter.Prompt(ctx, trust.UserPromptRequest{
		Title:   "Offline review requested",
		Body:    frame.Reason,
		Choices: []string{"reviewed", "defer"},
	})
	if err != nil {
		return trust.HandlerResult{Verdict: trust.VerdictApplied, Notes: "offline review pending"}, nil
	}
	return trust.HandlerResult{Verdict: trust.VerdictApplied, Notes: "offline review: " + resp.Choice}, nil
}

func RegisterAll(dispatcher *trust.Dispatcher) {
	dispatcher.Register(DisplayToUser{})
	dispatcher.Register(AskExec{})
	dispatcher.Register(Pause{})
	dispatcher.Register(RotateSessionKey{})
	dispatcher.Register(ForceResurgence{})
	dispatcher.Register(AttestState{})
	dispatcher.Register(OfflineReview{})
}
