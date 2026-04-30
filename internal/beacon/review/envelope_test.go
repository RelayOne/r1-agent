package review

import (
	"testing"
	"time"
)

func TestEnvelopeMarshalAndValidate(t *testing.T) {
	env := Envelope{
		BeaconID:    "bc-123",
		SessionID:   "sess-123",
		ArtifactRef: "sha256:abc",
		RequestedAt: time.Now().UTC(),
		Reason:      "offline review requested",
	}
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("expected marshaled envelope")
	}
	env.ArtifactRef = ""
	if err := env.Validate(); err == nil {
		t.Fatal("expected invalid envelope to fail validation")
	}
}
