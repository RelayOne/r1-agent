package pairing

import (
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/beacon/identity"
	"github.com/RelayOne/r1/internal/beacon/session"
)

func TestPairingFlowAndSASMismatch(t *testing.T) {
	beacon, _, err := identity.NewBeacon("laptop", "constitution-v1")
	if err != nil {
		t.Fatalf("NewBeacon: %v", err)
	}
	op, _, err := identity.NewOperator("eric@example.com")
	if err != nil {
		t.Fatalf("NewOperator: %v", err)
	}
	dev, _, err := identity.NewDevice("mobile-ios", "phone")
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	beaconEphemeral, err := session.NewEphemeralKeypair()
	if err != nil {
		t.Fatalf("NewEphemeralKeypair beacon: %v", err)
	}
	deviceEphemeral, err := session.NewEphemeralKeypair()
	if err != nil {
		t.Fatalf("NewEphemeralKeypair device: %v", err)
	}
	challenge, err := NewChallenge(beacon, beaconEphemeral.PublicKey)
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}
	response, err := NewResponse(challenge, dev, deviceEphemeral.PublicKey, op.PublicKey)
	if err != nil {
		t.Fatalf("NewResponse: %v", err)
	}
	sas1, err := VerifySAS(challenge, response)
	if err != nil {
		t.Fatalf("VerifySAS: %v", err)
	}
	sas2, err := VerifySAS(challenge, response)
	if err != nil {
		t.Fatalf("VerifySAS second: %v", err)
	}
	if sas1 != sas2 {
		t.Fatalf("sas mismatch %q vs %q", sas1, sas2)
	}
	response.ChallengeWords[0] = "dead"
	if _, err := VerifySAS(challenge, response); err == nil {
		t.Fatal("expected SAS verification to fail on word mismatch")
	}
}

func TestChallengeExpiry(t *testing.T) {
	beacon, _, err := identity.NewBeacon("laptop", "constitution-v1")
	if err != nil {
		t.Fatalf("NewBeacon: %v", err)
	}
	challenge, err := NewChallenge(beacon, make([]byte, 32))
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}
	if err := challenge.Validate(challenge.ExpiresAt.Add(time.Second)); err == nil {
		t.Fatal("expected expired challenge to fail validation")
	}
}
