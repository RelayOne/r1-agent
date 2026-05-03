package clarifyq

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestClarifyingQLobe_ToolSchemaShape covers TASK-22.
//
// Asserts the clarifyTool ToolDef matches the verbatim spec §5 schema:
//
//   - Name = "queue_clarifying_question"
//   - Description matches the spec sentence verbatim
//   - InputSchema parses to the structured shape declared in the spec
//     (type=object, four named properties, required={question,category,blocking})
//
// The check parses the InputSchema JSON and walks the resulting
// map[string]any so the test is robust to whitespace / property order
// drift inside the JSON literal — the assertion is structural equality
// with the spec, not byte-for-byte equality (the byte-equality test on
// the system prompt is the authority for cache-key stability;
// schemas are normalized before they leave the wire).
func TestClarifyingQLobe_ToolSchemaShape(t *testing.T) {
	t.Parallel()

	if got, want := clarifyTool.Name, "queue_clarifying_question"; got != want {
		t.Errorf("clarifyTool.Name = %q, want %q", got, want)
	}

	wantDesc := "Queue a clarifying question for the user. Surfaced at idle, never mid-tool-call. Maximum 3 outstanding."
	if got := clarifyTool.Description; got != wantDesc {
		t.Errorf("clarifyTool.Description drifted from spec §5\n got=%q\nwant=%q", got, wantDesc)
	}

	if len(clarifyTool.InputSchema) == 0 {
		t.Fatal("clarifyTool.InputSchema is empty")
	}

	var schema map[string]any
	if err := json.Unmarshal(clarifyTool.InputSchema, &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}

	if got, want := schema["type"], "object"; got != want {
		t.Errorf("schema.type = %v, want %v", got, want)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties is not an object: %T", schema["properties"])
	}

	wantPropNames := []string{"question", "category", "blocking", "rationale"}
	for _, name := range wantPropNames {
		if _, ok := props[name]; !ok {
			t.Errorf("schema.properties missing %q", name)
		}
	}

	// "category" must declare the spec's exact enum.
	cat, ok := props["category"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties.category not a map: %T", props["category"])
	}
	gotEnum, _ := cat["enum"].([]any)
	wantEnum := []any{"scope", "constraint", "preference", "data", "priority"}
	if !reflect.DeepEqual(gotEnum, wantEnum) {
		t.Errorf("category.enum = %v, want %v", gotEnum, wantEnum)
	}

	// "blocking" must be boolean per the spec.
	blocking, ok := props["blocking"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties.blocking not a map: %T", props["blocking"])
	}
	if got, want := blocking["type"], "boolean"; got != want {
		t.Errorf("blocking.type = %v, want %v", got, want)
	}

	// required must be exactly {question, category, blocking}.
	gotReq, _ := schema["required"].([]any)
	wantReq := []any{"question", "category", "blocking"}
	if !reflect.DeepEqual(gotReq, wantReq) {
		t.Errorf("schema.required = %v, want %v", gotReq, wantReq)
	}
}
