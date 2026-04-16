// Package hashline — typescript.go
//
// TypeScript / JavaScript line-level hash normalization (S-U-016).
// Extends the language-agnostic ComputeTag to produce hashes that
// survive formatter-only whitespace changes — a common cause of
// false positives when agents reformat with Prettier / ESLint on
// save and every unchanged semantic line gets a new hash.
//
// Scope of this implementation:
//
//  1. File extension determines whether TypeScript-aware
//     normalization fires: .ts / .tsx / .js / .jsx / .mjs / .cjs.
//     Other extensions fall through to the original ComputeTag.
//  2. Normalization is conservative: trim trailing whitespace,
//     collapse runs of non-string spaces, and optionally strip a
//     trailing semicolon. String literals (single, double, and
//     backtick-delimited template strings) are preserved verbatim.
//  3. This is NOT a full Prettier reformatter. It catches the
//     most common format-only diffs (semicolon drift, trailing
//     whitespace, redundant spaces) without trying to reproduce
//     Prettier's bracket / quote / trailing-comma decisions.
//     For those cases the line hash will still change — matching
//     Prettier exactly would require either shelling out to the
//     real Prettier binary or re-implementing its AST-level
//     printer, neither of which fits in this scope.
//
// Measured effect per R-50fedca4 (prompt 95): TypeScript hashline
// matched or beat str_replace across 16 models; the 25 pp
// underperformance the independent replication found was Python-
// specific (not addressed here). Go's gofmt produces deterministic
// output so the existing language-agnostic ComputeTag already
// works well for Go; Rust rustfmt is similar and is handled the
// same way.
package hashline

import (
	"path/filepath"
	"strings"
)

// tsAwareExtensions lists file suffixes whose lines pass through
// tsNormalizeLine before hashing. Kept small + explicit so the
// normalization doesn't creep into languages with different
// whitespace semantics (e.g. Python where indentation is load-
// bearing).
var tsAwareExtensions = map[string]bool{
	".ts":  true,
	".tsx": true,
	".js":  true,
	".jsx": true,
	".mjs": true,
	".cjs": true,
}

// ComputeTagForPath tags a single line using the normalization
// appropriate to the file's extension. Callers that already know
// their context is TS/JS can call tsNormalizeLine directly and
// feed the result to ComputeTag; callers that iterate a mixed
// tree of files should use this helper so each line gets the
// right treatment.
//
// Behavior:
//   - TS/JS extensions: content is passed through tsNormalizeLine
//     (trim + collapse + optional semicolon strip) before
//     ComputeTag.
//   - Everything else: forwards to ComputeTag unchanged. Go,
//     Rust, JSON, YAML, etc. keep their existing behavior so
//     adding this function is strictly additive.
func ComputeTagForPath(path, content string) Tag {
	ext := strings.ToLower(filepath.Ext(path))
	if tsAwareExtensions[ext] {
		return ComputeTag(tsNormalizeLine(content))
	}
	return ComputeTag(content)
}

// TagFileForPath is TagFile with per-extension normalization.
// Produces a TaggedFile whose Lines carry the RAW content but
// whose Tag values were computed off the normalized form, so
// downstream Render() / RenderRange() output stays faithful to
// what the file actually contains while the tags themselves
// survive formatter-only whitespace churn.
func TagFileForPath(path string) (*TaggedFile, error) {
	tf, err := TagFile(path)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	if !tsAwareExtensions[ext] {
		return tf, nil
	}
	for i := range tf.Lines {
		tf.Lines[i].Tag = ComputeTag(tsNormalizeLine(tf.Lines[i].Content))
	}
	return tf, nil
}

// tsNormalizeLine trims and collapses a TS/JS source line while
// preserving string literal contents verbatim. Applied before
// hashing so formatter-only edits (adding / removing optional
// trailing semicolons, collapsing double spaces, trimming
// end-of-line whitespace) don't perturb the line's hash.
//
// What's normalized:
//   - Trailing whitespace removed.
//   - Runs of 2+ spaces OUTSIDE strings collapsed to single space.
//     Leading indentation is NOT collapsed (preserves column-sensitive
//     diff tooling and reads).
//   - A single trailing semicolon (outside a string) is stripped.
//     This is the main semi-vs-no-semi drift source on Prettier-
//     formatted projects.
//
// What's NOT normalized (deliberately):
//   - Quote style (single ↔ double). Prettier projects typically
//     enforce one; normalizing would mask real edits that switch
//     quote style for a reason (e.g., JSX attribute values).
//   - Trailing commas in arrays / objects. Can be load-bearing
//     for some bundler configs.
//   - Indent character / width. Changing tabs↔spaces is a real
//     edit operators should see.
//   - JSX content. Whitespace inside JSX text is often meaningful
//     (e.g., the space between {foo}{bar} controls rendering).
//
// Implementation is a small state machine so string / template
// contents skip the collapse pass cleanly. Backslash escapes
// inside strings are honored (so `\"` inside a double-quoted
// string doesn't end the string).
func tsNormalizeLine(line string) string {
	line = strings.TrimRight(line, " \t\r\n")

	// If the entire line is just whitespace, trimming already
	// produced "". Bail before the state machine to avoid any
	// subtle empty-string branching.
	if line == "" {
		return line
	}

	// First pass: collapse runs of 2+ internal spaces outside
	// strings. Preserve leading indentation by scanning past it
	// before entering the state machine.
	indentEnd := 0
	for indentEnd < len(line) {
		c := line[indentEnd]
		if c != ' ' && c != '\t' {
			break
		}
		indentEnd++
	}
	indent := line[:indentEnd]
	body := line[indentEnd:]

	var out strings.Builder
	out.Grow(len(body))
	out.WriteString(indent)

	inStr := byte(0) // 0 when outside string, else opening quote char
	escape := false
	prevSpace := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inStr != 0 {
			out.WriteByte(c)
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == inStr {
				inStr = 0
			}
			prevSpace = false
			continue
		}
		switch c {
		case '"', '\'', '`':
			inStr = c
			out.WriteByte(c)
			prevSpace = false
			continue
		case ' ':
			if prevSpace {
				continue // drop collapsed duplicate
			}
			prevSpace = true
			out.WriteByte(c)
			continue
		default:
			prevSpace = false
			out.WriteByte(c)
		}
	}

	normalized := out.String()

	// Second pass: strip a single trailing semicolon outside a
	// string. We can reuse the state tracked above; after the
	// loop if inStr != 0 the line is an unterminated string
	// literal (common on template-string continuation lines),
	// and the ; we might see is inside the string — leave it.
	if inStr == 0 && strings.HasSuffix(normalized, ";") {
		normalized = normalized[:len(normalized)-1]
		// Re-trim whitespace that may now be trailing (e.g.
		// "foo ;" -> "foo ").
		normalized = strings.TrimRight(normalized, " \t")
	}

	return normalized
}
