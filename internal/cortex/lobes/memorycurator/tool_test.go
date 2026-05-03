package memorycurator

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestMemoryCuratorLobe_ToolSchemaShape covers TASK-27.
//
// Asserts the rememberTool ToolDef matches the verbatim spec §6 schema:
//
//   - Name = "remember"
//   - Description matches the spec sentence verbatim
//   - InputSchema parses to the structured shape declared in the spec
//     (type=object, five named properties, required={category,content})
//
// The check parses the InputSchema JSON and walks the resulting
// map[string]any so the test is robust to whitespace / property order
// drift inside the JSON literal — the assertion is structural equality
// with the spec, not byte-for-byte equality (the byte-equality test on
// the system prompt is the authority for cache-key stability;
// schemas are normalized before they leave the wire).
func TestMemoryCuratorLobe_ToolSchemaShape(t *testing.T) {
	t.Parallel()

	if got, want := rememberTool.Name, "remember"; got != want {
		t.Errorf("rememberTool.Name = %q, want %q", got, want)
	}

	wantDesc := "Persist a project-fact to long-term memory. Use sparingly — only durable, non-private facts."
	if got := rememberTool.Description; got != wantDesc {
		t.Errorf("rememberTool.Description drifted from spec §6\n got=%q\nwant=%q", got, wantDesc)
	}

	if len(rememberTool.InputSchema) == 0 {
		t.Fatal("rememberTool.InputSchema is empty")
	}

	var schema map[string]any
	if err := json.Unmarshal(rememberTool.InputSchema, &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}

	if got, want := schema["type"], "object"; got != want {
		t.Errorf("schema.type = %v, want %v", got, want)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties is not an object: %T", schema["properties"])
	}

	wantPropNames := []string{"category", "content", "context", "file", "tags"}
	for _, name := range wantPropNames {
		if _, ok := props[name]; !ok {
			t.Errorf("schema.properties missing %q", name)
		}
	}

	// "category" must declare the spec's exact six-value enum.
	cat, ok := props["category"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties.category not a map: %T", props["category"])
	}
	gotEnum, _ := cat["enum"].([]any)
	wantEnum := []any{"gotcha", "pattern", "preference", "fact", "anti_pattern", "fix"}
	if !reflect.DeepEqual(gotEnum, wantEnum) {
		t.Errorf("category.enum = %v, want %v", gotEnum, wantEnum)
	}

	// "tags" must declare a string-array shape.
	tagsField, ok := props["tags"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties.tags not a map: %T", props["tags"])
	}
	if got, want := tagsField["type"], "array"; got != want {
		t.Errorf("tags.type = %v, want %v", got, want)
	}
	tagsItems, ok := tagsField["items"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties.tags.items not a map: %T", tagsField["items"])
	}
	if got, want := tagsItems["type"], "string"; got != want {
		t.Errorf("tags.items.type = %v, want %v", got, want)
	}

	// required must be exactly {category, content} per the spec.
	gotReq, _ := schema["required"].([]any)
	wantReq := []any{"category", "content"}
	if !reflect.DeepEqual(gotReq, wantReq) {
		t.Errorf("schema.required = %v, want %v", gotReq, wantReq)
	}
}
