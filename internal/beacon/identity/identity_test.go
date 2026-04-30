package identity

import (
	"crypto/ed25519"
	"testing"
	"time"
)

func TestNewBeaconAndOperatorIDs(t *testing.T) {
	beacon, _, err := NewBeacon("Eric Laptop", "constitution-v1")
	if err != nil {
		t.Fatalf("NewBeacon: %v", err)
	}
	if beacon.BeaconID == "" || beacon.Fingerprint() == "" {
		t.Fatalf("expected beacon identifiers to be populated")
	}
	operator, _, err := NewOperator("eric@example.com")
	if err != nil {
		t.Fatalf("NewOperator: %v", err)
	}
	if operator.OperatorID == "" || operator.Fingerprint() == "" {
		t.Fatalf("expected operator identifiers to be populated")
	}
}

func TestDeviceCertRoundTrip(t *testing.T) {
	op, opPriv, err := NewOperator("eric@example.com")
	if err != nil {
		t.Fatalf("NewOperator: %v", err)
	}
	dev, _, err := NewDevice("mobile-ios", "phone")
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	now := time.Now().UTC()
	cert, err := SignDeviceCert(op, opPriv, dev, now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("SignDeviceCert: %v", err)
	}
	if err := VerifyDeviceCert(cert, now.Add(time.Hour)); err != nil {
		t.Fatalf("VerifyDeviceCert: %v", err)
	}
	cert.Signature = append([]byte(nil), cert.Signature...)
	cert.Signature[0] ^= 0xFF
	if err := VerifyDeviceCert(cert, now.Add(time.Hour)); err == nil {
		t.Fatal("expected tampered cert to fail verification")
	}
}

func TestVerifyHelpers(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	msg := []byte("beacon")
	sig := ed25519.Sign(priv, msg)
	pub := priv.Public().(ed25519.PublicKey)
	if !verify(pub, msg, sig) {
		t.Fatal("expected signature verification to succeed")
	}
}
