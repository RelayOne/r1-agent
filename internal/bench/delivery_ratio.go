// delivery_ratio.go — TIER 1-E: refuse to mark a task complete when the
// actual touched-byte count is significantly under the dispatcher's
// estimate without an explicit reasoning artifact.
//
// Mirrors the delta logic baked into codexjob (PROOFS.md / REASONING.md)
// and into internal/daemon/queue.go. This is the package-local primitive
// other R1 callers use when they want the same enforcement.
package bench

import (
	"errors"
	"fmt"
	"os"
)

// DefaultDeliveryThresholdPercent is the cutoff. Below this, the task is
// flagged "underdelivered" and the supervisor must read the reasoning
// artifact before marking it complete.
const DefaultDeliveryThresholdPercent = 80

// DeliveryRatio reports how much code a task actually touched relative to
// the dispatcher's estimate.
type DeliveryRatio struct {
	EstimateBytes  int64  `json:"estimate_bytes"`
	ActualBytes    int64  `json:"actual_bytes"`
	Percent        int    `json:"percent"` // (actual * 100) / estimate; 0 if estimate == 0
	Underdelivered bool   `json:"underdelivered"`
	ReasoningPath  string `json:"reasoning_path,omitempty"` // set if a REASONING.md was provided
}

// Compute returns the delivery ratio for one task. Threshold is the
// percent below which Underdelivered will be flagged; pass 0 to use the
// default (80%). reasoningPath is optional; when set and pointing to a
// real file with non-trivial content, it satisfies the under-delivery
// requirement and Underdelivered stays false even if percent is below
// threshold.
func Compute(estimateBytes, actualBytes int64, threshold int, reasoningPath string) (DeliveryRatio, error) {
	if estimateBytes < 0 || actualBytes < 0 {
		return DeliveryRatio{}, errors.New("byte counts must be >= 0")
	}
	if threshold <= 0 {
		threshold = DefaultDeliveryThresholdPercent
	}
	r := DeliveryRatio{EstimateBytes: estimateBytes, ActualBytes: actualBytes, ReasoningPath: reasoningPath}
	if estimateBytes == 0 {
		// No estimate provided: caller did not opt in to the check.
		return r, nil
	}
	r.Percent = int(actualBytes * 100 / estimateBytes)
	if r.Percent >= threshold {
		return r, nil
	}
	// Below threshold: the reasoning file must exist AND contain enough
	// content to plausibly justify the deviation.
	if reasoningPath != "" {
		info, err := os.Stat(reasoningPath)
		if err == nil && !info.IsDir() && info.Size() >= 200 {
			// Reasoning provided and substantive enough — accept.
			return r, nil
		}
	}
	r.Underdelivered = true
	return r, nil
}

// Format renders a DeliveryRatio as a one-line human-readable string.
func (r DeliveryRatio) Format() string {
	if r.EstimateBytes == 0 {
		return fmt.Sprintf("delivered %d bytes (no estimate set)", r.ActualBytes)
	}
	flag := ""
	if r.Underdelivered {
		flag = " UNDERDELIVERED"
	}
	return fmt.Sprintf("delivered %d/%d bytes (%d%%)%s", r.ActualBytes, r.EstimateBytes, r.Percent, flag)
}
