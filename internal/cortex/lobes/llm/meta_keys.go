package llm

// Note.Meta key conventions used by LLM Lobes.
//
// cortex-core spec 1 freezes the Note struct (workspace.go); these keys
// are how Lobes signal user-action workflows (PlanUpdate, Clarify, Curator)
// without modifying that struct. Consumers (UI, Router) read these keys
// from Note.Meta to drive confirm/cancel/expire UX.
//
// Spec: specs/cortex-concerns.md item 4.
const (
	// MetaActionKind: "user-confirm", "rule-violation", "memory-suggested",
	// "clarifying-question", etc. Free-form string; consumers should treat
	// unknown values as opaque.
	MetaActionKind = "action_kind"

	// MetaActionPayload: action-specific JSON-encoded payload (added/removed
	// plan items for PlanUpdate, the question text for Clarify, etc.).
	MetaActionPayload = "action_payload"

	// MetaExpiresAfterRound: round number after which a Note auto-resolves
	// (uint64 stored as JSON number). Used by transient suggestions that
	// shouldn't pile up if no action is taken.
	MetaExpiresAfterRound = "expires_after_round"

	// MetaRefs: JSON array of strings — IDs of related Notes / memory entries
	// / WAL events. Used for tracing "this Note was caused by event X".
	MetaRefs = "refs"
)
