package oneshot

import (
	"runtime/debug"
	"testing"
)

// TestApplyMemoryLimit_Headroom — applyMemoryLimit sets the Go
// runtime soft cap to a value within the [85%, 90%] window of
// the requested hard ceiling. Pins the HeadroomRatio constant
// against drift. Spec §T1.2.
func TestApplyMemoryLimit_Headroom(harness *testing.T) {
	setSoft := debug.SetMemoryLimit
	apply := applyMemoryLimit

	prev := setSoft(-1)
	harness.Cleanup(func() {
		setSoft(prev)
		if prev == 0 {
			harness.Errorf("prev soft cap unexpectedly zero")
		}
	})

	if err := apply(8192); err != nil {
		harness.Fatalf("applyMemoryLimit: %v", err)
	}
	got := setSoft(-1)

	hard := int64(8192) << 20
	low := int64(float64(hard) * 0.85)
	high := int64(float64(hard) * 0.90)
	if got < low || got > high {
		harness.Errorf("soft cap=%d want in [%d, %d] (85 to 90 percent of %d)",
			got, low, high, hard)
	}
}
