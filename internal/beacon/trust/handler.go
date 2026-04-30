package trust

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

type SignalKind string

const (
	KindDisplayToUser    SignalKind = "display_to_user"
	KindAskExec          SignalKind = "ask_user_and_execute_on_approve"
	KindPause            SignalKind = "pause"
	KindRotateSessionKey SignalKind = "rotate_session_key"
	KindForceResurgence  SignalKind = "force_resurgence"
	KindAttestState      SignalKind = "attest_state"
	KindOfflineReview    SignalKind = "request_offline_review"
)

var validKinds = map[SignalKind]bool{
	KindDisplayToUser:    true,
	KindAskExec:          true,
	KindPause:            true,
	KindRotateSessionKey: true,
	KindForceResurgence:  true,
	KindAttestState:      true,
	KindOfflineReview:    true,
}

var AllowedAskExecTools = map[string]bool{
	"update_self":        true,
	"rotate_session_key": true,
	"pause":              true,
	"resume":             true,
	"revoke_token":       true,
	"attest_state":       true,
}

var (
	ErrUnsignedSignal  = errors.New("trust: signal lacks valid signature")
	ErrExpiredSignal   = errors.New("trust: signal expired")
	ErrReplaySignal    = errors.New("trust: replayed signal nonce")
	ErrUnknownKind     = errors.New("trust: unknown signal kind")
	ErrUntrustedIssuer = errors.New("trust: issuer is not pinned")
)

const ClockSkewTolerance = 30 * time.Second

type Verdict string

const (
	VerdictApplied           Verdict = "applied"
	VerdictUserDeclined      Verdict = "user_declined"
	VerdictRejectedSignature Verdict = "rejected_signature"
	VerdictRejectedExpired   Verdict = "rejected_expired"
	VerdictRejectedReplay    Verdict = "rejected_replay"
	VerdictRejectedUnknown   Verdict = "rejected_unknown_kind"
	VerdictRejectedUntrusted Verdict = "rejected_untrusted_issuer"
	VerdictRejectedMalformed Verdict = "rejected_malformed"
)

type SignalFrame struct {
	Version     int             `json:"v"`
	Nonce       []byte          `json:"nonce"`
	IssuerHubID string          `json:"issuer_hub_id"`
	Kind        SignalKind      `json:"kind"`
	IssuedAt    time.Time       `json:"issued_at"`
	Expiry      time.Time       `json:"expiry"`
	Reason      string          `json:"reason"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Signature   []byte          `json:"signature"`
}

func (f *SignalFrame) Sign(priv ed25519.PrivateKey) error {
	body, err := f.canonicalSigningBytes()
	if err != nil {
		return err
	}
	f.Signature = ed25519.Sign(priv, body)
	return nil
}

func (f *SignalFrame) VerifySignature(pub ed25519.PublicKey) error {
	body, err := f.canonicalSigningBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, body, f.Signature) {
		return ErrUnsignedSignal
	}
	return nil
}

func (f *SignalFrame) canonicalSigningBytes() ([]byte, error) {
	copyFrame := *f
	copyFrame.Signature = nil
	return json.Marshal(copyFrame)
}

type TrustRoot struct {
	mu     sync.RWMutex
	pinned map[string]ed25519.PublicKey
}

func NewTrustRoot() *TrustRoot {
	return &TrustRoot{pinned: make(map[string]ed25519.PublicKey)}
}

func (r *TrustRoot) Pin(hubID string, pub ed25519.PublicKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pinned[hubID] = append(ed25519.PublicKey(nil), pub...)
}

func (r *TrustRoot) Lookup(hubID string) (ed25519.PublicKey, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key, ok := r.pinned[hubID]
	return key, ok
}

func (r *TrustRoot) IsEmpty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.pinned) == 0
}

type ReplayStore interface {
	Seen(nonce []byte, now time.Time) bool
}

type MemoryReplayStore struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func NewMemoryReplayStore() *MemoryReplayStore {
	return &MemoryReplayStore{seen: make(map[string]time.Time), ttl: 30 * 24 * time.Hour}
}

func (m *MemoryReplayStore) Seen(nonce []byte, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, seenAt := range m.seen {
		if now.Sub(seenAt) > m.ttl {
			delete(m.seen, key)
		}
	}
	key := string(nonce)
	if _, ok := m.seen[key]; ok {
		return true
	}
	m.seen[key] = now
	return false
}

func Verify(f *SignalFrame, root *TrustRoot, replay ReplayStore, now time.Time) (Verdict, error) {
	if root == nil || root.IsEmpty() {
		return VerdictRejectedUntrusted, ErrUntrustedIssuer
	}
	key, ok := root.Lookup(f.IssuerHubID)
	if !ok {
		return VerdictRejectedUntrusted, ErrUntrustedIssuer
	}
	if err := f.VerifySignature(key); err != nil {
		return VerdictRejectedSignature, err
	}
	if now.Before(f.IssuedAt.Add(-ClockSkewTolerance)) || now.After(f.Expiry.Add(ClockSkewTolerance)) {
		return VerdictRejectedExpired, ErrExpiredSignal
	}
	if !validKinds[f.Kind] {
		return VerdictRejectedUnknown, ErrUnknownKind
	}
	if replay != nil && replay.Seen(f.Nonce, now) {
		return VerdictRejectedReplay, ErrReplaySignal
	}
	return VerdictApplied, nil
}

type UserPromptRequest struct {
	Title   string
	Body    string
	Choices []string
}

type UserPromptResponse struct {
	Choice string
}

type UserPrompter interface {
	Prompt(context.Context, UserPromptRequest) (UserPromptResponse, error)
}

type SessionController interface {
	Pause(context.Context, string) error
	RotateSessionKey(context.Context) error
	ForceResurgence(context.Context, string) error
}

type ToolResult struct {
	OK            bool
	Output        json.RawMessage
	Error         string
	LedgerNodeIDs []string
}

type ToolDispatcher interface {
	Dispatch(context.Context, string, json.RawMessage) (ToolResult, error)
}

type Attestation struct {
	BuildHash               string
	ConstitutionHash        string
	LedgerRootHash          string
	ActiveTokenFingerprints []string
	Platform                string
	BeaconVersion           string
	AttestedAt              time.Time
}

type Attestor interface {
	Attest(context.Context) (Attestation, error)
}

type Dependencies struct {
	UserPrompter      UserPrompter
	SessionController SessionController
	ToolDispatcher    ToolDispatcher
	Attestor          Attestor
	Now               func() time.Time
}

type LedgerEntry struct {
	NodeType  string
	Marshaled []byte
}

type HandlerResult struct {
	Verdict       Verdict
	Notes         string
	LedgerEntries []LedgerEntry
}

type KindHandler interface {
	Kind() SignalKind
	Handle(context.Context, *SignalFrame, Dependencies) (HandlerResult, error)
}

type AskExecPayload struct {
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args,omitempty"`
	UserPrompt string          `json:"user_prompt"`
}

func (p AskExecPayload) Validate() error {
	if p.Tool == "" || p.UserPrompt == "" {
		return fmt.Errorf("trust: ask_exec payload requires tool and user_prompt")
	}
	if !AllowedAskExecTools[p.Tool] {
		return fmt.Errorf("trust: ask_exec tool %q not allowed", p.Tool)
	}
	return nil
}

type DisplayToUserPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (p DisplayToUserPayload) Validate() error {
	if p.Title == "" || p.Body == "" {
		return errors.New("trust: display_to_user requires title and body")
	}
	return nil
}

type PausePayload struct {
	Reason   string `json:"reason"`
	ResumeOn string `json:"resume_on"`
}

func (p PausePayload) Validate() error {
	switch p.ResumeOn {
	case "", "operator_confirm", "hub_resume", "deadline":
		return nil
	default:
		return fmt.Errorf("trust: invalid pause resume_on %q", p.ResumeOn)
	}
}
