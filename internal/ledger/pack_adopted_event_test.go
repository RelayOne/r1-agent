package ledger

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func TestSignVerifyPackAdopted(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	payload := PackAdoptedPayload{
		PackID:        "alpha",
		PackVersion:   "1.0.0",
		TargetProduct: "cloudswarm",
		TenantID:      "",
		SignerKeyID:   "ed25519:abc",
		AdoptedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	signed, err := SignPackAdopted(payload, priv)
	if err != nil {
		t.Fatalf("SignPackAdopted: %v", err)
	}
	if signed.SignatureHex == "" {
		t.Fatalf("SignatureHex empty")
	}
	if err := VerifyPackAdoptedSignature(signed, pub); err != nil {
		t.Fatalf("VerifyPackAdoptedSignature: %v", err)
	}
}

func TestVerifyPackAdoptedTampered(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signed, err := SignPackAdopted(PackAdoptedPayload{
		PackID: "alpha", PackVersion: "1", TargetProduct: "heroa",
		AdoptedAt: time.Now().UTC().Format(time.RFC3339),
	}, priv)
	if err != nil {
		t.Fatalf("SignPackAdopted: %v", err)
	}
	signed.TargetProduct = "veritize"
	if err := VerifyPackAdoptedSignature(signed, pub); !errors.Is(err, ErrPackAdoptedSignatureInvalid) {
		t.Fatalf("want ErrPackAdoptedSignatureInvalid, got %v", err)
	}
}

func TestSignPackAdoptedMissingFields(t *testing.T) {
	t.Parallel()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if _, err := SignPackAdopted(PackAdoptedPayload{PackID: ""}, priv); err == nil {
		t.Fatalf("want err for empty pack_id")
	}
}

func TestPersistPackAdopted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	payload := PackAdoptedPayload{
		PackID: "alpha", PackVersion: "1.0", TargetProduct: "r1",
		AdoptedAt: time.Now().UTC().Format(time.RFC3339),
	}
	n, err := PersistPackAdopted(store, "pa-1", payload, "test")
	if err != nil {
		t.Fatalf("PersistPackAdopted: %v", err)
	}
	if n.Type != "pack.adopted" {
		t.Fatalf("Type = %q", n.Type)
	}
	got, err := store.ReadNode("pa-1")
	if err != nil {
		t.Fatalf("ReadNode: %v", err)
	}
	if got.Type != "pack.adopted" {
		t.Fatalf("read Type = %q", got.Type)
	}
}
