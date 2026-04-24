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
// int / int8 / int16 / int32 / int64 / uint variants /
// float32 / float64. Mixed types fall through to float64
// comparison.
//
// Integer vs integer comparison uses int64 / uint64
// arithmetic — not float64 — so values above 2^53 compare
// correctly. A prior version that float-promoted every
// input collapsed large integers into the same float,
// causing newer writes to be treated as equal and the
// older value kept.
//
// Precision ladder:
//  1. Both values fit in int64        → int64 comparison
//  2. Both values fit in uint64       → uint64 comparison
//     (handles very large unsigned
//      values beyond int64 max)
//  3. Otherwise                       → float64 fallback
//     (lossy for values > 2^53 but
//      better than erroring)
func MaxReducer(existing, incoming any) (any, error) {
	ei, eiOK := toInt64(existing)
	ii, iiOK := toInt64(incoming)
	if eiOK && iiOK {
		if ei >= ii {
			return existing, nil
		}
		return incoming, nil
	}
	// Both values may be large unsigned types that don't
	// fit in int64 — compare as uint64 exactly rather than
	// losing precision through float64.
	eu, euOK := toUint64(existing)
	iu, iuOK := toUint64(incoming)
	if euOK && iuOK {
		if eu >= iu {
			return existing, nil
		}
		return incoming, nil
	}
	ef, efOK := toFloat(existing)
	inf, ifOK := toFloat(incoming)
	if !efOK {
		return nil, fmt.Errorf("sharedmem: MaxReducer existing not numeric: %T", existing)
	}
	if !ifOK {
		return nil, fmt.Errorf("sharedmem: MaxReducer incoming not numeric: %T", incoming)
	}
	if ef >= inf {
		return existing, nil
	}
	return incoming, nil
}

// toUint64 coerces unsigned integer types to uint64 for
// MaxReducer's precise-comparison fast path on values
// beyond int64 max.
func toUint64(v any) (uint64, bool) {
	switch x := v.(type) {
	case uint:
		return uint64(x), true
	case uint8:
		return uint64(x), true
	case uint16:
		return uint64(x), true
	case uint32:
		return uint64(x), true
	case uint64:
		return x, true
	default:
		return 0, false
	}
}

// toInt64 coerces integral types to int64 for precise
// comparison. Explicitly does NOT accept float types — the
// caller has to fall through to toFloat for mixed or
// floating-point comparisons so we don't silently round a
// 1.5 down to 1.
func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint:
		// uint can exceed int64 max; only accept when it fits.
		if uint64(x) <= uint64(1<<63-1) {
			return int64(x), true // #nosec G115 -- bounded on prior line: uint64(x) <= math.MaxInt64.
		}
		return 0, false
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		if x <= uint64(1<<63-1) {
			return int64(x), true
		}
		return 0, false
	default:
		return 0, false
	}
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

// toFloat coerces common numeric types to float64 for
// MaxReducer's mixed-type fallback path. Must accept every
// type toInt64 accepts so a `MaxReducer(int16(1), 1.5)`
// call (which falls through to the float path when one side
// isn't integral) doesn't erroneously report "not numeric".
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint8:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
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
