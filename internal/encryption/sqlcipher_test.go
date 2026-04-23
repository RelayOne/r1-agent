package encryption

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestBuildEncryptedDSN_Format verifies the DSN contains all four
// expected params (the hex key, the cipher name, the page size, and
// the KDF iter count) in a form the sqlite3mc driver accepts. We
// URL-decode manually so the assertions tolerate the ordering
// url.Values chooses.
func TestBuildEncryptedDSN_Format(t *testing.T) {
	key := make([]byte, MasterKeySize)
	for i := range key {
		key[i] = byte(i) // deterministic fixture
	}

	dsn, err := BuildEncryptedDSN("/tmp/r1/mission.db", key)
	if err != nil {
		t.Fatalf("BuildEncryptedDSN: %v", err)
	}

	// file: prefix is load-bearing — some sqlite3mc code paths
	// require it to engage the URI parser.
	if !strings.HasPrefix(dsn, "file:/tmp/r1/mission.db?") {
		t.Errorf("missing file: prefix or wrong path: %q", dsn)
	}

	hexKey := hex.EncodeToString(key)
	// The raw key literal gets URL-escaped (' becomes %27). Either
	// representation is acceptable as long as the hex bytes land
	// verbatim in the query string — sqlite3mc decodes both.
	if !strings.Contains(dsn, hexKey) {
		t.Errorf("hex key not present in DSN: got %q", RedactedDSN(dsn))
	}
	if !strings.Contains(dsn, "_key=") {
		t.Error("missing _key= param")
	}
	if !strings.Contains(dsn, "cipher="+SQLCipherName) {
		t.Errorf("missing cipher=%s param", SQLCipherName)
	}
	if !strings.Contains(dsn, "cipher_page_size=4096") {
		t.Error("missing cipher_page_size=4096 param")
	}
	if !strings.Contains(dsn, "kdf_iter=256000") {
		t.Error("missing kdf_iter=256000 param")
	}
}

// TestBuildEncryptedDSN_EmptyKey_Errors asserts we fail-closed on
// degenerate key material: an empty key would silently produce a
// DSN that opens an UNENCRYPTED database, which is exactly the
// failure mode the at-rest spec tells us to reject.
func TestBuildEncryptedDSN_EmptyKey_Errors(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		make([]byte, 16), // short
		make([]byte, 31), // off-by-one short
		make([]byte, 33), // off-by-one long
		make([]byte, 64), // way too long
	}
	for _, k := range cases {
		if _, err := BuildEncryptedDSN("/tmp/x.db", k); err == nil {
			t.Errorf("expected error for %d-byte key, got nil", len(k))
		}
	}

	// Sanity: the correct size still succeeds.
	if _, err := BuildEncryptedDSN("/tmp/x.db", make([]byte, MasterKeySize)); err != nil {
		t.Errorf("unexpected error for 32-byte key: %v", err)
	}

	// Empty path is also a fail-closed case — without it we'd open
	// an anonymous in-memory DB that silently discards writes on
	// process exit.
	if _, err := BuildEncryptedDSN("", make([]byte, MasterKeySize)); err == nil {
		t.Error("expected error for empty path, got nil")
	}
}

// TestRedactedDSN_ScrubsKey verifies the redaction helper replaces
// the hex key with the literal `<redacted>` sentinel. This is the
// only knob that stands between a raw master key and an operator's
// log aggregator, so the test is load-bearing.
func TestRedactedDSN_ScrubsKey(t *testing.T) {
	key := make([]byte, MasterKeySize)
	for i := range key {
		key[i] = 0xAB
	}
	hexKey := hex.EncodeToString(key) // distinctive 64-char pattern

	dsn, err := BuildEncryptedDSN("/tmp/x.db", key)
	if err != nil {
		t.Fatalf("BuildEncryptedDSN: %v", err)
	}
	if !strings.Contains(dsn, hexKey) {
		t.Fatalf("test precondition failed: hex key not in DSN")
	}

	red := RedactedDSN(dsn)
	if !strings.Contains(red, "_key=<redacted>") {
		t.Errorf("redacted DSN missing sentinel: %q", red)
	}
	if strings.Contains(red, hexKey) {
		t.Errorf("redacted DSN still contains raw hex key: %q", red)
	}
	// Other params must survive redaction — they are needed for
	// operators debugging cipher/page-size mismatches from logs.
	if !strings.Contains(red, "cipher="+SQLCipherName) {
		t.Error("redaction stripped cipher= param")
	}
	if !strings.Contains(red, "cipher_page_size=4096") {
		t.Error("redaction stripped cipher_page_size= param")
	}

	// A DSN with no key is returned verbatim.
	plain := "file:/tmp/plain.db?_journal=WAL"
	if got := RedactedDSN(plain); got != plain {
		t.Errorf("plaintext DSN mangled: got %q, want %q", got, plain)
	}
}
