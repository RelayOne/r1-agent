// Package a2a emits Agent-to-Agent (A2A) Agent Cards per the
// A2A protocol spec (STOKE-013/-018). The Agent Card is a static
// JSON document published at the canonical `/.well-known/agent.json`
// URL an A2A-compliant peer consults before submitting a task.
//
// Scope of this initial implementation: the card *generator*. A
// separate HTTP handler in `cmd/stoke-gateway/` or `internal/server/`
// will eventually mount the card at the well-known path; for now
// operators can write the generated document to disk and serve it
// via any static file server.
//
// What the card declares:
//   - agent identity (name, version, public key derived from the
//     stancesign Reviewer identity so peers can validate signed
//     messages later)
//   - protocol version the agent speaks
//   - capability list (derived from the capability manifest layer
//     in internal/skillmfr/ when that wiring lands; for now it's
//     operator-supplied via Capabilities)
//   - endpoint URLs for task submission + discovery
//   - supported content types the agent accepts in prompts
//
// This file is strictly additive: no existing Stoke code path reads
// A2A cards yet. The generator exists so that STOKE-018's A2A task
// lifecycle work can plug in without a separate scaffolding commit.
package a2a

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ProtocolVersion is the A2A spec version this generator targets.
// Bumped when the Agent Card schema evolves in a breaking way.
const ProtocolVersion = "0.2.0"

// AgentCard is the canonical A2A /.well-known/agent.json document
// shape. Field names and structure match the A2A spec so any
// compliant peer can consume the marshaled JSON without
// re-mapping.
type AgentCard struct {
	ProtocolVersion string   `json:"protocolVersion"`
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	Version         string   `json:"version"`
	URL             string   `json:"url,omitempty"`
	Provider        Provider `json:"provider,omitempty"`

	// Identity carries the agent's public key material so peers can
	// verify signatures on subsequent A2A messages without a
	// separate key-exchange round-trip. Key derived from
	// internal/stancesign/ on the operator side — the card
	// publishes the Reviewer stance's public key because Reviewer
	// is the cross-cutting identity across missions.
	Identity AgentIdentity `json:"identity,omitempty"`

	// Capabilities lists what the agent can do. The A2A spec keeps
	// this open-ended (free-form list of capability descriptors);
	// the structured form lives in the per-capability manifests
	// (STOKE-003), referenced here by name + version.
	Capabilities []CapabilityRef `json:"capabilities"`

	// Skills the agent ships with. Parallel to Capabilities but
	// carries richer descriptions so peers can display them in
	// discovery UIs.
	Skills []SkillDescriptor `json:"skills,omitempty"`

	// Endpoints declares how to reach the agent for task
	// submission, status polling, and streaming updates. Each is
	// optional; peers fall back to the top-level URL when an
	// endpoint is absent.
	Endpoints AgentEndpoints `json:"endpoints"`

	// DefaultInputModes / DefaultOutputModes list the content
	// types the agent accepts in prompts and emits in responses.
	// A peer submitting a task with an unsupported mode must get a
	// clear rejection, not a silent degradation.
	DefaultInputModes  []string `json:"defaultInputModes,omitempty"`
	DefaultOutputModes []string `json:"defaultOutputModes,omitempty"`

	// IssuedAt / ExpiresAt bound the card's freshness. Peers
	// should re-fetch when ExpiresAt has passed; absent expiry
	// means "fetch on every discovery round".
	IssuedAt  time.Time  `json:"issuedAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// Provider identifies the organization operating the agent. A2A
// discovery clients often group agents by provider in their UI.
type Provider struct {
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

// AgentIdentity publishes the verifiable identity bits a peer needs
// to authenticate subsequent messages.
type AgentIdentity struct {
	// PublicKeyPEM is the ed25519 public key material as PEM.
	// Peers caching this card can verify signed A2A messages
	// without a separate key-fetch round-trip.
	PublicKeyPEM string `json:"publicKeyPem,omitempty"`

	// DID is the TrustPlane-issued decentralized identifier, when
	// STOKE-011 registration has occurred. Empty string when
	// running in local-only mode.
	DID string `json:"did,omitempty"`

	// StanceRole is "reviewer" for the cross-cutting card. Not
	// an A2A spec field; prefixed with `_stoke.dev/` in output
	// for spec-strict consumers.
	StanceRole string `json:"_stoke.dev/stance_role,omitempty"`
}

// CapabilityRef is a pointer into the capability manifest registry
// (STOKE-003). The manifest hash is recorded so peers can detect
// drift without re-fetching every capability's full schema.
type CapabilityRef struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"`
	ManifestHash string `json:"manifestHash,omitempty"`
}

// SkillDescriptor is a richer capability entry aimed at discovery
// UIs. Corresponds to an entry in the skill library (STOKE-009 tier
// 4 / agentskills.io format).
type SkillDescriptor struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
}

// AgentEndpoints groups the URLs an A2A peer uses to interact with
// the agent. JSON-RPC is the default A2A binding; gRPC ships as
// STOKE-018 extends this.
type AgentEndpoints struct {
	JSONRPC string `json:"jsonrpc,omitempty"`
	SSE     string `json:"sse,omitempty"`
	GRPC    string `json:"grpc,omitempty"`
}

// Build assembles an AgentCard from the supplied options. Fields
// left empty in the options are omitted from JSON output (so the
// card stays compact and peers don't see misleading zero values).
func Build(opts Options) AgentCard {
	now := opts.IssuedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var exp *time.Time
	if opts.TTL > 0 {
		t := now.Add(opts.TTL)
		exp = &t
	}
	if len(opts.DefaultInputModes) == 0 {
		opts.DefaultInputModes = []string{"text/plain", "application/json"}
	}
	if len(opts.DefaultOutputModes) == 0 {
		opts.DefaultOutputModes = []string{"text/plain", "application/json"}
	}
	// A2A spec requires `capabilities` to be an array. A nil
	// slice marshals to `"capabilities": null`, which strict
	// consumers reject — and the standalone stoke-a2a binary
	// leaves the slice nil when STOKE_A2A_CAPABILITIES is
	// unset. Normalize to empty slice so the wire shape is
	// always `[]`.
	caps := opts.Capabilities
	if caps == nil {
		caps = []CapabilityRef{}
	}
	return AgentCard{
		ProtocolVersion:    ProtocolVersion,
		Name:               opts.Name,
		Description:        opts.Description,
		Version:            opts.Version,
		URL:                opts.URL,
		Provider:           opts.Provider,
		Identity:           opts.Identity,
		Capabilities:       caps,
		Skills:             opts.Skills,
		Endpoints:          opts.Endpoints,
		DefaultInputModes:  opts.DefaultInputModes,
		DefaultOutputModes: opts.DefaultOutputModes,
		IssuedAt:           now,
		ExpiresAt:          exp,
	}
}

// Options parameterize Build. Zero-value fields are omitted from
// the resulting card.
type Options struct {
	Name         string
	Description  string
	Version      string
	URL          string
	Provider     Provider
	Identity     AgentIdentity
	Capabilities []CapabilityRef
	Skills       []SkillDescriptor
	Endpoints    AgentEndpoints

	DefaultInputModes  []string
	DefaultOutputModes []string

	// IssuedAt defaults to time.Now().UTC() when zero.
	IssuedAt time.Time
	// TTL is how long the card is valid for. Zero means "always
	// fresh" — the ExpiresAt field is omitted in that case, and
	// callers are expected to re-issue on a schedule they manage.
	TTL time.Duration
}

// MarshalJSON on the card is the default encoding/json behavior;
// we expose ToJSON as a convenience to avoid callers repeating the
// marshal-and-indent dance.
func (c AgentCard) ToJSON() ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("a2a: marshal agent card: %w", err)
	}
	return b, nil
}

// WriteFile serializes the card and writes it to path with 0644
// permissions. Useful for operators who serve /.well-known/agent.json
// via a static web server; the agent process writes the card on
// start + on capability-set change.
//
// Overwrites atomically: writes to path + ".tmp" first, then
// renames. Callers never observe a half-written card.
func (c AgentCard) WriteFile(path string) error {
	b, err := c.ToJSON()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("a2a: write agent card: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("a2a: rename agent card: %w", err)
	}
	return nil
}
