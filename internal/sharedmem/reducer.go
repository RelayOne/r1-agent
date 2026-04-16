// Package sharedmem — reducer.go
//
// Built-in reducers for the SemanticInsert write path. A reducer
// takes the block's current Value and the incoming write's Value
// and returns the merged result.
//
// The three built-ins cover the common collection shapes:
//
//   - AddReducer     merges two []any lists by append (duplicates
//                    preserved). Suitable for "append-only log"
//                    blocks.
//   - UnionReducer   merges two []any lists by set-union
//                    (duplicates de-duped). Suitable for "set of
//                    tags / labels / user IDs" blocks.
//   - MaxReducer     keeps the larger of two numbers (handles int,
//                    int64, float64). Suitable for "maximum
//                    observed counter" blocks.
//
// Custom reducers can be registered via MemoryStore.RegisterReducer.
// This package intentionally ships a small, conservative set of
// built-ins rather than trying to cover every application-specific
// merge rule — that belongs in the application.
package sharedmem

import "fmt"

// Reducer merges an existing value with a new value, returning
// the result. An error short-circuits the Insert write.
type Reducer func(existing, incoming any) (any, error)

// AddReducer appends incoming onto existing, both treated as
// []any. Returns ErrReducerBadType if either value isn't a slice.
func AddReducer(existing, incoming any) (any, error) {
	ex, err := asSlice(existing, "existing")
	if err != nil {
		return nil, err
	}
	in, err := asSlice(incoming, "incoming")
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(ex)+len(in))
	out = append(out, ex...)
	out = append(out, in...)
	return out, nil
}

// UnionReducer computes set-union of existing + incoming, both
// []any. Equality via Go's == operator — works for comparable
// element types (strings, numbers, bools, interfaces wrapping
// them). Composite element types (maps, slices) aren't
// comparable and will fail; callers with composite elements
// should register a custom reducer.
func UnionReducer(existing, incoming any) (any, error) {
	ex, err := asSlice(existing, "existing")
	if err != nil {
		return nil, err
	}
	in, err := asSlice(incoming, "incoming")
	if err != nil {
		return nil, err
	}
	seen := make(map[any]struct{}, len(ex))
	out := make([]any, 0, len(ex)+len(in))
	for _, v := range ex {
		if !isComparable(v) {
			return nil, fmt.Errorf("sharedmem: UnionReducer element not comparable: %T", v)
		}
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	for _, v := range in {
		if !isComparable(v) {
			return nil, fmt.Errorf("sharedmem: UnionReducer element not comparable: %T", v)
		}
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out, nil
}

// MaxReducer keeps the larger of two numeric values. Accepts
// int, int64, float64 (and uses float promotion when mixed).
// Returns an error if either value isn't numeric.
func MaxReducer(existing, incoming any) (any, error) {
	ef, ok1 := toFloat(existing)
	inf, ok2 := toFloat(incoming)
	if !ok1 {
		return nil, fmt.Errorf("sharedmem: MaxReducer existing not numeric: %T", existing)
	}
	if !ok2 {
		return nil, fmt.Errorf("sharedmem: MaxReducer incoming not numeric: %T", incoming)
	}
	if ef >= inf {
		return existing, nil
	}
	return incoming, nil
}

// asSlice coerces v into []any. Accepts []any directly and
// refuses everything else — this package doesn't try to convert
// []string / []int / etc. silently because that's usually a
// caller bug (the block's Value should already be the correct
// shape).
func asSlice(v any, name string) ([]any, error) {
	if v == nil {
		return nil, nil
	}
	s, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("sharedmem: %s is not []any: %T", name, v)
	}
	return s, nil
}

// toFloat coerces common numeric types to float64 for MaxReducer.
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	default:
		return 0, false
	}
}

// isComparable reports whether v can be used as a Go map key.
// Comparable types are safe; slices, maps, and funcs aren't.
// Used by UnionReducer to fail cleanly instead of panicking
// when a caller tries to union non-comparable elements.
//
// Implementation: probe by inserting into a throwaway map and
// catching the runtime panic ("hash of unhashable type") via
// a deferred recover. The named return `ok` is set to true on
// the happy path; a recovered panic leaves it at its zero value
// (false).
func isComparable(v any) (ok bool) {
	defer func() { _ = recover() }()
	m := map[any]struct{}{}
	m[v] = struct{}{}
	_ = m
	return true
}
