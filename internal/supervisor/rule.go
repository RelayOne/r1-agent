// Package supervisor implements a deterministic rules engine for Stoke v2.
//
// The supervisor reads events from the bus, matches against registered rules,
// evaluates conditions (which may query the ledger), and fires hook actions
// via the bus. It contains no rule-specific logic — the core just walks
// registered rules.
package supervisor

import (
	"context"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
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

// ValidatePayload is a helper rules with declared schemas call on
// themselves inside Action() before publishing the event. Returns nil
// when the payload matches the rule's declared schema (or when the
// rule doesn't declare one). Returns a non-nil error naming the
// failing field when the payload violates the schema — callers
// should log the error and skip the publish rather than emitting a
// malformed event. Closes matrix gap A3 at the call-site layer.
func ValidatePayload(r Rule, payload map[string]any) error {
	sp, ok := r.(PayloadSchemaProvider)
	if !ok {
		return nil
	}
	schema := sp.PayloadSchema()
	if schema == nil || len(schema.Fields) == 0 {
		return nil
	}
	res := schemaval.ValidateMap(payload, *schema)
	if res.Valid {
		return nil
	}
	return fmt.Errorf("payload schema violation: %s", res.String())
}

// PayloadSchemaProvider is an OPTIONAL interface a Rule may implement
// to declare the schema its emitted Action payloads must satisfy.
// The supervisor dispatcher checks for this interface at rule-firing
// time via a type assertion — rules that don't implement it remain
// schemaless. New rules SHOULD implement it: a payload with a missing
// required field fails silently at replay because the consumer has
// no schema to validate against. See docs/anti-deception-matrix.md
// row "supervisor payloads."
//
// Returning nil means "no schema declared" — equivalent to not
// implementing the interface. Validation uses internal/schemaval.
type PayloadSchemaProvider interface {
	PayloadSchema() *schemaval.Schema
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
