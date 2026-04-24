// Package schemaval implements structured output validation for agent responses.
// Inspired by OpenHands' action validation and claw-code's tool result parsing:
//
// When agents produce structured output (JSON actions, tool calls, code blocks),
// this package validates the structure BEFORE execution. This catches:
// - Missing required fields
// - Wrong types
// - Invalid values
// - Malformed JSON
//
// Preventing bad tool calls saves expensive API round-trips.
package schemaval

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FieldType is a JSON schema type.
type FieldType string

const (
	TypeString  FieldType = "string"
	TypeNumber  FieldType = "number"
	TypeBool    FieldType = "boolean"
	TypeObject  FieldType = "object"
	TypeArray   FieldType = "array"
	TypeAny     FieldType = "any"
)

// Field describes a single field in a schema.
type Field struct {
	Name     string    `json:"name"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required"`
	Enum     []string  `json:"enum,omitempty"`     // allowed values for strings
	MinLen   int       `json:"min_len,omitempty"`   // minimum string length
	MaxLen   int       `json:"max_len,omitempty"`   // maximum string length
	Pattern  string    `json:"pattern,omitempty"`   // regex pattern for strings
	Fields   []Field   `json:"fields,omitempty"`    // nested fields for objects
}

// Schema describes the expected structure of an output.
type Schema struct {
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
}

// ValidationError describes a single validation failure.
type ValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

// ValidationResult holds the validation outcome.
type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationError `json:"errors,omitempty"`
}

// String returns a combined summary of any validation errors.
// Renamed from Error() so errname doesn't conflict with the
// error-type-name convention (ValidationResult is a success/failure
// outcome, not a Go error type).
func (r ValidationResult) String() string {
	if r.Valid {
		return ""
	}
	var msgs []string
	for _, e := range r.Errors {
		msgs = append(msgs, e.Error())
	}
	return strings.Join(msgs, "; ")
}

// Validate checks a JSON string against a schema.
func Validate(jsonStr string, schema Schema) ValidationResult {
	var data map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return ValidationResult{
			Valid:  false,
			Errors: []ValidationError{{Path: "$", Message: fmt.Sprintf("invalid JSON: %v", err)}},
		}
	}
	return ValidateMap(data, schema)
}

// ValidateMap checks a parsed map against a schema.
func ValidateMap(data map[string]any, schema Schema) ValidationResult {
	var errors []ValidationError
	validateFields("$", data, schema.Fields, &errors)
	return ValidationResult{
		Valid:  len(errors) == 0,
		Errors: errors,
	}
}

func validateFields(prefix string, data map[string]any, fields []Field, errors *[]ValidationError) {
	for _, field := range fields {
		path := prefix + "." + field.Name
		val, exists := data[field.Name]

		if !exists {
			if field.Required {
				*errors = append(*errors, ValidationError{Path: path, Message: "required field missing"})
			}
			continue
		}

		if val == nil {
			if field.Required {
				*errors = append(*errors, ValidationError{Path: path, Message: "required field is null"})
			}
			continue
		}

		validateValue(path, val, field, errors)
	}
}

func validateValue(path string, val any, field Field, errors *[]ValidationError) {
	switch field.Type {
	case TypeString:
		s, ok := val.(string)
		if !ok {
			*errors = append(*errors, ValidationError{Path: path, Message: fmt.Sprintf("expected string, got %T", val)})
			return
		}
		if field.MinLen > 0 && len(s) < field.MinLen {
			*errors = append(*errors, ValidationError{Path: path, Message: fmt.Sprintf("string too short: %d < %d", len(s), field.MinLen)})
		}
		if field.MaxLen > 0 && len(s) > field.MaxLen {
			*errors = append(*errors, ValidationError{Path: path, Message: fmt.Sprintf("string too long: %d > %d", len(s), field.MaxLen)})
		}
		if len(field.Enum) > 0 {
			found := false
			for _, e := range field.Enum {
				if s == e {
					found = true
					break
				}
			}
			if !found {
				*errors = append(*errors, ValidationError{Path: path, Message: fmt.Sprintf("value %q not in enum %v", s, field.Enum)})
			}
		}

	case TypeNumber:
		switch val.(type) {
		case float64, int, int64, float32:
			// ok
		default:
			*errors = append(*errors, ValidationError{Path: path, Message: fmt.Sprintf("expected number, got %T", val)})
		}

	case TypeBool:
		if _, ok := val.(bool); !ok {
			*errors = append(*errors, ValidationError{Path: path, Message: fmt.Sprintf("expected boolean, got %T", val)})
		}

	case TypeObject:
		obj, ok := val.(map[string]any)
		if !ok {
			*errors = append(*errors, ValidationError{Path: path, Message: fmt.Sprintf("expected object, got %T", val)})
			return
		}
		if len(field.Fields) > 0 {
			validateFields(path, obj, field.Fields, errors)
		}

	case TypeArray:
		if _, ok := val.([]any); !ok {
			*errors = append(*errors, ValidationError{Path: path, Message: fmt.Sprintf("expected array, got %T", val)})
		}

	case TypeAny:
		// anything goes
	}
}

// CommonSchemas provides pre-built schemas for common tool outputs.
var CommonSchemas = map[string]Schema{
	"tool_call": {
		Name: "tool_call",
		Fields: []Field{
			{Name: "tool", Type: TypeString, Required: true, MinLen: 1},
			{Name: "parameters", Type: TypeObject, Required: true},
		},
	},
	"edit": {
		Name: "edit",
		Fields: []Field{
			{Name: "file", Type: TypeString, Required: true, MinLen: 1},
			{Name: "old_text", Type: TypeString, Required: true},
			{Name: "new_text", Type: TypeString, Required: true},
		},
	},
	"task_result": {
		Name: "task_result",
		Fields: []Field{
			{Name: "status", Type: TypeString, Required: true, Enum: []string{"success", "failure", "error"}},
			{Name: "message", Type: TypeString, Required: false},
			{Name: "files_changed", Type: TypeArray, Required: false},
		},
	},
}

// FormatErrors produces a prompt-friendly error description.
func FormatErrors(result ValidationResult) string {
	if result.Valid {
		return ""
	}
	var b strings.Builder
	b.WriteString("Output validation failed:\n")
	for _, e := range result.Errors {
		fmt.Fprintf(&b, "  - %s: %s\n", e.Path, e.Message)
	}
	return b.String()
}

// ExtractJSON attempts to find and extract JSON from a string that may
// contain surrounding text (common with LLM outputs).
func ExtractJSON(text string) (string, bool) {
	// Try the whole string first
	text = strings.TrimSpace(text)
	if json.Valid([]byte(text)) {
		return text, true
	}

	// Look for JSON in code blocks
	if idx := strings.Index(text, "```json"); idx >= 0 {
		start := idx + 7
		if end := strings.Index(text[start:], "```"); end >= 0 {
			candidate := strings.TrimSpace(text[start : start+end])
			if json.Valid([]byte(candidate)) {
				return candidate, true
			}
		}
	}

	// Look for first { ... last }
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		candidate := text[start : end+1]
		if json.Valid([]byte(candidate)) {
			return candidate, true
		}
	}

	return "", false
}
