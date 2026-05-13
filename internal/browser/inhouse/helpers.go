// helpers.go: tiny JSON / base64 helpers used by client.go. Kept in
// a sibling file so client.go stays narrative-readable.

package inhouse

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// encodeJSON marshals v to a JSON string. Panics on failure are
// impossible for the shapes we feed it (plain maps + structs); the
// returned string is "" only when v itself was an empty map.
func encodeJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeInnerResult parses the `{id, result, error}` envelope CDP
// servers return inside Target.receivedMessageFromTarget. Unmarshals
// the inner Result into out and surfaces CDP error frames as Go errors.
func decodeInnerResult(msg string, out any) error {
	var inner struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(msg), &inner); err != nil {
		return fmt.Errorf("inhouse: parse inner reply: %w", err)
	}
	if inner.Error != nil {
		return fmt.Errorf("inhouse: cdp error %d: %s", inner.Error.Code, inner.Error.Message)
	}
	if out == nil || len(inner.Result) == 0 {
		return nil
	}
	return json.Unmarshal(inner.Result, out)
}

// quoteJSON returns a JSON-string-literal form of s, suitable for
// embedding inside a Runtime.evaluate expression body.
func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// decodeBase64 wraps base64.StdEncoding.DecodeString to keep client.go
// imports tidy.
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
