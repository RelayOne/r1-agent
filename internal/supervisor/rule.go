// Package supervisor implements a deterministic rules engine for Stoke v2.
//
// The supervisor reads events from the bus, matches against registered rules,
// evaluates conditions (which may query the ledger), and fires hook actions
// via the bus. It contains no rule-specific logic — the core just walks
// registered rules.
package supervisor

import (
	"context"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

// Rule is the interface all supervisor rules implement.
type Rule interface {
	// Name returns a unique, human-readable identifier for the rule.
	Name() string

	// Pattern returns the bus pattern this rule matches against.
	Pattern() bus.Pattern

	// Priority returns the evaluation order. Higher values evaluate first.
	Priority() int

	// Evaluate inspects the event and ledger state to decide whether the
	// rule should fire. It must be side-effect free.
	Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error)

	// Action executes the rule's effects by publishing events on the bus.
	Action(ctx context.Context, evt bus.Event, b *bus.Bus) error

	// Rationale returns a human-readable explanation of why this rule exists.
	Rationale() string
}

// PayloadSchemaProvider is an OPTIONAL interface a Rule may implement
// to declare the JSON Schema its emitted Action payloads must satisfy.
// The supervisor checks for this interface at dispatch time via a type
// assertion — rules that don't implement it remain schemaless. New
// rules SHOULD implement it: a payload with a missing required field
// fails silently at replay because the consumer has no schema to
// validate against. See docs/anti-deception-matrix.md row "supervisor
// payloads."
//
// The schema is returned as a raw JSON-encoded byte slice (e.g. from
// `json.Marshal` of a JSON Schema document, or an embed of a .json
// file). Returning nil or an empty slice means "no schema declared" —
// equivalent to not implementing the interface at all. Validation
// itself is performed by internal/schemaval which already exists for
// phase-contract validation.
type PayloadSchemaProvider interface {
	PayloadSchema() []byte
}

// ConfigSchema describes wizard-tunable knobs for a rule.
type ConfigSchema struct {
	Disableable bool                   `json:"disableable"`
	Fields      map[string]ConfigField `json:"fields,omitempty"`
}

// ConfigField describes a single tunable parameter.
type ConfigField struct {
	Type        string `json:"type"` // "int", "duration", "bool", "string"
	Default     any    `json:"default"`
	Description string `json:"description"`
}

// RuleConfig holds per-rule configuration from the wizard.
type RuleConfig struct {
	Enabled    *bool          `json:"enabled,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}
