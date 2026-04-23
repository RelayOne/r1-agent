package wisdom

// TASK 7 (work-stoke) coverage for the env-gated SQLCipher DSN the
// wisdom store now opens through. Two shape-of-the-pipe tests
// symmetrical with cmd/r1-server/db_encrypted_test.go:
//
//   - TestOpenSQLiteStore_PlaintextPath asserts the historical path
//     (no env) still produces a plain SQLite file and round-trips a
//     Record/Learnings cycle — the regression guard for every
//     existing deployment.
//
//   - TestOpenSQLiteStore_EncryptedPath sets STOKE_DB_ENCRYPTION=1,
//     points the encryption package at an in-memory keyring seeded
//     with a deterministic master key, writes a row containing a
//     distinctive sentinel, closes the DB, and grep-scans the on-disk
//     bytes. The sentinel must NOT appear verbatim — that is the
//     load-bearing at-rest invariant.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/encryption"
)

// TestOpenSQLiteStore_PlaintextPath — default path continues to work.
func TestOpenSQLiteStore_PlaintextPath(t *testing.T) {
	t.Setenv(dsnEncryptionEnv, "")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wisdom.db")

	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	s.Record("task-plain", Learning{
		Category:    Gotcha,
		Description: "plaintext-path regression guard",
		File:        "main.go",
	})
	ls := s.Learnings()
	if len(ls) != 1 || ls[0].TaskID != "task-plain" {
		t.Fatalf("Learnings=%+v, want one row for task-plain", ls)
	}

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read wisdom.db: %v", err)
	}
	if len(raw) < 16 || string(raw[:15]) != "SQLite format 3" {
		t.Errorf("plaintext DB missing canonical magic; got %q", string(raw[:min(15, len(raw))]))
	}
}

// TestOpenSQLiteStore_EncryptedPath — env-gated path engages the
// cipher and does NOT leak the sentinel onto disk. Two hard
// assertions, symmetrical with cmd/r1-server/db_encrypted_test.go:
//
//   - the plaintext SQLite magic ("SQLite format 3") must NOT appear
//     in the opening bytes of wisdom.db (the fastest probe for "is
//     the cipher actually engaging?").
//   - the sentinel must NOT appear verbatim in any sibling file.
//
// The go.mod replace directive pinning the sqlite3mc fork lands in
// the same commit as this test, so both assertions are unconditional.
func TestOpenSQLiteStore_EncryptedPath(t *testing.T) {
	kr := encryption.NewMemoryKeyring()
	key := make([]byte, encryption.MasterKeySize)
	for i := range key {
		key[i] = 0x5A
	}
	k, err := encryption.KeyFromBytes(key)
	if err != nil {
		t.Fatalf("KeyFromBytes: %v", err)
	}
	if err := kr.Put(encryption.MasterKeyAccount, k); err != nil {
		t.Fatalf("seed keyring: %v", err)
	}
	encryption.SetDefaultMasterKeyring(kr)
	t.Cleanup(func() { encryption.SetDefaultMasterKeyring(nil) })

	t.Setenv(dsnEncryptionEnv, "1")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wisdom.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore (encrypted): %v", err)
	}

	sentinel := "STOKE-T7-WISDOM-SENTINEL-DO-NOT-LEAK-" +
		"xyz987abc123-ciphertext-grep-target-bbbb"

	s.Record("task-enc", Learning{
		Category:    Gotcha,
		Description: sentinel, // lives in a TEXT column
		File:        "sqlite.go",
	})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Assertion 1: file header must NOT be plaintext SQLite magic.
	// The fastest "is the cipher on?" probe — a memcmp against 15
	// well-known bytes. A future revert of the go.mod replace
	// directive would turn this assertion red.
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read %s: %v", dbPath, err)
	}
	if len(raw) < 16 {
		t.Fatalf("wisdom.db too short (%d bytes) — schema didn't materialize?", len(raw))
	}
	if string(raw[:15]) == "SQLite format 3" {
		t.Fatalf("wisdom.db begins with plaintext SQLite magic; cipher is NOT engaging (len=%d)", len(raw))
	}

	// Assertion 2: sentinel must not appear verbatim in any file.
	leaked, scanned := scanDirForSentinel(t, dir, sentinel)
	if scanned == 0 {
		t.Fatal("no DB files found under dir — schema didn't materialize?")
	}
	if leaked {
		t.Errorf("sentinel %q leaked into encrypted DB files", sentinel)
	}
}

func scanDirForSentinel(t *testing.T, dir, sentinel string) (bool, int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var scanned int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		f, err := os.Open(p)
		if err != nil {
			t.Fatalf("open %s: %v", p, err)
		}
		body, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		scanned++
		if strings.Contains(string(body), sentinel) {
			return true, scanned
		}
	}
	return false, scanned
}

