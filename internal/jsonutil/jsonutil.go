// Package jsonutil provides shared JSON parsing utilities for safe extraction
// of JSON from mixed-format outputs (e.g., markdown code fences) and typed
// marshaling helpers that eliminate interface{} in serialization paths.
package jsonutil

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExtractFromMarkdown strips markdown code fences and extracts JSON into target.
// Falls back to brace-matching if the outer content isn't valid JSON.
// This is used to parse LLM outputs that may wrap JSON in ```json blocks.
func ExtractFromMarkdown(raw string, target interface{}) error {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	if err := json.Unmarshal([]byte(s), target); err == nil {
		return nil
	}

	// Fallback: find outermost braces
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(s[start:end+1]), target); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no valid JSON found in input (len=%d)", len(raw))
}

// MarshalIndent is a checked wrapper around json.MarshalIndent that returns
// a descriptive error. Eliminates the _, _ = json.MarshalIndent pattern.
func MarshalIndent(v interface{}, label string) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", label, err)
	}
	return data, nil
}

// WriteJSON marshals v to indented JSON bytes. Panics on marshal failure
// (use only for types known to be serializable, e.g., typed structs).
func MustMarshal(v interface{}) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("jsonutil.MustMarshal: %v", err))
	}
	return data
}

// SafeUnmarshal unmarshals data into target, returning a wrapped error with context.
func SafeUnmarshal(data []byte, target interface{}, label string) error {
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("unmarshal %s: %w", label, err)
	}
	return nil
}
