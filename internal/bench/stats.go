// Package bench statistics — Wilson score CI for the
// TruthfulCompletion leaderboard.
//
// Spec: specs/truthful-completion-benchmark.md §T8 + methodology
// document §7.1.

package bench

import "math"

// WilsonCI computes the Wilson score confidence interval for a
// binomial proportion. p is the observed proportion (clamped to
// [0,1]), n is the sample size, z is the standard-normal critical
// value (z = 1.96 → 95% CI; z = 2.576 → 99% CI).
//
// Returns (low, high) clamped to [0, 1].
//
// Closed form:
//   center = (p + z²/(2n)) / (1 + z²/n)
//   margin = (z / (1 + z²/n)) · sqrt( p(1-p)/n + z²/(4n²) )
//
// Edge cases:
//   n=0   → (0, 1). The interval is undefined; the conservative
//           bound carries no information so callers can't claim a
//           tight estimate from zero samples.
//   z=0   → (p, p). Zero-width interval; degenerate.
//   p<0   → clamped to 0.
//   p>1   → clamped to 1.
func WilsonCI(p float64, n int, z float64) (low, high float64) {
	if n <= 0 {
		return 0, 1
	}
	if z == 0 {
		// Zero-width: no margin. Still clamp to [0,1].
		c := math.Max(0, math.Min(1, p))
		return c, c
	}
	// Clamp p.
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}

	nf := float64(n)
	z2 := z * z

	denom := 1 + z2/nf
	center := (p + z2/(2*nf)) / denom
	margin := (z / denom) * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf))

	low = center - margin
	high = center + margin

	// Clamp to [0,1] — algebraically the Wilson interval is in
	// [0,1] when 0 ≤ p ≤ 1 and z > 0, but floating-point rounding
	// can put low slightly below 0 at p=0.
	if low < 0 {
		low = 0
	}
	if high > 1 {
		high = 1
	}
	return low, high
}
