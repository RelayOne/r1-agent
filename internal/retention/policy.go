// Package retention defines the data-retention policy types used by Stoke's
// retention-policy engine (see specs/retention-policies.md).
//
// This file owns the pure type layer: the Duration enum, the Policy struct,
// the Defaults() constructor, and Validate(). The enforcement primitives
// (EnforceOnSessionEnd, EnforceSweep, SweepLoop) and the redaction signer
// live in sibling files and are wired up by later commits as the
// memory-bus and ledger-redaction dependencies stabilise.
package retention

import "fmt"

// Duration is the string-typed enum of valid retention durations. Operators
// configure each retained surface (memories, stream files, ledger content,
// checkpoints, prompts) by selecting one of these values in YAML.
type Duration string

const (
	// WipeAfterSession deletes (or, for ledger content, crypto-shreds) the
	// surface as soon as the owning session ends.
	WipeAfterSession Duration = "wipe_after_session"
	// Retain7Days keeps the surface for seven days past its end-of-life
	// timestamp before sweeping.
	Retain7Days Duration = "retain_7_days"
	// Retain30Days keeps the surface for thirty days past its end-of-life
	// timestamp before sweeping.
	Retain30Days Duration = "retain_30_days"
	// Retain90Days keeps the surface for ninety days past its end-of-life
	// timestamp before sweeping.
	Retain90Days Duration = "retain_90_days"
	// RetainForever opts the surface out of all automatic deletion. Required
	// for permanent_memories and ledger_nodes.
	RetainForever Duration = "retain_forever"
)

// validDurations is the canonical list used by both IsValidDuration and the
// validation error message so they cannot drift apart.
var validDurations = []Duration{
	WipeAfterSession,
	Retain7Days,
	Retain30Days,
	Retain90Days,
	RetainForever,
}

// IsValidDuration reports whether d is one of the five known Duration
// constants.
func IsValidDuration(d Duration) bool {
	for _, v := range validDurations {
		if d == v {
			return true
		}
	}
	return false
}

// Policy is the operator-facing retention configuration. Each field maps to
// one of the retained surfaces described in specs/retention-policies.md §2.
//
// PermanentMemories and LedgerNodes are immutable: any value other than
// RetainForever is rejected by Validate().
type Policy struct {
	EphemeralMemories   Duration `yaml:"ephemeral_memories"`
	SessionMemories     Duration `yaml:"session_memories"`
	PersistentMemories  Duration `yaml:"persistent_memories"`
	PermanentMemories   Duration `yaml:"permanent_memories"`
	StreamFiles         Duration `yaml:"stream_files"`
	LedgerNodes         Duration `yaml:"ledger_nodes"`
	LedgerContent       Duration `yaml:"ledger_content"`
	CheckpointFiles     Duration `yaml:"checkpoint_files"`
	PromptsAndResponses Duration `yaml:"prompts_and_responses"`
}

// Defaults returns the spec's default policy, suitable for the
// STOKE_RETENTION=1 default-on profile when no operator config exists.
func Defaults() Policy {
	return Policy{
		EphemeralMemories:   WipeAfterSession,
		SessionMemories:     Retain30Days,
		PersistentMemories:  RetainForever,
		PermanentMemories:   RetainForever,
		StreamFiles:         Retain90Days,
		LedgerNodes:         RetainForever,
		LedgerContent:       RetainForever,
		CheckpointFiles:     Retain30Days,
		PromptsAndResponses: RetainForever,
	}
}

// policyField is a (yaml-key, value) pair used to drive Validate without
// duplicating the field list.
type policyField struct {
	key   string
	value Duration
}

func (p Policy) fields() []policyField {
	return []policyField{
		{"ephemeral_memories", p.EphemeralMemories},
		{"session_memories", p.SessionMemories},
		{"persistent_memories", p.PersistentMemories},
		{"permanent_memories", p.PermanentMemories},
		{"stream_files", p.StreamFiles},
		{"ledger_nodes", p.LedgerNodes},
		{"ledger_content", p.LedgerContent},
		{"checkpoint_files", p.CheckpointFiles},
		{"prompts_and_responses", p.PromptsAndResponses},
	}
}

// immutableForeverKeys lists the YAML keys that MUST be set to RetainForever.
// Both the chain-tier ledger and the permanent memory tier are non-negotiable.
var immutableForeverKeys = map[string]bool{
	"permanent_memories": true,
	"ledger_nodes":       true,
}

// Validate returns nil iff every field holds a known Duration and the
// immutable-forever fields hold RetainForever. The error messages match the
// strings documented in specs/retention-policies.md §3.
func (p Policy) Validate() error {
	for _, f := range p.fields() {
		if !IsValidDuration(f.value) {
			return fmt.Errorf(
				"retention.%s: invalid duration %q, must be one of [wipe_after_session retain_7_days retain_30_days retain_90_days retain_forever]",
				f.key, string(f.value),
			)
		}
		if immutableForeverKeys[f.key] && f.value != RetainForever {
			return fmt.Errorf("retention.%s: must be retain_forever (immutable)", f.key)
		}
	}
	return nil
}
