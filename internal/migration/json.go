package migration

import "encoding/json"

// jsonMarshal is the canonical encoder for migration package payloads.
// Wraps encoding/json.Marshal so any future canonicalization (e.g.
// sorted keys, no-whitespace) can be applied package-wide in one
// place. Today it's a thin pass-through.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
