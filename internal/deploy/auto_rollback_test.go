package deploy

import (
	"testing"
	"time"
)

// TestAutoRollback walks the full truth-table of the triple-condition
// predicate documented in specs/deploy-executor.md §Auto-Rollback
// Decision Tree. Each row encodes (statusCode, consoleErrCount,
// elapsed) → want, with a label that explains which clause dominates.
// Covering every corner here gives us one place to catch any future
// drift toward a looser policy (say, an OR-of-two bug).
func TestAutoRollback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		status     int
		errs       int
		elapsed    time.Duration
		wantFire   bool
		wantReason string
	}{
		{
			name:       "trigger: all three conditions met",
			status:     500,
			errs:       2,
			elapsed:    45 * time.Second,
			wantFire:   true,
			wantReason: "trigger",
		},
		{
			name:       "trigger: 502 with errors after warm-up",
			status:     502,
			errs:       1,
			elapsed:    31 * time.Second,
			wantFire:   true,
			wantReason: "trigger",
		},
		{
			name:       "no trigger: status 200 blocks everything",
			status:     200,
			errs:       5,
			elapsed:    60 * time.Second,
			wantFire:   false,
			wantReason: "status_ok",
		},
		{
			name:       "no trigger: no console errors (status=500, post-warmup)",
			status:     500,
			errs:       0,
			elapsed:    60 * time.Second,
			wantFire:   false,
			wantReason: "no_console_errs",
		},
		{
			name:       "no trigger: negative err count treated as zero",
			status:     500,
			errs:       -1,
			elapsed:    60 * time.Second,
			wantFire:   false,
			wantReason: "no_console_errs",
		},
		{
			name:       "no trigger: within warm-up window (15s elapsed)",
			status:     500,
			errs:       3,
			elapsed:    15 * time.Second,
			wantFire:   false,
			wantReason: "within_warmup",
		},
		{
			name:       "no trigger: exactly at warm-up boundary (30s)",
			status:     500,
			errs:       3,
			elapsed:    30 * time.Second,
			wantFire:   false,
			wantReason: "within_warmup",
		},
		{
			name:       "trigger: just past warm-up boundary (30s + 1ns)",
			status:     500,
			errs:       3,
			elapsed:    30*time.Second + time.Nanosecond,
			wantFire:   true,
			wantReason: "trigger",
		},
		{
			name:       "no trigger: zero elapsed (instant verify)",
			status:     500,
			errs:       3,
			elapsed:    0,
			wantFire:   false,
			wantReason: "within_warmup",
		},
		{
			name:       "no trigger: negative elapsed (clock skew)",
			status:     500,
			errs:       3,
			elapsed:    -5 * time.Second,
			wantFire:   false,
			wantReason: "within_warmup",
		},
		{
			name:       "status_ok reason reports first (errs+warmup also fail)",
			status:     200,
			errs:       0,
			elapsed:    10 * time.Second,
			wantFire:   false,
			wantReason: "status_ok",
		},
		{
			name:       "no_console_errs reason reports before within_warmup",
			status:     500,
			errs:       0,
			elapsed:    10 * time.Second,
			wantFire:   false,
			wantReason: "no_console_errs",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotFire := AutoRollback(tc.status, tc.errs, tc.elapsed)
			if gotFire != tc.wantFire {
				t.Errorf("AutoRollback(%d,%d,%v) = %v, want %v",
					tc.status, tc.errs, tc.elapsed, gotFire, tc.wantFire)
			}
			gotReason := AutoRollbackReason(tc.status, tc.errs, tc.elapsed)
			if gotReason != tc.wantReason {
				t.Errorf("AutoRollbackReason(%d,%d,%v) = %q, want %q",
					tc.status, tc.errs, tc.elapsed, gotReason, tc.wantReason)
			}
			// Invariant: reason == "trigger" iff fire == true. Protects
			// against a future refactor that splits the two functions
			// and lets them disagree.
			wantTrigger := (gotReason == "trigger")
			if wantTrigger != gotFire {
				t.Errorf("reason/fire agreement broken: fire=%v reason=%q", gotFire, gotReason)
			}
		})
	}
}

// TestAutoRollbackWarmupConstant pins the documented 30-second window.
// If a future commit tunes this value the test will fail loudly; the
// spec (specs/deploy-executor.md §Auto-Rollback Decision Tree) is the
// authoritative source and must be updated in the same change.
func TestAutoRollbackWarmupConstant(t *testing.T) {
	t.Parallel()
	if AutoRollbackWarmup != 30*time.Second {
		t.Errorf("AutoRollbackWarmup = %v, want 30s (see specs/deploy-executor.md)",
			AutoRollbackWarmup)
	}
}

// TestAutoRollbackPurity confirms the predicate is a pure function —
// identical inputs produce identical outputs across many invocations,
// and no hidden state (time.Now, package globals, etc.) leaks in.
// Regression guard for any future refactor that tries to add a clock
// dependency directly.
func TestAutoRollbackPurity(t *testing.T) {
	t.Parallel()
	const iterations = 1000
	for i := 0; i < iterations; i++ {
		if !AutoRollback(500, 1, 45*time.Second) {
			t.Fatalf("iteration %d: expected trigger=true for fixed inputs", i)
		}
		if AutoRollback(200, 1, 45*time.Second) {
			t.Fatalf("iteration %d: expected trigger=false when status=200", i)
		}
	}
}
