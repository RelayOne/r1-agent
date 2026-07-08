package main

// pack_trustroot_test.go — C7 rejection-path coverage for trust-root
// enforcement on the pack LOAD and ADOPT paths. Fills two gaps: (1) the
// MatchKey enforcement wired into enforceTrustRootForLoad / checkTrustRootForAdopt
// was only unit-tested in isolation, never through the consuming path; and
// (2) the trust-root document's OWN signature was never verified on
// consumption (VerifyTrustRoot had no non-test caller) — a tampered
// trust-root.json was accepted. Enforcement is now gated on the pinned root
// key R1_TRUST_ROOT_PUBKEY; these tests exercise both.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/skill"
	"github.com/RelayOne/r1/internal/skillmfr"
)

func genTrustKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

func b64(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

func docWithKey(kid, pubB64 string) *skill.TrustRootDocument {
	return &skill.TrustRootDocument{
		Version:  "1",
		IssuedAt: time.Now().UTC().Format(time.RFC3339),
		Keys: []skill.TrustRootEntry{{
			KeyID:     kid,
			PublicKey: pubB64,
			Authority: skill.AuthorityR1,
		}},
	}
}

func writeTrustRoot(t *testing.T, dir string, doc *skill.TrustRootDocument) {
	t.Helper()
	if err := skill.SaveTrustRoot(skill.TrustRootPathFor(dir, r1dir.Canonical), doc); err != nil {
		t.Fatalf("SaveTrustRoot: %v", err)
	}
}

// noPin forces the pinned-root-key env off so a stray ambient value can't
// change behavior in the un-pinned (v1 fallback) tests.
func noPin(t *testing.T) { t.Helper(); t.Setenv("R1_TRUST_ROOT_PUBKEY", "") }

// --- LOAD path -----------------------------------------------------------

func TestEnforceTrustRootLoad_UntrustedKidRejected(t *testing.T) {
	noPin(t)
	dir := t.TempDir()
	good, _ := genTrustKey(t)
	kidGood := skill.DeriveKeyID(good)
	writeTrustRoot(t, dir, docWithKey(kidGood, b64(good)))

	err := enforceTrustRootForLoad(dir, "mypack", &skillmfr.PackSignature{KeyID: "kid-evil"})
	if err == nil || !strings.Contains(err.Error(), "not in trust root") {
		t.Fatalf("want not-in-trust-root rejection, got %v", err)
	}
}

func TestEnforceTrustRootLoad_ExpiredKeyRejected(t *testing.T) {
	noPin(t)
	dir := t.TempDir()
	pub, _ := genTrustKey(t)
	kid := skill.DeriveKeyID(pub)
	doc := docWithKey(kid, b64(pub))
	doc.Keys[0].NotAfter = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	writeTrustRoot(t, dir, doc)

	err := enforceTrustRootForLoad(dir, "mypack", &skillmfr.PackSignature{KeyID: kid})
	if !errors.Is(err, skill.ErrTrustRootKeyExpired) {
		t.Fatalf("want ErrTrustRootKeyExpired, got %v", err)
	}
}

func TestEnforceTrustRootLoad_TrustedKidPasses(t *testing.T) {
	noPin(t)
	dir := t.TempDir()
	pub, _ := genTrustKey(t)
	kid := skill.DeriveKeyID(pub)
	writeTrustRoot(t, dir, docWithKey(kid, b64(pub)))

	if err := enforceTrustRootForLoad(dir, "mypack", &skillmfr.PackSignature{KeyID: kid}); err != nil {
		t.Fatalf("trusted kid should pass (no pin), got %v", err)
	}
}

func TestEnforceTrustRootLoad_NoDocFallback(t *testing.T) {
	noPin(t)
	dir := t.TempDir()
	if err := enforceTrustRootForLoad(dir, "mypack", &skillmfr.PackSignature{KeyID: "any"}); err != nil {
		t.Fatalf("absent trust root should fall back to nil, got %v", err)
	}
}

func TestEnforceTrustRootLoad_UnsignedPackPasses(t *testing.T) {
	noPin(t)
	dir := t.TempDir()
	pub, _ := genTrustKey(t)
	writeTrustRoot(t, dir, docWithKey(skill.DeriveKeyID(pub), b64(pub)))
	if err := enforceTrustRootForLoad(dir, "mypack", nil); err != nil {
		t.Fatalf("unsigned pack should pass (v1 fallback), got %v", err)
	}
}

// --- pinned root-signature verification (closes the tamper gap) ----------

func TestEnforceTrustRootLoad_PinnedTamperRejected(t *testing.T) {
	dir := t.TempDir()
	rootPub, rootPriv := genTrustKey(t)
	pkPub, _ := genTrustKey(t)
	kid := skill.DeriveKeyID(pkPub)
	doc := docWithKey(kid, b64(pkPub))
	if err := skill.SignTrustRoot(doc, rootPriv); err != nil {
		t.Fatalf("SignTrustRoot: %v", err)
	}
	// Attacker appends their own key AFTER the root signed the document.
	evilPub, _ := genTrustKey(t)
	doc.Keys = append(doc.Keys, skill.TrustRootEntry{
		KeyID: "kid-evil", PublicKey: b64(evilPub), Authority: skill.AuthorityR1,
	})
	writeTrustRoot(t, dir, doc)
	t.Setenv("R1_TRUST_ROOT_PUBKEY", b64(rootPub))

	// Even the injected evil kid is rejected because the DOCUMENT signature
	// no longer verifies against the pinned root key.
	err := enforceTrustRootForLoad(dir, "mypack", &skillmfr.PackSignature{KeyID: "kid-evil"})
	if !errors.Is(err, skill.ErrTrustRootSignatureInvalid) {
		t.Fatalf("tampered trust root must fail closed, got %v", err)
	}
}

func TestEnforceTrustRootLoad_PinnedValidPasses(t *testing.T) {
	dir := t.TempDir()
	rootPub, rootPriv := genTrustKey(t)
	pkPub, _ := genTrustKey(t)
	kid := skill.DeriveKeyID(pkPub)
	doc := docWithKey(kid, b64(pkPub))
	if err := skill.SignTrustRoot(doc, rootPriv); err != nil {
		t.Fatalf("SignTrustRoot: %v", err)
	}
	writeTrustRoot(t, dir, doc)
	t.Setenv("R1_TRUST_ROOT_PUBKEY", b64(rootPub))

	if err := enforceTrustRootForLoad(dir, "mypack", &skillmfr.PackSignature{KeyID: kid}); err != nil {
		t.Fatalf("validly-signed pinned doc + trusted kid should pass, got %v", err)
	}
}

func TestEnforceTrustRootLoad_PinnedUnsignedRejected(t *testing.T) {
	dir := t.TempDir()
	rootPub, _ := genTrustKey(t)
	pkPub, _ := genTrustKey(t)
	kid := skill.DeriveKeyID(pkPub)
	writeTrustRoot(t, dir, docWithKey(kid, b64(pkPub))) // no signature
	t.Setenv("R1_TRUST_ROOT_PUBKEY", b64(rootPub))

	err := enforceTrustRootForLoad(dir, "mypack", &skillmfr.PackSignature{KeyID: kid})
	if !errors.Is(err, skill.ErrTrustRootSignatureInvalid) {
		t.Fatalf("pinned + unsigned doc must fail closed, got %v", err)
	}
}

func TestEnforceTrustRootLoad_MalformedPinRejected(t *testing.T) {
	dir := t.TempDir()
	pub, _ := genTrustKey(t)
	kid := skill.DeriveKeyID(pub)
	writeTrustRoot(t, dir, docWithKey(kid, b64(pub)))
	t.Setenv("R1_TRUST_ROOT_PUBKEY", "not-valid-base64!!!")

	if err := enforceTrustRootForLoad(dir, "mypack", &skillmfr.PackSignature{KeyID: kid}); err == nil {
		t.Fatalf("malformed R1_TRUST_ROOT_PUBKEY should error, got nil")
	}
}

// --- ADOPT path (symmetric) ----------------------------------------------

func TestCheckTrustRootAdopt_UntrustedKidRejected(t *testing.T) {
	noPin(t)
	dir := t.TempDir()
	good, _ := genTrustKey(t)
	writeTrustRoot(t, dir, docWithKey(skill.DeriveKeyID(good), b64(good)))

	_, err := checkTrustRootForAdopt(dir, "mypack", &skillmfr.PackSignature{KeyID: "kid-evil"}, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "not in trust root") {
		t.Fatalf("adopt: want not-in-trust-root rejection, got %v", err)
	}
}

func TestCheckTrustRootAdopt_PinnedTamperRejected(t *testing.T) {
	dir := t.TempDir()
	rootPub, rootPriv := genTrustKey(t)
	pkPub, _ := genTrustKey(t)
	kid := skill.DeriveKeyID(pkPub)
	doc := docWithKey(kid, b64(pkPub))
	if err := skill.SignTrustRoot(doc, rootPriv); err != nil {
		t.Fatalf("SignTrustRoot: %v", err)
	}
	evilPub, _ := genTrustKey(t)
	doc.Keys = append(doc.Keys, skill.TrustRootEntry{
		KeyID: "kid-evil", PublicKey: b64(evilPub), Authority: skill.AuthorityR1,
	})
	writeTrustRoot(t, dir, doc)
	t.Setenv("R1_TRUST_ROOT_PUBKEY", b64(rootPub))

	_, err := checkTrustRootForAdopt(dir, "mypack", &skillmfr.PackSignature{KeyID: "kid-evil"}, time.Now().UTC())
	if !errors.Is(err, skill.ErrTrustRootSignatureInvalid) {
		t.Fatalf("adopt: tampered trust root must fail closed, got %v", err)
	}
}
