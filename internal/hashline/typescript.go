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

// tsNormalizeLine normalizes a TS/JS source line before hashing.
// Scope: ONLY trailing-whitespace trim. Anything more aggressive
// introduces false-equal hashes:
//
//   - Collapsing internal spaces inside multi-line template
//     literals (SQL/HTML/GraphQL templates) would equate
//     "  select  *" with "  select *" even though both are
//     string data with different rendered output.
//   - Collapsing inside regex literals (`/a  b/`) changes what
//     the regex matches.
//   - Collapsing inside JSX text (`<pre>a  b</pre>`) changes
//     what the browser renders.
//   - Stripping a trailing `;` collapses empty statements like
//     `while (ready);` with `while (ready)` — different control
//     flow.
//
// All of those would silently lose edits that
// ComputeTagForPath/TagFileForPath is supposed to detect. An
// accurate broader normalization requires a real JS parser;
// until Stoke ships one (or shells out to Prettier --check),
// trailing-whitespace trim is the safe subset.
//
// Trailing-whitespace trim alone still catches the most common
// formatter-only churn source: editors that strip end-of-line
// whitespace on save (a near-universal default) would otherwise
// re-hash every unchanged line on any touched file.
func tsNormalizeLine(line string) string {
	return strings.TrimRight(line, " \t\r\n")
}
