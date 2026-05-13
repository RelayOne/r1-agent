package skill

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func genTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

func TestTrustRoot_SignAndVerifyRoundTrip(t *testing.T) {
	t.Parallel()
	rootPub, rootPriv := genTestKey(t)
	pkPub, _ := genTestKey(t)
	doc := &TrustRootDocument{
		Version:  "1",
		IssuedAt: time.Now().UTC().Format(time.RFC3339),
		Keys: []TrustRootEntry{{
			KeyID:     DeriveKeyID(pkPub),
			PublicKey: base64.StdEncoding.EncodeToString(pkPub),
			Authority: AuthorityR1,
		}},
	}
	if err := SignTrustRoot(doc, rootPriv); err != nil {
		t.Fatalf("SignTrustRoot: %v", err)
	}
	if err := VerifyTrustRoot(doc, rootPub); err != nil {
		t.Fatalf("VerifyTrustRoot: %v", err)
	}
}

func TestTrustRoot_VerifyTampered(t *testing.T) {
	t.Parallel()
	rootPub, rootPriv := genTestKey(t)
	pkPub, _ := genTestKey(t)
	doc := &TrustRootDocument{
		Version: "1",
		Keys: []TrustRootEntry{{
			KeyID:     DeriveKeyID(pkPub),
			PublicKey: base64.StdEncoding.EncodeToString(pkPub),
			Authority: AuthorityR1,
		}},
	}
	if err := SignTrustRoot(doc, rootPriv); err != nil {
		t.Fatalf("SignTrustRoot: %v", err)
	}
	doc.Keys[0].Authority = AuthorityTenant
	doc.Keys[0].TenantID = "evil"
	if err := VerifyTrustRoot(doc, rootPub); !errors.Is(err, ErrTrustRootSignatureInvalid) {
		t.Fatalf("want ErrTrustRootSignatureInvalid, got %v", err)
	}
}

func TestTrustRoot_MatchKey(t *testing.T) {
	t.Parallel()
	pkPub, _ := genTestKey(t)
	kid := DeriveKeyID(pkPub)
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	doc := &TrustRootDocument{
		Keys: []TrustRootEntry{{
			KeyID:     kid,
			PublicKey: base64.StdEncoding.EncodeToString(pkPub),
			Authority: AuthorityR1,
		}},
	}
	if _, err := MatchKey(doc, kid, "any-pack", now); err != nil {
		t.Fatalf("MatchKey: %v", err)
	}
	if _, err := MatchKey(doc, "ed25519:missing", "any-pack", now); !errors.Is(err, ErrTrustRootKeyNotFound) {
		t.Fatalf("want ErrTrustRootKeyNotFound, got %v", err)
	}
}

func TestTrustRoot_MatchKeyExpired(t *testing.T) {
	t.Parallel()
	pkPub, _ := genTestKey(t)
	kid := DeriveKeyID(pkPub)
	doc := &TrustRootDocument{
		Keys: []TrustRootEntry{{
			KeyID:     kid,
			PublicKey: base64.StdEncoding.EncodeToString(pkPub),
			Authority: AuthorityR1,
			NotAfter:  "2025-01-01T00:00:00Z",
		}},
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := MatchKey(doc, kid, "any", now); !errors.Is(err, ErrTrustRootKeyExpired) {
		t.Fatalf("want ErrTrustRootKeyExpired, got %v", err)
	}
}

func TestTrustRoot_MatchKeyNotYetValid(t *testing.T) {
	t.Parallel()
	pkPub, _ := genTestKey(t)
	kid := DeriveKeyID(pkPub)
	doc := &TrustRootDocument{
		Keys: []TrustRootEntry{{
			KeyID:     kid,
			PublicKey: base64.StdEncoding.EncodeToString(pkPub),
			Authority: AuthorityR1,
			NotBefore: "2030-01-01T00:00:00Z",
		}},
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := MatchKey(doc, kid, "any", now); !errors.Is(err, ErrTrustRootKeyNotYetValid) {
		t.Fatalf("want ErrTrustRootKeyNotYetValid, got %v", err)
	}
}

func TestTrustRoot_MatchKeyScopeViolation(t *testing.T) {
	t.Parallel()
	pkPub, _ := genTestKey(t)
	kid := DeriveKeyID(pkPub)
	doc := &TrustRootDocument{
		Keys: []TrustRootEntry{{
			KeyID:     kid,
			PublicKey: base64.StdEncoding.EncodeToString(pkPub),
			Authority: AuthorityTenant,
			TenantID:  "acme",
			Scopes:    []string{"acme."},
		}},
	}
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if _, err := MatchKey(doc, kid, "acme.thing", now); err != nil {
		t.Fatalf("acme.thing should match: %v", err)
	}
	if _, err := MatchKey(doc, kid, "other.pack", now); !errors.Is(err, ErrTrustRootScopeViolation) {
		t.Fatalf("want ErrTrustRootScopeViolation, got %v", err)
	}
}

func TestTrustRoot_LoadAbsentReturnsNil(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	doc, err := LoadTrustRoot(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatalf("LoadTrustRoot absent: %v", err)
	}
	if doc != nil {
		t.Fatalf("doc = %v, want nil", doc)
	}
}

func TestTrustRoot_SaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "trust-root.json")
	_, rootPriv := genTestKey(t)
	pkPub, _ := genTestKey(t)
	doc := &TrustRootDocument{
		Version: "1",
		Keys: []TrustRootEntry{{
			KeyID:     DeriveKeyID(pkPub),
			PublicKey: base64.StdEncoding.EncodeToString(pkPub),
			Authority: AuthorityR1,
		}},
	}
	if err := SignTrustRoot(doc, rootPriv); err != nil {
		t.Fatalf("SignTrustRoot: %v", err)
	}
	if err := SaveTrustRoot(path, doc); err != nil {
		t.Fatalf("SaveTrustRoot: %v", err)
	}
	got, err := LoadTrustRoot(path)
	if err != nil {
		t.Fatalf("LoadTrustRoot: %v", err)
	}
	if got == nil || len(got.Keys) != 1 || got.Keys[0].KeyID != doc.Keys[0].KeyID {
		t.Fatalf("LoadTrustRoot mismatch: %+v", got)
	}
}

func TestTrustRoot_LoadOrGenerateRootKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "root.key")
	priv1, pub1, err := LoadOrGenerateRootKey(path)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	priv2, pub2, err := LoadOrGenerateRootKey(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if string(priv1) != string(priv2) || string(pub1) != string(pub2) {
		t.Fatalf("key changed on reload")
	}
}

func TestTrustRoot_DeriveKeyID(t *testing.T) {
	t.Parallel()
	pk, _ := genTestKey(t)
	kid := DeriveKeyID(pk)
	if len(kid) < 16 || kid[:8] != "ed25519:" {
		t.Fatalf("DeriveKeyID = %q", kid)
	}
}
