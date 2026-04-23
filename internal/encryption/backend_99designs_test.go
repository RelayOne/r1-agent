package encryption

// Tests cover the MasterKeyringBackend surface from three angles:
//
//  1. Fallback behaviour — when 99designs is unavailable (we force
//     it via ForceBackend="file") the file backend wins.
//  2. Round-trip — the file backend honours Get/Set/Delete semantics
//     including the ErrMasterKeyringEntryMissing sentinel.
//  3. Backwards compatibility — a value written through the old
//     `FileKeyring.Put` path (32-byte Key) is readable through the
//     new MasterKeyringBackend.Get path.
//
// We deliberately avoid hitting a real OS keychain in the test
// suite: CI has no D-Bus / no Keychain / no Credential Manager, and
// even on developer boxes a test suite should not pollute the user's
// keychain. ForceBackend="file" keeps us on the fallback path, which
// is the path every CI run exercises in production anyway.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpen_FallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	// Force the file backend — equivalent to what happens on a
	// headless Linux box where 99designs/secret-service can't
	// reach D-Bus.
	be, err := OpenMasterKeyring(MasterKeyringOpts{
		ServiceName:  "stoke-test",
		FileDir:      dir,
		ForceBackend: "file",
	})
	if err != nil {
		t.Fatalf("OpenMasterKeyring: %v", err)
	}
	// Writing through the file backend should land a 0600 file in
	// the configured directory — that's our proof the fallback won
	// (as opposed to an in-memory or OS-keychain backend that would
	// leave the directory empty).
	if err := be.Set("probe", []byte("hello")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	// The temp-file rename dance leaves exactly one artifact per key.
	var count int
	for _, e := range entries {
		if !e.IsDir() {
			count++
		}
	}
	if count == 0 {
		t.Fatal("expected fallback to write a file into FileDir, got none")
	}
}

func TestOpen_RoundtripViaFileBackend(t *testing.T) {
	dir := t.TempDir()
	be, err := OpenMasterKeyring(MasterKeyringOpts{
		FileDir:      dir,
		ForceBackend: "file",
	})
	if err != nil {
		t.Fatalf("OpenMasterKeyring: %v", err)
	}
	// Missing entries report ErrMasterKeyringEntryMissing — the
	// sentinel LoadOrGenerateMasterFrom-style callers rely on to
	// distinguish "generate a fresh key" from "real IO failure".
	if _, err := be.Get("never-written"); !errors.Is(err, ErrMasterKeyringEntryMissing) {
		t.Fatalf("want ErrMasterKeyringEntryMissing for fresh dir, got %v", err)
	}
	// Set → Get → Delete → Get (missing again) is the core
	// behavioural contract. If the value doesn't round-trip, the
	// entire encryption-at-rest pipeline is broken.
	val := []byte("not-exactly-32-bytes")
	if err := be.Set("k", val); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := be.Get("k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Errorf("round-trip mismatch: got %q want %q", got, val)
	}
	if err := be.Delete("k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := be.Get("k"); !errors.Is(err, ErrMasterKeyringEntryMissing) {
		t.Errorf("Get after Delete: want ErrMasterKeyringEntryMissing, got %v", err)
	}
	// Delete of a now-missing entry is idempotent — mirrors
	// FileKeyring.Delete and OS-keychain semantics so operators
	// can safely re-run cleanup scripts.
	if err := be.Delete("k"); err != nil {
		t.Errorf("Delete idempotency: got %v", err)
	}
}

func TestOpen_CompatibleWithExistingKeyring(t *testing.T) {
	dir := t.TempDir()
	// Write a 32-byte key via the PRE-EXISTING FileKeyring path —
	// the one master_key.go LoadOrGenerateMasterFrom uses today.
	// The new MasterKeyringBackend must be able to read it without
	// a migration step, otherwise operators would have to rotate
	// their master key just to upgrade to the adapter.
	legacy, err := NewFileKeyring(dir)
	if err != nil {
		t.Fatalf("NewFileKeyring: %v", err)
	}
	k, _ := NewKey()
	if err := legacy.Put(MasterKeyAccount, k); err != nil {
		t.Fatalf("legacy Put: %v", err)
	}

	// Now open the master-keyring adapter at the same directory
	// and fetch the master-key slot.
	be, err := OpenMasterKeyring(MasterKeyringOpts{
		FileDir:      dir,
		ForceBackend: "file",
	})
	if err != nil {
		t.Fatalf("OpenMasterKeyring: %v", err)
	}
	got, err := be.Get(MasterKeyAccount)
	if err != nil {
		t.Fatalf("Get master key via adapter: %v", err)
	}
	want := k.Bytes()
	if !bytes.Equal(got, want[:]) {
		t.Errorf("adapter-read master key differs from legacy-written key")
	}
	// And the reverse: a value Set through the adapter should be
	// readable by the legacy FileKeyring path when the value is
	// 32 bytes long. This keeps downgrade compatibility — an
	// operator rolling back the binary doesn't lose access.
	fresh, _ := NewKey()
	freshBytes := fresh.Bytes()
	if err := be.Set("agent-roundtrip", freshBytes[:]); err != nil {
		t.Fatalf("adapter Set: %v", err)
	}
	viaLegacy, err := legacy.Get("agent-roundtrip")
	if err != nil {
		t.Fatalf("legacy Get of adapter-written key: %v", err)
	}
	viaLegacyBytes := viaLegacy.Bytes()
	if !bytes.Equal(freshBytes[:], viaLegacyBytes[:]) {
		t.Errorf("adapter→legacy round-trip differs")
	}
}

func TestOpen_DefaultsServiceNameAndFileDir(t *testing.T) {
	// ServiceName="" and FileDir="" should resolve cleanly — we
	// test only the file-forced branch so we don't depend on the
	// user's home being writable by the test runner.
	home := t.TempDir()
	t.Setenv("HOME", home)
	be, err := OpenMasterKeyring(MasterKeyringOpts{ForceBackend: "file"})
	if err != nil {
		t.Fatalf("OpenMasterKeyring with zero opts: %v", err)
	}
	if err := be.Set("k", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Default dir is <home>/.r1/keyring.
	expected := filepath.Join(home, ".r1", "keyring")
	entries, err := os.ReadDir(expected)
	if err != nil {
		t.Fatalf("ReadDir default keyring dir: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("expected default keyring dir %q to contain the written entry", expected)
	}
}

func TestOpen_RespectsEnvBackendOverride(t *testing.T) {
	// R1_KEYRING_BACKEND=file should reach the file fallback even
	// on hosts where a real 99designs backend is available. We
	// can't directly inspect WHICH backend was chosen, but we can
	// prove the file directory got populated — which only the file
	// backend does.
	dir := t.TempDir()
	t.Setenv("R1_KEYRING_BACKEND", "file")
	be, err := OpenMasterKeyring(MasterKeyringOpts{FileDir: dir})
	if err != nil {
		t.Fatalf("OpenMasterKeyring: %v", err)
	}
	if err := be.Set("envprobe", []byte("x")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("R1_KEYRING_BACKEND=file did not route through file backend")
	}
}
