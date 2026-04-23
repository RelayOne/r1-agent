// Package eventlog implements a durable, hash-chained SQLite event log for
// Stoke's runtime substrate. See specs/event-log-proper.md for the design.
package eventlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Marshal produces deterministic canonical JSON bytes for v:
//
//   - UTF-8, no whitespace, no HTML escaping.
//   - Object keys sorted lexicographically (by byte order).
//   - Array order preserved.
//   - Numbers, strings, booleans, null, and nested map/slice structures
//     are supported.
//
// For equal inputs, Marshal returns byte-equal output on every call. The
// result is used as the canonical input to the hash chain: we store the same
// canonical bytes in the database so Verify can reproduce the hash without
// needing to re-normalize.
func Marshal(v any) ([]byte, error) {
	canon, err := canonicalize(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(canon); err != nil {
		return nil, fmt.Errorf("eventlog canonical: encode: %w", err)
	}
	// json.Encoder.Encode appends a trailing '\n'; strip it for byte-equality.
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	// Return a copy so the caller cannot mutate buf's backing storage if
	// we later reuse it.
	cp := make([]byte, len(out))
	copy(cp, out)
	return cp, nil
}

// canonicalize walks v via reflection and returns an equivalent value whose
// maps have been replaced with *orderedMap (an ordered key list) so that
// json.Marshal emits keys in sorted order. Passthrough for scalar types and
// slices. json.RawMessage is re-parsed into a canonical form so differing
// whitespace or key ordering in a raw payload cannot break chain equality.
func canonicalize(v any) (any, error) {
	// json.RawMessage: re-parse and re-canonicalize its contents.
	if raw, ok := v.(json.RawMessage); ok {
		if len(raw) == 0 {
			return nil, nil
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("eventlog canonical: re-parse RawMessage: %w", err)
		}
		return canonicalize(decoded)
	}
	// json.Number: preserve as-is (json.Marshal handles them correctly).
	if num, ok := v.(json.Number); ok {
		return num, nil
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil, nil
	}
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
		return canonicalize(rv.Elem().Interface())
	case reflect.Map:
		// Only string-keyed maps are valid JSON; other keys error out.
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("eventlog canonical: unsupported map key type %s", rv.Type().Key())
		}
		n := rv.Len()
		keys := make([]string, 0, n)
		valMap := make(map[string]any, n)
		iter := rv.MapRange()
		for iter.Next() {
			k := iter.Key().String()
			val, err := canonicalize(iter.Value().Interface())
			if err != nil {
				return nil, err
			}
			keys = append(keys, k)
			valMap[k] = val
		}
		sort.Strings(keys)
		return &orderedMap{keys: keys, vals: valMap}, nil
	case reflect.Slice, reflect.Array:
		// []byte is a special case: encoding/json emits it as base64, which
		// is already deterministic.
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			// Copy to a []byte value so json.Marshal takes the fast path.
			b := make([]byte, rv.Len())
			reflect.Copy(reflect.ValueOf(b), rv)
			return b, nil
		}
		n := rv.Len()
		out := make([]any, n)
		for i := 0; i < n; i++ {
			val, err := canonicalize(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			out[i] = val
		}
		return out, nil
	case reflect.Struct:
		// Round-trip through json to flatten struct-tag rules, then
		// canonicalize the resulting map.
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("eventlog canonical: struct marshal: %w", err)
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("eventlog canonical: struct re-parse: %w", err)
		}
		return canonicalize(decoded)
	default:
		// Scalars (bool, int*, float*, string, nil, etc.): passthrough.
		return v, nil
	}
}

// orderedMap is a map whose json.Marshal output emits keys in the order
// listed in keys. The field is private so only canonicalize() constructs it.
type orderedMap struct {
	keys []string
	vals map[string]any
}

// MarshalJSON implements json.Marshaler.
func (m *orderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		// Encode key as a JSON string without HTML escaping.
		kb, err := encodeJSONString(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := encodeCanonicalValue(m.vals[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// encodeCanonicalValue marshals v with HTML escaping disabled. Nested
// orderedMaps recurse through MarshalJSON automatically.
func encodeCanonicalValue(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// encodeJSONString returns a JSON-encoded string without HTML escaping.
func encodeJSONString(s string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}
