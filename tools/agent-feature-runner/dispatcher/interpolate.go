// interpolate.go — variable interpolation for feature steps per spec
// 8 §12 item 18:
//
//   > Support ${SESSION_ID} and ${MISSION_ID} variable interpolation
//   > with prior-step output.
//
// The runner accumulates a Bindings map as it executes a scenario.
// Each tool response that the runner deems "named output" updates the
// map: e.g. r1.session.start writes Bindings["SESSION_ID"]; r1.mission.
// create writes Bindings["MISSION_ID"]. Subsequent step text is
// rewritten via Interpolate before dispatching.
//
// Syntax:
//
//   ${NAME}        -> looked up in bindings; missing key errors
//   ${NAME:-fallback}  -> fallback when NAME is unset (POSIX shape)
//
// Multi-pass interpolation is NOT performed (a value containing
// ${OTHER} stays literal); this is intentional to keep substitution
// total-order-deterministic.
package dispatcher

import (
	"fmt"
	"regexp"
)

// Bindings is the per-scenario variable map. The runner mutates it as
// each step's tool result lands.
type Bindings map[string]string

// varPattern matches ${NAME} and ${NAME:-fallback}.
var varPattern = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)(?::-([^}]*))?\}`)

// Interpolate replaces every ${NAME} or ${NAME:-fallback} in s with the
// bound value (or the fallback). Returns an error when a ${NAME}
// without fallback references an unbound variable.
func Interpolate(s string, bindings Bindings) (string, error) {
	var firstErr error
	out := varPattern.ReplaceAllStringFunc(s, func(match string) string {
		groups := varPattern.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match // unreachable for the regex above
		}
		name := groups[1]
		fallback := ""
		if len(groups) >= 3 {
			fallback = groups[2]
		}
		if v, ok := bindings[name]; ok {
			return v
		}
		// Distinguish "fallback supplied" from "fallback omitted" by
		// checking whether the original match contained the :- delim.
		if hasFallback(match) {
			return fallback
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("unbound variable %q (no :- fallback)", name)
		}
		return match
	})
	if firstErr != nil {
		return s, firstErr
	}
	return out, nil
}

// hasFallback returns true when the match contains the :- delimiter,
// signaling the author supplied a fallback (even if the fallback
// itself is empty: ${NAME:-} is a deliberate "soft empty" pattern).
func hasFallback(match string) bool {
	for i := 0; i+1 < len(match); i++ {
		if match[i] == ':' && match[i+1] == '-' {
			return true
		}
	}
	return false
}

// InterpolateAll runs Interpolate over every entry in steps. Returns
// the first error encountered (the rest of the slice is still
// processed so callers see ALL substitution failures in one pass when
// they call this in dry-run mode).
func InterpolateAll(steps []string, bindings Bindings) ([]string, error) {
	var firstErr error
	out := make([]string, len(steps))
	for i, s := range steps {
		v, err := Interpolate(s, bindings)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		out[i] = v
	}
	return out, firstErr
}
