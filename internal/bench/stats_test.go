package bench

import (
	"math"
	"testing"
)

func TestWilsonCI_KnownValues(t *testing.T) {
	cases := []struct {
		name              string
		p                 float64
		n                 int
		z                 float64
		wantLow, wantHigh float64
		tol               float64
	}{
		// 95% CI cases (z = 1.96). Reference values from the standard
		// Wilson formula; verified against scipy.stats.binomtest CIs.
		{"p=0.96 n=100", 0.96, 100, 1.96, 0.901, 0.984, 0.005},
		{"p=0.45 n=100", 0.45, 100, 1.96, 0.357, 0.547, 0.005},
		{"p=0.50 n=100", 0.50, 100, 1.96, 0.404, 0.596, 0.005},
		// Edge cases
		{"n=0", 0.5, 0, 1.96, 0.0, 1.0, 0.0},
		// p=0/p=1 boundary: standard Wilson (no continuity correction).
		// The CI is symmetric about p=0.5: high@p=0 = 1 - low@p=1.
		{"p=0 n=10", 0.0, 10, 1.96, 0.0, 0.278, 0.005},
		{"p=1 n=10", 1.0, 10, 1.96, 0.722, 1.0, 0.005},
		// 99% CI
		{"99% CI p=0.96 n=100", 0.96, 100, 2.576, 0.875, 0.991, 0.005},
		// Zero margin
		{"z=0", 0.42, 100, 0.0, 0.42, 0.42, 0.001},
		// Clamping
		{"p > 1 clamps", 1.5, 100, 1.96, 0.963, 1.0, 0.005},
		{"p < 0 clamps", -0.2, 100, 1.96, 0.0, 0.037, 0.005},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			low, high := WilsonCI(tc.p, tc.n, tc.z)
			if math.Abs(low-tc.wantLow) > tc.tol {
				t.Errorf("low: got %.4f want %.4f (tol %.4f)", low, tc.wantLow, tc.tol)
			}
			if math.Abs(high-tc.wantHigh) > tc.tol {
				t.Errorf("high: got %.4f want %.4f (tol %.4f)", high, tc.wantHigh, tc.tol)
			}
			if low < 0 || low > 1 {
				t.Errorf("low %.4f outside [0,1]", low)
			}
			if high < 0 || high > 1 {
				t.Errorf("high %.4f outside [0,1]", high)
			}
			if low > high {
				t.Errorf("low (%.4f) > high (%.4f)", low, high)
			}
		})
	}
}

// TestWilsonCI_NeverNegative confirms the clamping handles the
// rounding case at p=0 n=very-small.
func TestWilsonCI_NeverNegative(t *testing.T) {
	for n := 1; n <= 5; n++ {
		low, high := WilsonCI(0.0, n, 1.96)
		if low < 0 {
			t.Errorf("n=%d: low = %.6f, want >= 0", n, low)
		}
		if high > 1 {
			t.Errorf("n=%d: high = %.6f, want <= 1", n, high)
		}
	}
}
