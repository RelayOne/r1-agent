package session

import "testing"

func TestSessionRoundTripAndReplayRejection(t *testing.T) {
	beaconEphemeral, err := NewEphemeralKeypair()
	if err != nil {
		t.Fatalf("NewEphemeralKeypair beacon: %v", err)
	}
	deviceEphemeral, err := NewEphemeralKeypair()
	if err != nil {
		t.Fatalf("NewEphemeralKeypair device: %v", err)
	}
	beaconKeys, err := DeriveSessionKeys(beaconEphemeral.PrivateKey, deviceEphemeral.PublicKey, "beacon")
	if err != nil {
		t.Fatalf("DeriveSessionKeys beacon: %v", err)
	}
	deviceKeys, err := DeriveSessionKeys(deviceEphemeral.PrivateKey, beaconEphemeral.PublicKey, "device")
	if err != nil {
		t.Fatalf("DeriveSessionKeys device: %v", err)
	}
	if beaconKeys.SessionID != deviceKeys.SessionID {
		t.Fatalf("session ids differ: %s vs %s", beaconKeys.SessionID, deviceKeys.SessionID)
	}
	beaconChannel, err := NewSecureChannel(beaconKeys)
	if err != nil {
		t.Fatalf("NewSecureChannel beacon: %v", err)
	}
	deviceChannel, err := NewSecureChannel(deviceKeys)
	if err != nil {
		t.Fatalf("NewSecureChannel device: %v", err)
	}
	frame, err := deviceChannel.Encrypt([]byte("approve"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	plaintext, err := beaconChannel.Decrypt(frame)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(plaintext) != "approve" {
		t.Fatalf("plaintext=%q", plaintext)
	}
	if _, err := beaconChannel.Decrypt(frame); err == nil {
		t.Fatal("expected replayed frame to fail")
	}
}

func TestSharedSASDeterministic(t *testing.T) {
	a := SharedSAS([]byte("beacon"), []byte("device"), []byte("challenge"), []byte("response"))
	b := SharedSAS([]byte("beacon"), []byte("device"), []byte("challenge"), []byte("response"))
	if a != b {
		t.Fatalf("expected deterministic SAS, got %q vs %q", a, b)
	}
}
