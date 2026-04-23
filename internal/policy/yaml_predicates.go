package policy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// compiledPred is a parse-time compiled predicate. Regexes and
// numeric operands are resolved when NewYAMLClient loads the
// rule file so Check never does string parsing on the hot path.
type compiledPred struct {
	Key string         // Context map key
	Op  string         // "matches" | "startswith" | "equals" | "in" | ">=" | "<=" | ">" | "<"
	Str string         // for matches/startswith/equals — raw value string
	Re  *regexp.Regexp // compiled at parse time for matches
	In  []string       // for in
	Num float64        // for >=/<=/>/<
}

// parsePredicate converts a single YAML predicate string of the
// form `<key> <op> <value>` into a compiled predicate. It
// returns an error when the operator is unknown, the operand is
// malformed, or a regex fails to compile.
//
// Supported shapes:
//
//	command matches "^npm install"
//	phase equals "execute"
//	path startswith "/workspace/"
//	tool_name in [bash, file_read, file_write]
//	budget_remaining_usd > 1.0
//	trust_level >= 3
func parsePredicate(raw string) (compiledPred, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return compiledPred{}, fmt.Errorf("empty predicate")
	}
	// Split off the key (first whitespace-delimited token).
	key, rest, ok := splitFirstField(s)
	if !ok {
		return compiledPred{}, fmt.Errorf("predicate %q: missing operator", raw)
	}
	// Detect operator. Order matters: >=, <= before >, <.
	rest = strings.TrimSpace(rest)
	switch {
	case strings.HasPrefix(rest, ">="):
		return buildNumericPred(key, ">=", strings.TrimSpace(rest[2:]), raw)
	case strings.HasPrefix(rest, "<="):
		return buildNumericPred(key, "<=", strings.TrimSpace(rest[2:]), raw)
	case strings.HasPrefix(rest, ">"):
		return buildNumericPred(key, ">", strings.TrimSpace(rest[1:]), raw)
	case strings.HasPrefix(rest, "<"):
		return buildNumericPred(key, "<", strings.TrimSpace(rest[1:]), raw)
	}
	// Word-style operators: matches | startswith | equals | in
	op, operand, ok := splitFirstField(rest)
	if !ok {
		return compiledPred{}, fmt.Errorf("predicate %q: missing operand", raw)
	}
	operand = strings.TrimSpace(operand)
	switch op {
	case "matches":
		val, err := unquote(operand)
		if err != nil {
			return compiledPred{}, fmt.Errorf("predicate %q: %w", raw, err)
		}
		re, err := regexp.Compile(val)
		if err != nil {
			return compiledPred{}, fmt.Errorf("predicate %q: invalid regex: %w", raw, err)
		}
		return compiledPred{Key: key, Op: "matches", Str: val, Re: re}, nil
	case "startswith":
		val, err := unquote(operand)
		if err != nil {
			return compiledPred{}, fmt.Errorf("predicate %q: %w", raw, err)
		}
		return compiledPred{Key: key, Op: "startswith", Str: val}, nil
	case "equals":
		val, err := unquote(operand)
		if err != nil {
			return compiledPred{}, fmt.Errorf("predicate %q: %w", raw, err)
		}
		return compiledPred{Key: key, Op: "equals", Str: val}, nil
	case "in":
		list, err := parseInList(operand)
		if err != nil {
			return compiledPred{}, fmt.Errorf("predicate %q: %w", raw, err)
		}
		return compiledPred{Key: key, Op: "in", In: list}, nil
	default:
		return compiledPred{}, fmt.Errorf("predicate %q: unknown operator %q", raw, op)
	}
}

// buildNumericPred constructs a numeric comparison predicate,
// parsing the operand as a float64.
func buildNumericPred(key, op, operand, raw string) (compiledPred, error) {
	if operand == "" {
		return compiledPred{}, fmt.Errorf("predicate %q: missing numeric operand", raw)
	}
	n, err := strconv.ParseFloat(operand, 64)
	if err != nil {
		return compiledPred{}, fmt.Errorf("predicate %q: invalid number %q: %w", raw, operand, err)
	}
	return compiledPred{Key: key, Op: op, Num: n}, nil
}

// splitFirstField splits s at the first run of whitespace and
// returns (head, tail, true). If s has no whitespace it returns
// (s, "", false).
func splitFirstField(s string) (string, string, bool) {
	for i, r := range s {
		if r == ' ' || r == '\t' {
			return s[:i], strings.TrimLeft(s[i:], " \t"), true
		}
	}
	return s, "", false
}

// unquote accepts either a bare token or a double-quoted string
// and returns the underlying string value.
func unquote(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty operand")
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		out, err := strconv.Unquote(s)
		if err != nil {
			return "", fmt.Errorf("invalid quoted string %q: %w", s, err)
		}
		return out, nil
	}
	return s, nil
}

// parseInList parses `[a, b, c]` or `["a", "b"]` into a slice of
// strings. Whitespace around entries is trimmed.
func parseInList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("`in` list must be wrapped in [ ]: %q", s)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []string{}, nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := unquote(p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// evalPred evaluates a compiled predicate against a Context map.
// Missing keys and type mismatches evaluate to false; the engine
// never panics on unexpected context shapes.
func evalPred(p compiledPred, ctx map[string]any) bool {
	raw, ok := ctx[p.Key]
	if !ok {
		return false
	}
	switch p.Op {
	case "matches":
		s, ok := coerceString(raw)
		if !ok {
			return false
		}
		return p.Re.MatchString(s)
	case "startswith":
		s, ok := coerceString(raw)
		if !ok {
			return false
		}
		return strings.HasPrefix(s, p.Str)
	case "equals":
		s, ok := coerceString(raw)
		if !ok {
			return false
		}
		return s == p.Str
	case "in":
		s, ok := coerceString(raw)
		if !ok {
			return false
		}
		for _, v := range p.In {
			if v == s {
				return true
			}
		}
		return false
	case ">=":
		n, ok := coerceFloat(raw)
		if !ok {
			return false
		}
		return n >= p.Num
	case "<=":
		n, ok := coerceFloat(raw)
		if !ok {
			return false
		}
		return n <= p.Num
	case ">":
		n, ok := coerceFloat(raw)
		if !ok {
			return false
		}
		return n > p.Num
	case "<":
		n, ok := coerceFloat(raw)
		if !ok {
			return false
		}
		return n < p.Num
	default:
		return false
	}
}

// coerceString coerces a Context value to string when possible.
// Only genuine strings and stringer-safe primitives are accepted;
// unknown shapes return ok=false so the predicate yields false.
func coerceString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case fmt.Stringer:
		return x.String(), true
	default:
		return "", false
	}
}

// coerceFloat coerces a Context value to float64. JSON numbers
// unmarshal to float64 by default; YAML unmarshal to int or
// float64. Both int and float variants and numeric strings are
// accepted. All other shapes return ok=false.
func coerceFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case string:
		n, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
