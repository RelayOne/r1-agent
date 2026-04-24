package main

// TASK 7 (work-stoke) coverage for the env-gated SQLCipher DSN the
// r1-server tracking DB now opens through. Two shape-of-the-pipe tests:
//
//   - TestOpenDB_PlaintextPath asserts the default (no env set) path
//     keeps working. This is the regression guard for every existing
//     deployment — if the encrypted wiring broke the plaintext branch,
//     this test fires first.
//
//   - TestOpenDB_EncryptedPath sets STOKE_DB_ENCRYPTION=1, points the
//     encryption package at an in-memory MemoryKeyring pre-seeded with a
//     deterministic 32-byte key, opens the DB, writes a signature row
//     with a distinctive plaintext marker, closes, and then grep-scans
//     the file bytes for that marker. The marker must be absent from
//     the ciphertext — that's the load-bearing invariant for at-rest
//     encryption.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/encryption"
	"github.com/RelayOne/r1/internal/session"
)

// TestOpenDB_PlaintextPath is the "nothing-changed" guard. With
// STOKE_DB_ENCRYPTION unset (the default) OpenDB must return a
// working handle that accepts writes and reads, and the DB file on
// disk must be a vanilla SQLite database (so tools like `sqlite3`
// keep working for operators who have never opted into encryption).
func TestOpenDB_PlaintextPath(t *testing.T) {
	t.Setenv(dsnEncryptionEnv, "") // explicit unset for clarity

	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Round-trip a signature row so the schema actually materializes
	// and we exercise the same code path an operator would hit.
	now := time.Now().UTC()
	sig := session.SignatureFile{
		Version:    "1",
		PID:        1001,
		InstanceID: "r1-plaintext-01",
		StartedAt:  now,
		UpdatedAt:  now,
		RepoRoot:   "/tmp/plaintext-repo",
		Mode:       "ship",
		Status:     "running",
	}
	if err := db.UpsertSession(sig); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	rows, err := db.ListSessions("")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 1 || rows[0].InstanceID != "r1-plaintext-01" {
		t.Fatalf("ListSessions returned %+v, want one row for r1-plaintext-01", rows)
	}

	// Header of a plaintext SQLite file is literally "SQLite format 3\x00".
	// Reading the first 16 bytes and comparing avoids any libsqlite3
	// dependency in the test.
	raw, err := os.ReadFile(filepath.Join(dir, "server.db"))
	if err != nil {
		t.Fatalf("read server.db: %v", err)
	}
	if len(raw) < 16 {
		t.Fatalf("server.db too short (%d bytes)", len(raw))
	}
	if string(raw[:15]) != "SQLite format 3" {
		t.Errorf("plaintext DB missing canonical magic; got %q", string(raw[:15]))
	}
}

// TestOpenDB_EncryptedPath covers the STOKE_DB_ENCRYPTION=1 branch.
// The test seeds an in-process MemoryKeyring as the default so no
// filesystem or OS keyring state leaks between runs, opens the DB,
// writes a row containing a distinctive sentinel string, and then
// grep-scans the on-disk bytes. The sentinel must NOT appear verbatim
// in the file.
//
// Two hard assertions:
//
//   - the SQLite header magic ("SQLite format 3") must NOT appear in
//     the opening bytes of server.db (plaintext DB gives it away at
//     offset 0; ChaCha20 XOR-scrambles it).
//   - the sentinel substring must NOT appear in any file under the
//     data directory.
//
// The go.mod replace directive landed in this same commit pins the
// sqlite3mc fork, so both assertions are unconditional. A future
// revert of the replace would flip both to red — which is exactly the
// regression signal at-rest encryption needs.
func TestOpenDB_EncryptedPath(t *testing.T) {
	// Seed a deterministic keyring so LoadOrGenerateMaster returns a
	// stable 32-byte key inside this test process. MemoryKeyring lives
	// entirely in RAM so nothing leaks between tests or to the
	// developer's ~/.r1/keyring.
	kr := encryption.NewMemoryKeyring()
	key := make([]byte, encryption.MasterKeySize)
	for i := range key {
		key[i] = 0xA5 // distinctive, non-zero, non-printable pattern
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
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("OpenDB with encryption: %v", err)
	}

	// The sentinel is a strong-random-looking ASCII string that has
	// no chance of occurring in either the SQLite header or any
	// schema DDL. If the driver writes plaintext this string shows
	// up verbatim; if the driver encrypts, ChaCha20 produces
	// uniformly distributed ciphertext so the chance of a 64-char
	// substring match is effectively zero.
	sentinel := "STOKE-T7-SENTINEL-DO-NOT-LEAK-abc123xyz987-" +
		"ciphertext-grep-target-aaaa"

	now := time.Now().UTC()
	sig := session.SignatureFile{
		Version:    "1",
		PID:        2002,
		InstanceID: "r1-encrypted-01",
		StartedAt:  now,
		UpdatedAt:  now,
		RepoRoot:   sentinel, // sentinel lives in a TEXT column
		Mode:       "ship",
		Status:     "running",
	}
	if err := db.UpsertSession(sig); err != nil {
		db.Close()
		t.Fatalf("UpsertSession: %v", err)
	}

	// Force the page to disk. Close() on a WAL database performs a
	// checkpoint, so a subsequent file read sees committed pages.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Assertion 1: the canonical plaintext SQLite magic must be
	// absent from the opening bytes of server.db. This is the
	// cheapest possible "is the cipher actually on?" probe — a
	// single memcmp against 15 well-known bytes — and it fires
	// before we bother grepping the whole dir. A future revert of
	// the go.mod replace directive would turn this assertion red.
	dbPath := filepath.Join(dir, "server.db")
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read %s: %v", dbPath, err)
	}
	if len(raw) < 16 {
		t.Fatalf("server.db too short (%d bytes) — schema didn't materialize?", len(raw))
	}
	if string(raw[:15]) == "SQLite format 3" {
		t.Fatalf("server.db begins with plaintext SQLite magic; cipher is NOT engaging (len=%d)", len(raw))
	}

	// Assertion 2: the sentinel must not appear verbatim in any
	// sibling file (server.db, -wal, -shm). Close() above
	// checkpoints the WAL, but we still scan every sibling so a
	// future refactor that leaks plaintext into a stray .wal fires
	// here instead of silently exfiltrating.
	leaked, scanned := scanForSentinel(t, dir, sentinel)
	if scanned == 0 {
		t.Fatal("no DB files found under dataDir — schema didn't materialize?")
	}
	if leaked {
		t.Errorf("sentinel %q appears in encrypted DB files; cipher is NOT engaging", sentinel)
	}
}

// scanForSentinel walks dir and checks every regular file for the
// substring. Returns (leaked, filesScanned). Files with a zero-byte
// body are counted as scanned but contribute no content.
func scanForSentinel(t *testing.T, dir, sentinel string) (bool, int) {
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

