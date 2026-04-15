// Package plan — integrity_helpers.go
//
// Shared helpers used across integrity_*.go ecosystem files.
package plan

import (
	"bytes"
	"encoding/json"
)

// jsonUnmarshalLenient parses JSON that may contain trivial LLM/hand-
// written drift (trailing commas, // and /* */ comments). Falls back
// to the strict parser when the lenient cleanup is a no-op so valid
// JSON takes the fast path.
func jsonUnmarshalLenient(data []byte, out interface{}) error {
	if err := json.Unmarshal(data, out); err == nil {
		return nil
	}
	cleaned := stripJSONComments(data)
	cleaned = stripTrailingCommas(cleaned)
	return json.Unmarshal(cleaned, out)
}

// stripJSONComments removes // and /* */ comments from a JSON-ish
// buffer. Skips content inside string literals.
func stripJSONComments(data []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(data))
	n := len(data)
	inStr := false
	escape := false
	for i := 0; i < n; i++ {
		c := data[i]
		if inStr {
			out.WriteByte(c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < n {
			if data[i+1] == '/' {
				for i < n && data[i] != '\n' {
					i++
				}
				if i < n {
					out.WriteByte('\n')
				}
				continue
			}
			if data[i+1] == '*' {
				i += 2
				for i+1 < n && !(data[i] == '*' && data[i+1] == '/') {
					i++
				}
				i++
				continue
			}
		}
		out.WriteByte(c)
	}
	return out.Bytes()
}

// stripTrailingCommas removes JS-style trailing commas (before ] or })
// from a JSON-ish buffer. Skips content inside string literals.
func stripTrailingCommas(data []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(data))
	n := len(data)
	inStr := false
	escape := false
	for i := 0; i < n; i++ {
		c := data[i]
		if inStr {
			out.WriteByte(c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out.WriteByte(c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < n && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			if j < n && (data[j] == ']' || data[j] == '}') {
				continue // drop the comma
			}
		}
		out.WriteByte(c)
	}
	return out.Bytes()
}
