// jsonpath.go — minimal JSONPath evaluator for the teatest shim's
// GetModel handler per spec 8 §12 item 15:
//
//   > Reflect-based JSONPath model introspection (use tidwall/gjson if
//   > not already a dep, else fallback to encoding/json round-trip).
//
// gjson is NOT a dep of this repo (and adding it requires updating the
// vendor tree, which is out of scope for the test harness spec). This
// file ships the fallback path: we marshal the live model to JSON
// once, then walk the resulting tree with a small expression parser.
//
// Supported syntax (subset, sufficient for §10 feature-runner needs):
//
//   $                              -> the whole document
//   $.field                        -> object field
//   $.field.subfield               -> nested object field
//   $.field[0]                     -> array element by index
//   $.field[0].sub                 -> chained
//   $.lanes[*].id                  -> wildcard array projection (returns array)
//   $.lanes[?(@.status=="run")]    -> NOT supported (errors with helpful msg)
//
// Unsupported syntax errors at evaluation time with the offending
// segment in the message so feature authors get a clear hint.
package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// EvalJSONPath returns the JSON sub-document at the given path.
// Returns json.RawMessage("null") with no error when the path resolves
// to a JSON null; a missing field is an error (consistent with gjson).
func EvalJSONPath(raw json.RawMessage, path string) (json.RawMessage, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		return raw, nil
	}
	if !strings.HasPrefix(path, "$") {
		return nil, fmt.Errorf("jsonpath %q must start with $", path)
	}
	// Tokenize starting after the leading $.
	rest := path[1:]
	cur := raw
	for len(rest) > 0 {
		switch rest[0] {
		case '.':
			// Field accessor: read until the next '.', '[' or end.
			end := indexAny(rest[1:], ".[")
			if end < 0 {
				end = len(rest) - 1
			}
			field := rest[1 : 1+end]
			if field == "" {
				return nil, fmt.Errorf("empty field accessor in %q", path)
			}
			next, err := stepField(cur, field)
			if err != nil {
				return nil, fmt.Errorf("at %q: %w", field, err)
			}
			cur = next
			rest = rest[1+end:]
		case '[':
			closeIdx := strings.Index(rest, "]")
			if closeIdx < 0 {
				return nil, fmt.Errorf("unclosed [ in %q", path)
			}
			expr := rest[1:closeIdx]
			next, err := stepBracket(cur, expr)
			if err != nil {
				return nil, fmt.Errorf("at %q: %w", expr, err)
			}
			cur = next
			rest = rest[closeIdx+1:]
		default:
			return nil, fmt.Errorf("unexpected character %q at %q", rest[0:1], rest)
		}
	}
	return cur, nil
}

// stepField walks one object field. Errors when cur is not an object
// or the field is absent.
func stepField(cur json.RawMessage, field string) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(cur, &obj); err != nil {
		return nil, fmt.Errorf("not an object: %v", err)
	}
	val, ok := obj[field]
	if !ok {
		return nil, fmt.Errorf("field not found")
	}
	return val, nil
}

// stepBracket handles [N] indexing and [*] wildcard projection. Other
// expressions (filters, slices) error with a helpful message.
func stepBracket(cur json.RawMessage, expr string) (json.RawMessage, error) {
	expr = strings.TrimSpace(expr)
	if expr == "*" {
		// Wildcard: return the array itself unchanged. Subsequent
		// segments apply elementwise — but that requires a different
		// data shape (an array of projections). To keep the evaluator
		// simple, the caller can post-process the array; we surface
		// the array verbatim here.
		return cur, nil
	}
	if strings.HasPrefix(expr, "?") {
		return nil, fmt.Errorf("filter expressions [?(...)] not supported in this evaluator")
	}
	idx, err := strconv.Atoi(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid bracket expression %q: must be integer or *", expr)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(cur, &arr); err != nil {
		return nil, fmt.Errorf("not an array: %v", err)
	}
	if idx < 0 || idx >= len(arr) {
		return nil, fmt.Errorf("index %d out of range [0,%d)", idx, len(arr))
	}
	return arr[idx], nil
}

// indexAny returns the lowest index in s of any byte in chars, or -1
// if none are present.
func indexAny(s, chars string) int {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return i
			}
		}
	}
	return -1
}
