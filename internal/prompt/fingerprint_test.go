package prompt

import (
	"testing"
)

func TestComputeFingerprint(t *testing.T) {
	fp1 := ComputeFingerprint("You are an AI assistant.")
	fp2 := ComputeFingerprint("You are an AI assistant.")
	fp3 := ComputeFingerprint("You are a coding agent.")

	if !fp1.Same(fp2) {
		t.Error("identical prompts should have same fingerprint")
	}
	if fp1.Same(fp3) {
		t.Error("different prompts should have different fingerprints")
	}
	if fp1.HexHash == "" {
		t.Error("hex hash should not be empty")
	}
}

func TestDynamicBoundary(t *testing.T) {
	static := "You are an AI assistant.\n"
	dynamic := "Today's date is 2026-04-03.\nSession: abc-123\n"
	prompt := static + DynamicBoundary + dynamic

	fp := ComputeFingerprint(prompt)
	if fp.StaticLen != len(static) {
		t.Errorf("expected static len %d, got %d", len(static), fp.StaticLen)
	}
	if fp.DynamicLen != len(dynamic) {
		t.Errorf("expected dynamic len %d, got %d", len(dynamic), fp.DynamicLen)
	}

	// Same static, different dynamic should have same fingerprint
	dynamic2 := "Today's date is 2026-04-04.\nSession: def-456\n"
	prompt2 := static + DynamicBoundary + dynamic2
	fp2 := ComputeFingerprint(prompt2)

	if !fp.Same(fp2) {
		t.Error("same static content should produce same fingerprint regardless of dynamic content")
	}
}

func TestTimestampNormalization(t *testing.T) {
	// Timestamps in static content should be normalized away
	fp1 := ComputeFingerprint("Created at 2026-01-01T10:00:00 by system")
	fp2 := ComputeFingerprint("Created at 2026-04-03T14:30:00 by system")

	if !fp1.Same(fp2) {
		t.Error("prompts differing only by timestamp should have same fingerprint")
	}
}

func TestUUIDNormalization(t *testing.T) {
	fp1 := ComputeFingerprint("Session a0bc7432-fc20-4008-b616-f4710ccbd3bc active")
	fp2 := ComputeFingerprint("Session 12345678-abcd-ef01-2345-678901234567 active")

	if !fp1.Same(fp2) {
		t.Error("prompts differing only by UUID should have same fingerprint")
	}
}

func TestTracker(t *testing.T) {
	tracker := NewTracker()

	// First update - no break
	broke := tracker.Update("System prompt v1")
	if broke {
		t.Error("first update should not be a cache break")
	}
	if tracker.Current().Version != 1 {
		t.Errorf("expected version 1, got %d", tracker.Current().Version)
	}

	// Same prompt - no break
	broke = tracker.Update("System prompt v1")
	if broke {
		t.Error("same prompt should not be a cache break")
	}

	// Different prompt - break!
	broke = tracker.Update("System prompt v2 with new instructions")
	if !broke {
		t.Error("different prompt should be a cache break")
	}
	if tracker.Breaks() != 1 {
		t.Errorf("expected 1 break, got %d", tracker.Breaks())
	}
	if tracker.Current().Version != 2 {
		t.Errorf("expected version 2, got %d", tracker.Current().Version)
	}

	// History
	history := tracker.History()
	if len(history) != 3 {
		t.Errorf("expected 3 history entries, got %d", len(history))
	}
}

func TestBuildCacheStablePrompt(t *testing.T) {
	prompt := BuildCacheStablePrompt(
		[]string{"You are an AI assistant.", "Follow these rules:"},
		[]string{"Current task: fix bug", "File: main.go"},
	)

	if !contains(prompt, DynamicBoundary) {
		t.Error("prompt should contain dynamic boundary")
	}
	if !contains(prompt, "You are an AI assistant.") {
		t.Error("prompt should contain static parts")
	}
	if !contains(prompt, "Current task: fix bug") {
		t.Error("prompt should contain dynamic parts")
	}
}

func TestBuildCacheStablePromptNoDynamic(t *testing.T) {
	prompt := BuildCacheStablePrompt(
		[]string{"Static only"},
		nil,
	)
	if contains(prompt, DynamicBoundary) {
		t.Error("prompt without dynamic parts should not contain boundary")
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
