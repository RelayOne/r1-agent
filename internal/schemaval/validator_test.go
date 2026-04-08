package schemaval

import (
	"testing"
)

func TestValidateSimple(t *testing.T) {
	schema := Schema{
		Name: "test",
		Fields: []Field{
			{Name: "name", Type: TypeString, Required: true},
			{Name: "age", Type: TypeNumber, Required: true},
		},
	}

	result := Validate(`{"name": "Alice", "age": 30}`, schema)
	if !result.Valid {
		t.Errorf("expected valid, got: %s", result.Error())
	}
}

func TestValidateMissingRequired(t *testing.T) {
	schema := Schema{
		Name: "test",
		Fields: []Field{
			{Name: "name", Type: TypeString, Required: true},
			{Name: "age", Type: TypeNumber, Required: true},
		},
	}

	result := Validate(`{"name": "Alice"}`, schema)
	if result.Valid {
		t.Error("should be invalid: missing required age")
	}
	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(result.Errors))
	}
}

func TestValidateWrongType(t *testing.T) {
	schema := Schema{
		Name: "test",
		Fields: []Field{
			{Name: "count", Type: TypeNumber, Required: true},
		},
	}

	result := Validate(`{"count": "not a number"}`, schema)
	if result.Valid {
		t.Error("should be invalid: wrong type")
	}
}

func TestValidateEnum(t *testing.T) {
	schema := Schema{
		Name: "test",
		Fields: []Field{
			{Name: "status", Type: TypeString, Required: true, Enum: []string{"active", "inactive"}},
		},
	}

	result := Validate(`{"status": "active"}`, schema)
	if !result.Valid {
		t.Errorf("should be valid: %s", result.Error())
	}

	result = Validate(`{"status": "unknown"}`, schema)
	if result.Valid {
		t.Error("should be invalid: not in enum")
	}
}

func TestValidateStringLength(t *testing.T) {
	schema := Schema{
		Name: "test",
		Fields: []Field{
			{Name: "code", Type: TypeString, Required: true, MinLen: 3, MaxLen: 10},
		},
	}

	result := Validate(`{"code": "ab"}`, schema)
	if result.Valid {
		t.Error("should fail: too short")
	}

	result = Validate(`{"code": "abcdefghijk"}`, schema)
	if result.Valid {
		t.Error("should fail: too long")
	}

	result = Validate(`{"code": "abc"}`, schema)
	if !result.Valid {
		t.Errorf("should pass: %s", result.Error())
	}
}

func TestValidateNestedObject(t *testing.T) {
	schema := Schema{
		Name: "test",
		Fields: []Field{
			{Name: "config", Type: TypeObject, Required: true, Fields: []Field{
				{Name: "host", Type: TypeString, Required: true},
				{Name: "port", Type: TypeNumber, Required: true},
			}},
		},
	}

	result := Validate(`{"config": {"host": "localhost", "port": 8080}}`, schema)
	if !result.Valid {
		t.Errorf("should be valid: %s", result.Error())
	}

	result = Validate(`{"config": {"host": "localhost"}}`, schema)
	if result.Valid {
		t.Error("should fail: nested port missing")
	}
}

func TestValidateArray(t *testing.T) {
	schema := Schema{
		Name: "test",
		Fields: []Field{
			{Name: "tags", Type: TypeArray, Required: true},
		},
	}

	result := Validate(`{"tags": ["a", "b"]}`, schema)
	if !result.Valid {
		t.Errorf("should be valid: %s", result.Error())
	}

	result = Validate(`{"tags": "not-array"}`, schema)
	if result.Valid {
		t.Error("should fail: not an array")
	}
}

func TestValidateInvalidJSON(t *testing.T) {
	schema := Schema{Name: "test"}
	result := Validate("not json", schema)
	if result.Valid {
		t.Error("should fail on invalid JSON")
	}
}

func TestValidateOptionalMissing(t *testing.T) {
	schema := Schema{
		Name: "test",
		Fields: []Field{
			{Name: "optional", Type: TypeString, Required: false},
		},
	}

	result := Validate(`{}`, schema)
	if !result.Valid {
		t.Error("missing optional field should be valid")
	}
}

func TestCommonSchemas(t *testing.T) {
	if _, ok := CommonSchemas["tool_call"]; !ok {
		t.Error("missing tool_call schema")
	}
	if _, ok := CommonSchemas["edit"]; !ok {
		t.Error("missing edit schema")
	}
	if _, ok := CommonSchemas["task_result"]; !ok {
		t.Error("missing task_result schema")
	}
}

func TestExtractJSON(t *testing.T) {
	// Plain JSON
	j, ok := ExtractJSON(`{"key": "value"}`)
	if !ok || j != `{"key": "value"}` {
		t.Error("should extract plain JSON")
	}

	// JSON in code block
	j, ok = ExtractJSON("here is the result:\n```json\n{\"a\": 1}\n```\n")
	if !ok || j != `{"a": 1}` {
		t.Errorf("should extract from code block, got %q ok=%v", j, ok)
	}

	// JSON embedded in text
	j, ok = ExtractJSON(`The result is {"status": "ok"} as expected`)
	if !ok {
		t.Error("should extract embedded JSON")
	}

	// No JSON
	_, ok = ExtractJSON("no json here")
	if ok {
		t.Error("should not find JSON")
	}
}

func TestFormatErrors(t *testing.T) {
	r := Result{Valid: true}
	if FormatErrors(r) != "" {
		t.Error("valid result should produce empty string")
	}

	r = Result{
		Valid:  false,
		Errors: []ValidationError{{Path: "$.name", Message: "required"}},
	}
	formatted := FormatErrors(r)
	if formatted == "" {
		t.Error("should produce error output")
	}
}

func TestValidateBool(t *testing.T) {
	schema := Schema{
		Name: "test",
		Fields: []Field{
			{Name: "active", Type: TypeBool, Required: true},
		},
	}

	result := Validate(`{"active": true}`, schema)
	if !result.Valid {
		t.Error("should be valid")
	}

	result = Validate(`{"active": "yes"}`, schema)
	if result.Valid {
		t.Error("should fail: string not bool")
	}
}

func TestValidateNullRequired(t *testing.T) {
	schema := Schema{
		Name: "test",
		Fields: []Field{
			{Name: "name", Type: TypeString, Required: true},
		},
	}

	result := Validate(`{"name": null}`, schema)
	if result.Valid {
		t.Error("null required field should fail")
	}
}
