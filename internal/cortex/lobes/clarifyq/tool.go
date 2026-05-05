// Tool definition for ClarifyingQLobe (spec item 22).
//
// The clarifyTool variable carries the queue_clarifying_question tool
// definition the Lobe sends to Haiku 4.5 on every triggered user-turn
// call. The schema is verbatim from specs/cortex-concerns.md §5
// ("Tool schema (verbatim)") and is asserted byte-for-byte by
// TestClarifyingQLobe_ToolSchemaShape.
//
// API adaptation: the spec snippet uses Anthropic's wire field
// "input_schema"; r1's provider.ToolDef carries that JSON in its
// InputSchema field (which marshals to "input_schema" — see
// internal/provider/anthropic.go ToolDef definition). The verbatim
// JSON snippet is therefore embedded as the InputSchema RawMessage so
// the wire layout stays identical to the spec's literal JSON.
package clarifyq

import (
	"encoding/json"

	"github.com/RelayOne/r1/internal/provider"
)

// clarifyToolName is the model-facing tool name. The Lobe walks the
// model's response Content blocks looking for tool_use blocks whose
// Name matches this constant; anything else is ignored.
const clarifyToolName = "queue_clarifying_question"

// clarifyToolDescription is the model-facing tool description.
// Verbatim from spec §5.
const clarifyToolDescription = "Queue a clarifying question for the user. Surfaced at idle, never mid-tool-call. Maximum 3 outstanding."

// clarifyToolInputSchemaJSON is the JSON schema for the tool input.
// Verbatim from spec §5 — every field name, ordering, and description
// matches the spec snippet character-for-character. Drift in this
// constant busts the cache key on every Lobe call across every session.
const clarifyToolInputSchemaJSON = `{
  "type": "object",
  "properties": {
    "question":   {"type":"string", "description":"One sentence, ≤140 chars."},
    "category":   {"type":"string", "enum":["scope","constraint","preference","data","priority"]},
    "blocking":   {"type":"boolean", "description":"True if work cannot proceed without an answer."},
    "rationale":  {"type":"string", "description":"Why this is unclear, ≤200 chars."}
  },
  "required": ["question","category","blocking"]
}`

// clarifyTool is the provider.ToolDef sent to Haiku on every triggered
// turn. Defined as a package-level var (not a const) because
// json.RawMessage is a slice type and Go forbids slice consts.
var clarifyTool = provider.ToolDef{
	Name:        clarifyToolName,
	Description: clarifyToolDescription,
	InputSchema: json.RawMessage(clarifyToolInputSchemaJSON),
}
