package daemondisco

import (
	"encoding/hex"
	"testing"
)

func TestToken_Random32Bytes(t *testing.T) {
	tok, err := MintToken()
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	raw, err := hex.DecodeString(tok)
	if err != nil {
		t.Fatalf("token not valid hex: %v (got %q)", err, tok)
	}
	if len(raw) != TokenBytes {
		t.Errorf("token decoded length = %d, want %d", len(raw), TokenBytes)
	}
}

func TestToken_HexEncoded(t *testing.T) {
	tok, err := MintToken()
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if got := len(tok); got != TokenBytes*2 {
		t.Errorf("token length = %d chars, want %d", got, TokenBytes*2)
	}
	for i, r := range tok {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !ok {
			t.Fatalf("token[%d]=%q is not lowercase hex; full=%q", i, r, tok)
		}
	}
}

func TestToken_Distinct(t *testing.T) {
	// Every MintToken call must regenerate (no persistence). 1000
	// calls with no collisions effectively rules out a stuck PRNG.
	const N = 1000
	seen := make(map[string]struct{}, N)
	for i := 0; i < N; i++ {
		tok, err := MintToken()
		if err != nil {
			t.Fatalf("MintToken[%d]: %v", i, err)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token after %d mints: %q", i, tok)
		}
		seen[tok] = struct{}{}
	}
}
