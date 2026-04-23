package encryption

import (
	"bytes"
	"testing"
)

func TestDeriveKey_Deterministic(t *testing.T) {
	master := make([]byte, MasterKeySize)
	for i := range master {
		master[i] = byte(i)
	}

	a, err := DeriveKey(master, PurposeJSONLStream)
	if err != nil {
		t.Fatalf("DeriveKey first call: %v", err)
	}
	b, err := DeriveKey(master, PurposeJSONLStream)
	if err != nil {
		t.Fatalf("DeriveKey second call: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("DeriveKey not deterministic:\n  first:  %x\n  second: %x", a, b)
	}
	if len(a) != MasterKeySize {
		t.Errorf("derived key length = %d, want %d", len(a), MasterKeySize)
	}
}

func TestDeriveKey_DifferentPurposesDiverge(t *testing.T) {
	master := make([]byte, MasterKeySize)
	for i := range master {
		master[i] = byte(i * 7)
	}

	jsonl, err := DeriveKey(master, PurposeJSONLStream)
	if err != nil {
		t.Fatalf("DeriveKey jsonl: %v", err)
	}
	sqlite, err := DeriveKey(master, PurposeSQLiteCipher)
	if err != nil {
		t.Fatalf("DeriveKey sqlite: %v", err)
	}
	sig, err := DeriveKey(master, PurposeLedgerRedactionSig)
	if err != nil {
		t.Fatalf("DeriveKey sig: %v", err)
	}

	if bytes.Equal(jsonl, sqlite) {
		t.Error("jsonl-stream and sqlite-cipher keys collided — HKDF info not plumbed")
	}
	if bytes.Equal(jsonl, sig) {
		t.Error("jsonl-stream and ledger-redaction-sign keys collided")
	}
	if bytes.Equal(sqlite, sig) {
		t.Error("sqlite-cipher and ledger-redaction-sign keys collided")
	}
}

func TestDeriveKey_DifferentMastersDiverge(t *testing.T) {
	m1 := bytes.Repeat([]byte{0x11}, MasterKeySize)
	m2 := bytes.Repeat([]byte{0x22}, MasterKeySize)

	a, err := DeriveKey(m1, PurposeJSONLStream)
	if err != nil {
		t.Fatalf("DeriveKey m1: %v", err)
	}
	b, err := DeriveKey(m2, PurposeJSONLStream)
	if err != nil {
		t.Fatalf("DeriveKey m2: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Error("distinct masters produced identical derived keys")
	}
}

func TestDeriveKey_RejectsBadInputs(t *testing.T) {
	short := make([]byte, 16)
	if _, err := DeriveKey(short, PurposeJSONLStream); err == nil {
		t.Error("expected error for 16-byte master key")
	}
	long := make([]byte, 64)
	if _, err := DeriveKey(long, PurposeJSONLStream); err == nil {
		t.Error("expected error for 64-byte master key")
	}
	master := make([]byte, MasterKeySize)
	if _, err := DeriveKey(master, ""); err == nil {
		t.Error("expected error for empty purpose")
	}
}

func TestLoadOrGenerateMasterFrom_GeneratesThenLoads(t *testing.T) {
	kr := NewMemoryKeyring()

	first, err := LoadOrGenerateMasterFrom(kr)
	if err != nil {
		t.Fatalf("first LoadOrGenerateMasterFrom: %v", err)
	}
	if len(first) != MasterKeySize {
		t.Fatalf("first key length = %d, want %d", len(first), MasterKeySize)
	}
	if bytes.Equal(first, make([]byte, MasterKeySize)) {
		t.Error("generated master key is all zeroes (rand.Reader broken?)")
	}

	// Second call MUST load the persisted key, not re-generate.
	second, err := LoadOrGenerateMasterFrom(kr)
	if err != nil {
		t.Fatalf("second LoadOrGenerateMasterFrom: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("second call generated a new key instead of loading\n  first:  %x\n  second: %x",
			first, second)
	}
}

func TestLoadOrGenerateMasterFrom_PersistsUnderDocumentedAccount(t *testing.T) {
	kr := NewMemoryKeyring()
	if _, err := LoadOrGenerateMasterFrom(kr); err != nil {
		t.Fatalf("LoadOrGenerateMasterFrom: %v", err)
	}
	// The spec pins the keyring slot to MasterKeyAccount so SQLCipher
	// and redaction-signer code paths can agree on the lookup key.
	if _, err := kr.Get(MasterKeyAccount); err != nil {
		t.Errorf("master key not stored at %q: %v", MasterKeyAccount, err)
	}
}

func TestLoadOrGenerateMasterFrom_NilKeyringErrors(t *testing.T) {
	if _, err := LoadOrGenerateMasterFrom(nil); err == nil {
		t.Error("expected error for nil keyring")
	}
}

func TestLoadOrGenerateMasterFrom_ReturnedSliceIsIndependentCopy(t *testing.T) {
	kr := NewMemoryKeyring()
	first, err := LoadOrGenerateMasterFrom(kr)
	if err != nil {
		t.Fatalf("LoadOrGenerateMasterFrom: %v", err)
	}
	// Scribble on the returned slice; subsequent callers must still
	// get the pristine stored key. Otherwise we'd have a correctness
	// bug where a caller mutating their copy corrupts the keyring.
	original := append([]byte(nil), first...)
	for i := range first {
		first[i] ^= 0xff
	}
	second, err := LoadOrGenerateMasterFrom(kr)
	if err != nil {
		t.Fatalf("second LoadOrGenerateMasterFrom: %v", err)
	}
	if !bytes.Equal(second, original) {
		t.Errorf("mutation on caller slice leaked into keyring")
	}
}

func TestLoadOrGenerateMaster_UsesInjectedDefault(t *testing.T) {
	kr := NewMemoryKeyring()
	SetDefaultMasterKeyring(kr)
	t.Cleanup(func() { SetDefaultMasterKeyring(nil) })

	first, err := LoadOrGenerateMaster()
	if err != nil {
		t.Fatalf("first LoadOrGenerateMaster: %v", err)
	}
	second, err := LoadOrGenerateMaster()
	if err != nil {
		t.Fatalf("second LoadOrGenerateMaster: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Error("LoadOrGenerateMaster did not load the persisted key on the second call")
	}
	// And the stored entry must sit under the documented account name
	// so the process-global helper and LoadOrGenerateMasterFrom agree.
	k, err := kr.Get(MasterKeyAccount)
	if err != nil {
		t.Fatalf("master key absent from injected default: %v", err)
	}
	stored := k.Bytes()
	if !bytes.Equal(stored[:], first) {
		t.Error("injected-default store holds a different key than LoadOrGenerateMaster returned")
	}
}

// Guard that SetDefaultMasterKeyring(nil) resets to the filesystem
// default. We don't exercise the file path here (that would require
// sandboxing $HOME); we just verify the reset leaves the package
// in a state where a subsequent SetDefaultMasterKeyring(kr) works.
func TestSetDefaultMasterKeyring_ResetIsClean(t *testing.T) {
	kr1 := NewMemoryKeyring()
	SetDefaultMasterKeyring(kr1)
	if _, err := LoadOrGenerateMaster(); err != nil {
		t.Fatalf("LoadOrGenerateMaster with kr1: %v", err)
	}
	SetDefaultMasterKeyring(nil)
	kr2 := NewMemoryKeyring()
	SetDefaultMasterKeyring(kr2)
	t.Cleanup(func() { SetDefaultMasterKeyring(nil) })

	k, err := LoadOrGenerateMaster()
	if err != nil {
		t.Fatalf("LoadOrGenerateMaster with kr2: %v", err)
	}
	// kr2 is fresh, so a fresh key was generated there. Make sure it
	// actually lives in kr2, not leaking back into kr1.
	if _, err := kr2.Get(MasterKeyAccount); err != nil {
		t.Errorf("expected new key in kr2: %v", err)
	}
	if len(k) != MasterKeySize {
		t.Errorf("key length = %d, want %d", len(k), MasterKeySize)
	}
}
