package nodes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// SystemPromptFingerprint is the ledger record produced at daemon
// startup by the §T3 fingerprinting path. The promptguard package
// computes the ed25519 signature; this node persists the signed blob
// so an operator running `r1 promptguard verify-system-prompt` can
// compare the live-assembled prompt against the stored fingerprint.
//
// ID prefix: system_prompt_fingerprint-
//
// Spec: specs/promptguard-hardening.md §T3 item 12.
type SystemPromptFingerprint struct {
	// PromptSHA256 is the hex-encoded SHA-256 of the assembled prompt.
	PromptSHA256 string `json:"prompt_sha256"`
	// KeyFingerprint is the first 16 hex chars of SHA-256(pubkey). It
	// disambiguates key rotations so a fingerprint produced by an
	// older key surfaces as Tampered rather than silently re-verifying.
	KeyFingerprint string `json:"key_fingerprint"`
	// SignedAt is the wall-clock time the signature was produced.
	SignedAt time.Time `json:"signed_at"`
	// Signature is the hex-encoded ed25519 signature over the
	// canonical-JSON of {sha256, signed_at, key_fp}.
	Signature string `json:"signature"`
	// AssemblyTrace names the template files / config inputs that
	// contributed to the assembled prompt. Free-form string for now;
	// operator audit views render it verbatim so a Modified diff
	// can be triaged.
	AssemblyTrace string `json:"assembly_trace,omitempty"`

	Version int `json:"schema_version"`
}

// NodeType returns the canonical name used by the registry.
func (s *SystemPromptFingerprint) NodeType() string { return "system_prompt_fingerprint" }

// SchemaVersion exposes the on-wire version.
func (s *SystemPromptFingerprint) SchemaVersion() int { return s.Version }

// Validate enforces required fields. PromptSHA256 + Signature +
// SignedAt + KeyFingerprint MUST be present so the operator-facing
// verify CLI has something to compare against. AssemblyTrace is
// optional because legacy fingerprints written before that field
// existed should still round-trip.
func (s *SystemPromptFingerprint) Validate() error {
	if s.PromptSHA256 == "" {
		return fmt.Errorf("system_prompt_fingerprint: prompt_sha256 is required")
	}
	if s.KeyFingerprint == "" {
		return fmt.Errorf("system_prompt_fingerprint: key_fingerprint is required")
	}
	if s.Signature == "" {
		return fmt.Errorf("system_prompt_fingerprint: signature is required")
	}
	if s.SignedAt.IsZero() {
		return fmt.Errorf("system_prompt_fingerprint: signed_at is required")
	}
	if s.Version < 1 {
		return fmt.Errorf("system_prompt_fingerprint: schema_version must be >= 1")
	}
	return nil
}

// ContentHash returns a stable hex digest over the load-bearing fields
// of the node. Used by ledger consumers that want a one-line hash to
// dedup multiple writes of the same fingerprint (e.g. a daemon
// restart that re-fingerprints the same unchanged system prompt).
func (s *SystemPromptFingerprint) ContentHash() string {
	h := sha256.New()
	h.Write([]byte(s.PromptSHA256))
	h.Write([]byte(s.KeyFingerprint))
	h.Write([]byte(s.Signature))
	return hex.EncodeToString(h.Sum(nil))
}

func init() {
	Register("system_prompt_fingerprint", func() NodeTyper {
		return &SystemPromptFingerprint{Version: 1}
	})
}
