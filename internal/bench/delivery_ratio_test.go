package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryRatio_OnTarget(t *testing.T) {
	r, err := Compute(1000, 900, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Underdelivered {
		t.Errorf("90%% should not be underdelivered")
	}
	if r.Percent != 90 {
		t.Errorf("percent = %d, want 90", r.Percent)
	}
}

func TestDeliveryRatio_UnderThresholdFlags(t *testing.T) {
	r, _ := Compute(1000, 500, 0, "")
	if !r.Underdelivered {
		t.Errorf("50%% should be underdelivered")
	}
	if r.Percent != 50 {
		t.Errorf("percent = %d, want 50", r.Percent)
	}
}

func TestDeliveryRatio_ReasoningClearsFlag(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "REASONING.md")
	body := strings.Repeat("Why we shipped fewer bytes: legitimate scope cut explained at apps/api/x.ts:1.\n", 5)
	if err := os.WriteFile(rp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := Compute(1000, 300, 0, rp)
	if r.Underdelivered {
		t.Errorf("substantive REASONING should clear underdelivered flag")
	}
}

func TestDeliveryRatio_ShortReasoningRejected(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "REASONING.md")
	os.WriteFile(rp, []byte("idk"), 0o644)
	r, _ := Compute(1000, 300, 0, rp)
	if !r.Underdelivered {
		t.Errorf("3-byte reasoning should NOT clear flag")
	}
}

func TestDeliveryRatio_NoEstimateMeansOptedOut(t *testing.T) {
	r, _ := Compute(0, 100, 0, "")
	if r.Underdelivered {
		t.Errorf("zero estimate should never flag underdelivered")
	}
	if r.Percent != 0 {
		t.Errorf("percent = %d, want 0", r.Percent)
	}
}

func TestDeliveryRatio_CustomThreshold(t *testing.T) {
	// 70% delivered against a strict 90% threshold — should flag.
	r, _ := Compute(1000, 700, 90, "")
	if !r.Underdelivered {
		t.Errorf("70%% under 90%% threshold should flag")
	}
	// 70% against a lax 50% threshold — should pass.
	r2, _ := Compute(1000, 700, 50, "")
	if r2.Underdelivered {
		t.Errorf("70%% over 50%% threshold should pass")
	}
}

func TestDeliveryRatio_NegativeBytesError(t *testing.T) {
	if _, err := Compute(-1, 0, 0, ""); err == nil {
		t.Errorf("expected error on negative estimate")
	}
	if _, err := Compute(0, -1, 0, ""); err == nil {
		t.Errorf("expected error on negative actual")
	}
}

func TestDeliveryRatio_FormatString(t *testing.T) {
	r, _ := Compute(1000, 500, 0, "")
	s := r.Format()
	if !strings.Contains(s, "500/1000") {
		t.Errorf("format missing ratio: %q", s)
	}
	if !strings.Contains(s, "UNDERDELIVERED") {
		t.Errorf("format missing flag: %q", s)
	}
	r2, _ := Compute(0, 100, 0, "")
	if !strings.Contains(r2.Format(), "no estimate") {
		t.Errorf("format missing 'no estimate' note: %q", r2.Format())
	}
}
