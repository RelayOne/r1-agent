package failure

import (
	"errors"
	"testing"
	"time"
)

func TestMirrorLagClassifierDetectMatchesCommitReadRace(t *testing.T) {
	classifier := MirrorLagClassifier{}
	err := errors.New(`Cloud Build failed: Couldn't read commit "abc123" from mirrored repo`)
	if !classifier.Detect(err) {
		t.Fatal("expected mirror lag detection")
	}
}

func TestMirrorLagClassifierDetectRejectsUnrelatedErrors(t *testing.T) {
	classifier := MirrorLagClassifier{}
	err := errors.New("go test failed: expected 200, got 500")
	if classifier.Detect(err) {
		t.Fatal("did not expect mirror lag detection")
	}
}

func TestMirrorLagClassifierShouldRetryOnce(t *testing.T) {
	classifier := MirrorLagClassifier{}
	delay, ok := classifier.ShouldRetry(1)
	if !ok {
		t.Fatal("expected first attempt to retry")
	}
	if delay != 30*time.Second {
		t.Fatalf("delay = %v, want 30s", delay)
	}

	delay, ok = classifier.ShouldRetry(2)
	if ok {
		t.Fatal("did not expect second retry")
	}
	if delay != 0 {
		t.Fatalf("delay = %v, want 0", delay)
	}
}
