package pairing

import (
	"encoding/hex"
	"strings"
)

func nonceToWords(nonce []byte, count int) []string {
	if count <= 0 {
		return nil
	}
	encoded := strings.ToLower(hex.EncodeToString(nonce))
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		start := i * 4
		if start+4 > len(encoded) {
			start = len(encoded) - 4
		}
		out = append(out, encoded[start:start+4])
	}
	return out
}
