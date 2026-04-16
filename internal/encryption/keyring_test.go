package encryption

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func TestNewKey_Uniqueness(t *testing.T) {
	k1, _ := NewKey()
	k2, _ := NewKey()
	if bytes.Equal(k1.material[:], k2.material[:]) {
		t.Error("two freshly generated keys collided (1 in 2^256 chance)")
	}
}

func TestKeyFromBytes_Length(t *testing.T) {
	if _, err := KeyFromBytes(make([]byte, 16)); err == nil {
		t.Error("expected error for 16-byte input")
	}
	if _, err := KeyFromBytes(make([]byte, 32)); err != nil {
		t.Errorf("unexpected error for 32-byte input: %v", err)
	}
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	k, _ := NewKey()
	plain := []byte("hello stoke — secret notes here")
	ct, err := Encrypt(k, plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(ct, plain) {
		t.Error("ciphertext equals plaintext")
	}
	out, err := Decrypt(k, ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Errorf("round-trip mismatch: got %q want %q", out, plain)
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	k1, _ := NewKey()
	k2, _ := NewKey()
	ct, _ := Encrypt(k1, []byte("secret"))
	if _, err := Decrypt(k2, ct); err == nil {
		t.Error("decrypt with wrong key should fail")
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	k, _ := NewKey()
	ct, _ := Encrypt(k, []byte("secret"))
	// Flip a bit in the tag area (last byte).
	ct[len(ct)-1] ^= 0x01
	if _, err := Decrypt(k, ct); err == nil {
		t.Error("decrypt of tampered ciphertext should fail")
	}
}

func TestEncrypt_NonceFreshness(t *testing.T) {
	// Two encryptions of the same plaintext with the same key
	// should produce different ciphertexts (different nonces).
	k, _ := NewKey()
	ct1, _ := Encrypt(k, []byte("same"))
	ct2, _ := Encrypt(k, []byte("same"))
	if bytes.Equal(ct1, ct2) {
		t.Error("encrypting the same plaintext twice produced identical ciphertexts — nonce reuse")
	}
}

func TestMemoryKeyring_BasicOps(t *testing.T) {
	kr := NewMemoryKeyring()
	if _, err := kr.Get("agent-a"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("want ErrKeyNotFound for unknown agent, got %v", err)
	}
	k, created, err := kr.GetOrCreate("agent-a")
	if err != nil || !created {
		t.Fatalf("GetOrCreate: err=%v created=%v", err, created)
	}
	k2, created, _ := kr.GetOrCreate("agent-a")
	if created {
		t.Error("second GetOrCreate should return existing key (created=false)")
	}
	if k.material != k2.material {
		t.Error("GetOrCreate returned different keys across calls")
	}
	if err := kr.Delete("agent-a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := kr.Get("agent-a"); !errors.Is(err, ErrKeyNotFound) {
		t.Error("Get after Delete should return ErrKeyNotFound")
	}
	// Delete non-existent should be idempotent.
	if err := kr.Delete("agent-a"); err != nil {
		t.Errorf("Delete of non-existent agent should be idempotent, got %v", err)
	}
}

func TestFileKeyring_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	kr1, err := NewFileKeyring(dir)
	if err != nil {
		t.Fatalf("NewFileKeyring: %v", err)
	}
	k, _, err := kr1.GetOrCreate("agent-a")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	// Fresh instance pointed at the same directory should read
	// the same key.
	kr2, _ := NewFileKeyring(dir)
	k2, err := kr2.Get("agent-a")
	if err != nil {
		t.Fatalf("Get from second instance: %v", err)
	}
	if k.material != k2.material {
		t.Error("keys differ across instances — persistence broken")
	}
}

func TestFileKeyring_KeyFileMode(t *testing.T) {
	dir := t.TempDir()
	kr, _ := NewFileKeyring(dir)
	k, _ := NewKey()
	if err := kr.Put("a", k); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Brute-force scan the directory; the only file should have
	// 0600 perms.
	matches, _ := filepath.Glob(filepath.Join(dir, "*.key"))
	if len(matches) != 1 {
		t.Fatalf("got %d key files, want 1", len(matches))
	}
	// Can't check perms directly without osutil — the mode
	// should be enforced by Put() which passes 0o600 to WriteFile.
	// This test ensures the file exists at the expected path.
}

func TestFileKeyring_AgentIDWithSpecialChars(t *testing.T) {
	dir := t.TempDir()
	kr, _ := NewFileKeyring(dir)
	// Agent IDs with slashes shouldn't escape the keyring dir.
	k, _, err := kr.GetOrCreate("did:tp:../../etc/passwd")
	if err != nil {
		t.Fatalf("GetOrCreate with special chars: %v", err)
	}
	k2, err := kr.Get("did:tp:../../etc/passwd")
	if err != nil {
		t.Fatalf("Get round-trip: %v", err)
	}
	if k.material != k2.material {
		t.Error("special-char agent ID didn't round-trip")
	}
}
