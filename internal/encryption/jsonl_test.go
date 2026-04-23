package encryption

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

func freshKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestEncryptLine_RoundTrip(t *testing.T) {
	key := freshKey(t)
	cases := []string{
		`{"event":"tool_use","name":"Read","args":{"path":"/tmp/x"}}`,
		`{"text":"hello — unicode ✓ ☃"}`,
		strings.Repeat("a", 64*1024), // 64KB stress
	}
	for _, plain := range cases {
		enc, err := EncryptLine(key, []byte(plain))
		if err != nil {
			t.Fatalf("EncryptLine: %v", err)
		}
		if strings.ContainsRune(enc, '\n') {
			t.Errorf("encoded line contains newline; callers append it")
		}
		out, err := DecryptLine(key, enc)
		if err != nil {
			t.Fatalf("DecryptLine: %v", err)
		}
		if !bytes.Equal(out, []byte(plain)) {
			t.Errorf("round-trip mismatch: got %q want %q", out, plain)
		}
	}
}

func TestEncryptLine_NonceUnique(t *testing.T) {
	// Same key + same plaintext should produce different ciphertexts
	// because the nonce is random per call. If this collides the AEAD
	// is fundamentally broken.
	key := freshKey(t)
	plain := []byte(`{"x":1}`)
	a, err := EncryptLine(key, plain)
	if err != nil {
		t.Fatalf("EncryptLine a: %v", err)
	}
	b, err := EncryptLine(key, plain)
	if err != nil {
		t.Fatalf("EncryptLine b: %v", err)
	}
	if a == b {
		t.Error("identical ciphertexts on two encryptions — nonce reuse")
	}
}

func TestDecryptLine_TrailingNewline(t *testing.T) {
	key := freshKey(t)
	plain := []byte(`{"hello":"world"}`)
	enc, err := EncryptLine(key, plain)
	if err != nil {
		t.Fatalf("EncryptLine: %v", err)
	}
	for _, suffix := range []string{"", "\n", "\r\n"} {
		out, err := DecryptLine(key, enc+suffix)
		if err != nil {
			t.Fatalf("DecryptLine with suffix %q: %v", suffix, err)
		}
		if !bytes.Equal(out, plain) {
			t.Errorf("suffix %q: got %q want %q", suffix, out, plain)
		}
	}
}

func TestDecryptLine_Truncated(t *testing.T) {
	key := freshKey(t)
	enc, err := EncryptLine(key, []byte(`{"k":"v"}`))
	if err != nil {
		t.Fatalf("EncryptLine: %v", err)
	}
	// Lop off the last 8 base64 chars — destroys the auth tag and
	// part of the ciphertext.
	truncated := enc[:len(enc)-8]
	if _, err := DecryptLine(key, truncated); err == nil {
		t.Error("expected error decrypting truncated line, got nil")
	}

	// And a hard truncation below the (nonce + tag) minimum should
	// also fail with a clear length error rather than a panic.
	raw, _ := base64.StdEncoding.DecodeString(enc)
	short := base64.StdEncoding.EncodeToString(raw[:8])
	if _, err := DecryptLine(key, short); err == nil {
		t.Error("expected error on sub-minimum-length line, got nil")
	}
}

func TestDecryptLine_FlippedByte(t *testing.T) {
	key := freshKey(t)
	enc, err := EncryptLine(key, []byte(`{"sensitive":"value"}`))
	if err != nil {
		t.Fatalf("EncryptLine: %v", err)
	}
	// Decode -> flip a ciphertext byte (past the 24-byte nonce) ->
	// re-encode. Poly1305 must reject it.
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	flipIdx := chacha20poly1305.NonceSizeX + 2
	if flipIdx >= len(raw) {
		t.Fatalf("ciphertext too short to flip at idx %d (len %d)", flipIdx, len(raw))
	}
	raw[flipIdx] ^= 0x01
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, err := DecryptLine(key, tampered); err == nil {
		t.Error("expected auth-tag error on flipped ciphertext byte, got nil")
	}
}

func TestDecryptLine_FlippedTagByte(t *testing.T) {
	key := freshKey(t)
	enc, err := EncryptLine(key, []byte(`{"sensitive":"value"}`))
	if err != nil {
		t.Fatalf("EncryptLine: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	// Flip a byte in the trailing 16-byte Poly1305 tag.
	raw[len(raw)-1] ^= 0x80
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, err := DecryptLine(key, tampered); err == nil {
		t.Error("expected auth-tag error on flipped tag byte, got nil")
	}
}

func TestDecryptLine_WrongKey(t *testing.T) {
	k1 := freshKey(t)
	k2 := freshKey(t)
	enc, err := EncryptLine(k1, []byte(`{"x":42}`))
	if err != nil {
		t.Fatalf("EncryptLine: %v", err)
	}
	if _, err := DecryptLine(k2, enc); err == nil {
		t.Error("expected error decrypting under wrong key, got nil")
	}
}

func TestEncryptLine_EmptyPlaintext(t *testing.T) {
	key := freshKey(t)
	enc, err := EncryptLine(key, []byte{})
	if err != nil {
		t.Fatalf("EncryptLine empty: %v", err)
	}
	// Envelope = 24-byte nonce + 0-byte ciphertext + 16-byte tag = 40
	// raw bytes => 56 base64 chars (with padding).
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if got, want := len(raw), chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead; got != want {
		t.Errorf("empty envelope size: got %d want %d", got, want)
	}
	out, err := DecryptLine(key, enc)
	if err != nil {
		t.Fatalf("DecryptLine empty: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("empty round-trip: got %q want empty", out)
	}
	// Also accept a nil plaintext.
	enc2, err := EncryptLine(key, nil)
	if err != nil {
		t.Fatalf("EncryptLine nil: %v", err)
	}
	out2, err := DecryptLine(key, enc2)
	if err != nil {
		t.Fatalf("DecryptLine nil: %v", err)
	}
	if len(out2) != 0 {
		t.Errorf("nil round-trip: got %q want empty", out2)
	}
}

func TestEncryptLine_BadKeyLength(t *testing.T) {
	if _, err := EncryptLine(make([]byte, 16), []byte("x")); err == nil {
		t.Error("expected error for 16-byte key in EncryptLine")
	}
	if _, err := DecryptLine(make([]byte, 16), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Error("expected error for 16-byte key in DecryptLine")
	}
}

func TestDecryptLine_InvalidBase64(t *testing.T) {
	key := freshKey(t)
	if _, err := DecryptLine(key, "not!valid@base64$$$"); err == nil {
		t.Error("expected base64 decode error, got nil")
	}
}
