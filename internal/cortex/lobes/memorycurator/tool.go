// Tool definition for MemoryCuratorLobe (spec item 27).
//
// The rememberTool variable carries the "remember" tool definition the
// Lobe sends to Haiku 4.5 on every triggered turn. The schema is verbatim
// from specs/cortex-concerns.md §6 ("Tool schema (verbatim)") and is
// asserted structurally by TestMemoryCuratorLobe_ToolSchemaShape.
//
// API adaptation: the spec snippet uses Anthropic's wire field
// "input_schema"; r1's provider.ToolDef carries that JSON in its
// InputSchema field (which marshals to "input_schema" — see
// internal/provider/anthropic.go ToolDef definition). The verbatim
// JSON snippet is therefore embedded as the InputSchema RawMessage so
// the wire layout stays identical to the spec's literal JSON.
package memorycurator

import (
	"encoding/json"

	"github.com/RelayOne/r1/internal/provider"
)

// rememberToolName is the model-facing tool name. The Lobe walks the
// model's response Content blocks looking for tool_use blocks whose
// Name matches this constant; anything else is ignored.
const rememberToolName = "remember"

// rememberToolDescription is the model-facing tool description.
// Verbatim from spec §6.
const rememberToolDescription = "Persist a project-fact to long-term memory. Use sparingly — only durable, non-private facts."

// rememberToolInputSchemaJSON is the JSON schema for the tool input.
// Verbatim from spec §6 — every field name, ordering, and description
// matches the spec snippet character-for-character. Drift in this
// constant busts the cache key on every Lobe call across every session.
const rememberToolInputSchemaJSON = `{
  "type": "object",
  "properties": {
    "category": {"type":"string", "enum":["gotcha","pattern","preference","fact","anti_pattern","fix"]},
    "content":  {"type":"string", "description":"Single declarative sentence, ≤200 chars."},
    "context":  {"type":"string", "description":"What in the conversation triggered this, ≤200 chars."},
    "file":     {"type":"string", "description":"Related file path, optional."},
    "tags":     {"type":"array", "items":{"type":"string"}}
  },
  "required":["category","content"]
}`

// rememberTool is the provider.ToolDef sent to Haiku on every triggered
// turn. Defined as a package-level var (not a const) because
// json.RawMessage is a slice type and Go forbids slice consts.
var rememberTool = provider.ToolDef{
	Name:        rememberToolName,
	Description: rememberToolDescription,
	InputSchema: json.RawMessage(rememberToolInputSchemaJSON),
}
