package jsonutil

import (
	"encoding/json"
	"strings"
	"unicode"
)

// ExtractJSONObject extracts and parses the first top-level JSON object
// from raw LLM output. It handles the common failure modes:
//   - Markdown fences: ```json { ... } ``` or bare ``` fences
//   - Preamble prose: "Sure, here's the JSON you asked for: {...}"
//   - Postamble prose: "... Let me know if you want changes!"
//   - UTF-8 BOM
//   - JavaScript-style trailing commas (before ] or })
//   - Leading/trailing whitespace
//
// Returns the cleaned JSON blob as json.RawMessage. Returns (nil, err)
// if no balanced object is found or the cleaned blob fails to parse.
//
// This is the robust replacement for ExtractFromMarkdown. Callers should
// prefer ExtractJSONInto for the common "extract and unmarshal" flow.
func ExtractJSONObject(raw string) (json.RawMessage, error) {
	cleaned := cleanLLMJSON(raw)
	start, end := findBalancedObject(cleaned)
	if start < 0 {
		return nil, &ExtractError{Raw: raw, Reason: "no balanced JSON object found"}
	}
	blob := cleaned[start : end+1]
	blob = removeTrailingCommas(blob)
	// Validate via a round-trip parse.
	var v interface{}
	if err := json.Unmarshal([]byte(blob), &v); err == nil {
		return json.RawMessage(blob), nil
	}
	// Repair pass 1: missing commas between array/object elements.
	repaired := insertMissingCommas(blob)
	if repaired != blob {
		if err := json.Unmarshal([]byte(repaired), &v); err == nil {
			return json.RawMessage(repaired), nil
		}
	}
	// Repair pass 1.5: bare identifier tokens inside arrays. LLM
	// output frequently emits `"dependencies": [T5, T6]` when it
	// means `["T5", "T6"]` — the model elides the quotes on what
	// looks to it like symbolic identifiers. Quote any bare token
	// that sits alone between `[` / `,` and `,` / `]` and matches
	// an identifier shape. Does NOT touch numbers, null, true,
	// false — those are legitimate bare JSON values.
	quoted := quoteBareIdentifiersInArrays(blob)
	if quoted != blob {
		if err := json.Unmarshal([]byte(quoted), &v); err == nil {
			return json.RawMessage(quoted), nil
		}
		// Also try in combination with comma repair.
		combined := insertMissingCommas(quoted)
		if combined != quoted {
			if err := json.Unmarshal([]byte(combined), &v); err == nil {
				return json.RawMessage(combined), nil
			}
		}
	}
	// Repair pass 2: truncated output — the model hit max_tokens and
	// the JSON just stops mid-element. closeTruncatedJSON appends
	// closing quotes/brackets/braces to make it structurally valid.
	// This loses whatever was being written at the truncation point,
	// but preserves all the complete sessions/tasks/ACs before it.
	//
	// Carry the bare-identifier quoting forward: we run the close /
	// comma passes on BOTH the raw blob and the quoted variant so a
	// document with both `[T5]` drift and a truncation is recoverable
	// in one sweep instead of either repair winning on its own.
	for _, base := range []string{blob, quoted} {
		if base == "" {
			continue
		}
		closed := closeTruncatedJSON(base)
		if closed == base {
			continue
		}
		closed = removeTrailingCommas(closed)
		if err := json.Unmarshal([]byte(closed), &v); err == nil {
			return json.RawMessage(closed), nil
		}
		// Try missing-comma repair on the closed version too.
		closedRepaired := insertMissingCommas(closed)
		if closedRepaired != closed {
			if err := json.Unmarshal([]byte(closedRepaired), &v); err == nil {
				return json.RawMessage(closedRepaired), nil
			}
		}
		// And the quote-plus-close combo.
		closedQuoted := quoteBareIdentifiersInArrays(closed)
		if closedQuoted != closed {
			if err := json.Unmarshal([]byte(closedQuoted), &v); err == nil {
				return json.RawMessage(closedQuoted), nil
			}
			combo := insertMissingCommas(closedQuoted)
			if combo != closedQuoted {
				if err := json.Unmarshal([]byte(combo), &v); err == nil {
					return json.RawMessage(combo), nil
				}
			}
		}
	}
	// All repair attempts failed. Return the original error.
	if err := json.Unmarshal([]byte(blob), &v); err != nil {
		return nil, &ExtractError{Raw: raw, Blob: blob, Reason: "unmarshal: " + err.Error()}
	}
	return json.RawMessage(blob), nil
}

// ExtractJSONInto is a convenience wrapper: extract then Unmarshal in one
// call. Returns the cleaned blob alongside the error for diagnostics.
func ExtractJSONInto(raw string, out interface{}) ([]byte, error) {
	blob, err := ExtractJSONObject(raw)
	if err != nil {
		return nil, err
	}
	if uErr := json.Unmarshal(blob, out); uErr != nil {
		return blob, &ExtractError{Raw: raw, Blob: string(blob), Reason: "unmarshal into target: " + uErr.Error()}
	}
	return blob, nil
}

// ExtractError wraps parse failures with enough context for callers to
// produce useful error messages — including a bounded preview of the
// raw response for easier debugging when a model drifts off-format.
type ExtractError struct {
	Raw    string
	Blob   string
	Reason string
}

func (e *ExtractError) Error() string {
	preview := e.Raw
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}
	return "extract JSON: " + e.Reason + " (first 200 chars: " + preview + ")"
}

// cleanLLMJSON removes the most common garbage around LLM JSON output:
// BOM, markdown fences, leading/trailing whitespace.
func cleanLLMJSON(raw string) string {
	s := raw
	s = strings.TrimPrefix(s, "\ufeff") // UTF-8 BOM
	s = strings.TrimSpace(s)

	// Strip markdown fences. Handle ```json and bare ``` styles.
	if strings.HasPrefix(s, "```") {
		if nl := strings.Index(s, "\n"); nl > 0 {
			s = s[nl+1:]
		} else {
			s = strings.TrimPrefix(s, "```")
		}
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	return s
}

// findBalancedObject locates the first balanced { ... } block, ignoring
// braces inside quoted strings. If the input is truncated (starts with
// { but runs out before closing all braces), the function closes the
// unclosed containers by appending the necessary } characters and
// returns the patched string's bounds. This handles LLM output that
// hit max_tokens mid-JSON — the truncated suffix is lost but the
// structural prefix is salvageable.
func findBalancedObject(s string) (int, int) {
	start := -1
	depth := 0
	inStr := false
	escaped := false
	for i, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if inStr {
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inStr = false
			}
			continue
		}
		switch r {
		case '"':
			inStr = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				return start, i
			}
		}
	}
	// Truncation recovery: if we found a start '{' but depth > 0 at
	// EOF, the model hit max_tokens mid-JSON. Rather than returning
	// -1,-1 and halting the whole pipeline, return the bounds of
	// what we have. The caller's json.Unmarshal will still fail, but
	// the truncation-aware ExtractJSONObject path can then try
	// closing the unclosed braces before parsing.
	if start >= 0 && depth > 0 {
		return start, len(s) - 1
	}
	return -1, -1
}

// closeTruncatedJSON appends the necessary closing characters to make
// a truncated JSON object structurally valid. It tracks a stack of
// container types ({/[) so closures are emitted in the correct order.
// If the text was truncated mid-string or mid-value, the string is
// closed and the incomplete key-value pair is dropped.
func closeTruncatedJSON(blob string) string {
	var stack []byte // '{' or '['
	inStr := false
	escaped := false
	for _, r := range blob {
		if escaped {
			escaped = false
			continue
		}
		if inStr {
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inStr = false
			}
			continue
		}
		switch r {
		case '"':
			inStr = true
		case '{':
			stack = append(stack, '{')
		case '}':
			if len(stack) > 0 && stack[len(stack)-1] == '{' {
				stack = stack[:len(stack)-1]
			}
		case '[':
			stack = append(stack, '[')
		case ']':
			if len(stack) > 0 && stack[len(stack)-1] == '[' {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if len(stack) == 0 && !inStr {
		return blob // already balanced
	}
	var b strings.Builder
	b.WriteString(blob)
	// Close an open string literal.
	if inStr {
		b.WriteString(`"`)
	}
	// Close containers in reverse order (LIFO).
	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i] {
		case '{':
			b.WriteByte('}')
		case '[':
			b.WriteByte(']')
		}
	}
	return b.String()
}

// insertMissingCommas inserts a comma between two adjacent JSON
// elements when the model forgot the separator. It handles these
// patterns (outside of string literals):
//
//	}"    -> },"      (object followed by next key/element)
//	]"    -> ],"      (array  followed by next key/element)
//	}{    -> },{      (two objects in an array, no comma)
//	][    -> ],[      (two arrays  in an array, no comma)
//	]{    -> ],{      (array then object in an array, no comma)
//	}[    -> },[      (object then array in an array, no comma)
//
// It is conservative: it only inserts commas when the left-hand
// character actually closes a container, never inside strings, and
// never when the next non-whitespace character is one of , ] } which
// would be legitimate continuations. If the input was already valid,
// the output equals the input and the caller's second unmarshal pass
// is a no-op. This is the minimal repair for long LLM-emitted JSON
// that truncates or drops a separator at container boundaries.
func insertMissingCommas(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	inStr := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		b.WriteByte(c)
		if escaped {
			escaped = false
			continue
		}
		if inStr {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			continue
		}
		if c == '}' || c == ']' {
			// Look ahead past whitespace to the next non-space char.
			j := i + 1
			for j < len(s) && unicode.IsSpace(rune(s[j])) {
				j++
			}
			if j >= len(s) {
				continue
			}
			next := s[j]
			// Next char starts a new element if it opens a string,
			// an object, or an array. The valid continuations after
			// a closing brace/bracket are , ] } (end of parent) or
			// EOF — none of which trigger the repair.
			if next == '"' || next == '{' || next == '[' {
				b.WriteByte(',')
			}
		}
	}
	return b.String()
}

// quoteBareIdentifiersInArrays scans s and wraps bare identifier
// tokens inside array literals in double quotes. Handles the common
// LLM drift of emitting `"dependencies": [T5, T6, T7]` or
// `"refs": [S1]` where the emitted value should be a string.
//
// Rules:
//   - Only operates inside `[ ... ]` regions (after colons or
//     nested).
//   - A bare token is `[A-Za-z_][A-Za-z0-9_-]*` that is NOT
//     surrounded by quotes, NOT a JSON keyword (true/false/null),
//     NOT a number, and sits between `[` or `,` and `,` or `]`.
//   - Strings, numbers, objects, nested arrays are left alone.
//
// The function is intentionally conservative — the target pattern
// is task-ID-shaped tokens (T5, S2, AC1); any other bare tokens
// that fit the identifier regex are also quoted, which is safe
// because true/false/null/numbers short-circuit before this
// reaches the wrapping step.
func quoteBareIdentifiersInArrays(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 32)
	inStr := false
	escaped := false
	arrayDepth := 0
	i := 0
	for i < len(s) {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			i++
			continue
		}
		if c == '[' {
			arrayDepth++
			b.WriteByte(c)
			i++
			continue
		}
		if c == ']' {
			if arrayDepth > 0 {
				arrayDepth--
			}
			b.WriteByte(c)
			i++
			continue
		}
		// Inside an array context, check for a bare identifier token
		// that needs quoting. Valid start: position immediately after
		// `[` or `,` (skipping whitespace).
		if arrayDepth > 0 && isIdentStart(c) {
			// Walk backward past whitespace to confirm we're right
			// after a `[` or `,`.
			k := len(s)
			out := b.String()
			_ = k
			_ = out
			prev := prevNonSpace(b.String())
			if prev == '[' || prev == ',' {
				end := i
				for end < len(s) && isIdentPart(s[end]) {
					end++
				}
				token := s[i:end]
				// Skip JSON keywords (true/false/null) and anything
				// that already starts with a digit (numbers handled
				// by the default path).
				low := strings.ToLower(token)
				if low == "true" || low == "false" || low == "null" {
					b.WriteString(token)
					i = end
					continue
				}
				// Next non-space char after token must be `,` or `]`
				// for this to be a bare-array-element scenario.
				j := end
				for j < len(s) && unicode.IsSpace(rune(s[j])) {
					j++
				}
				if j < len(s) && (s[j] == ',' || s[j] == ']') {
					b.WriteByte('"')
					b.WriteString(token)
					b.WriteByte('"')
					i = end
					continue
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// isIdentStart reports whether c can start a JSON-adjacent bare
// identifier token that quoteBareIdentifiersInArrays should rescue.
// Numbers start paths (`.`, digits) are intentionally excluded so
// real numeric literals parse as-is.
func isIdentStart(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
}

// isIdentPart reports whether c can continue an identifier.
func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-'
}

// prevNonSpace returns the last non-whitespace byte of s, or 0 if
// none. Used by quoteBareIdentifiersInArrays to decide whether we
// just crossed a `[` or `,` boundary.
func prevNonSpace(s string) byte {
	for i := len(s) - 1; i >= 0; i-- {
		if !unicode.IsSpace(rune(s[i])) {
			return s[i]
		}
	}
	return 0
}

// removeTrailingCommas strips JavaScript-style trailing commas (before
// ']' or '}'). Skips commas inside quoted strings. Leaves everything
// else untouched so valid JSON round-trips unchanged.
func removeTrailingCommas(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if inStr {
			if c == '\\' {
				b.WriteByte(c)
				escaped = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			b.WriteByte(c)
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(s) && unicode.IsSpace(rune(s[j])) {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				// Drop the comma.
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}
