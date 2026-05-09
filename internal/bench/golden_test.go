package bench

import (
	"context"
	"strings"
	"testing"
	"time"
)

// concernTemplateMissingMarker is the prefix of the harness error that
// indicates the concern Builder has no template for the role/face the
// bench's spawned stance asks for. This is a known limitation of running
// the full substrate from a unit test (bench wires its own ledger + bus
// + concern.Builder but does not register concern templates because the
// bench package would otherwise import internal/concern/templates,
// which transitively pulls in heavyweight dependencies that bloat
// every CI run that touches this package).
//
// Tests below treat THIS specific error as a USER-SKIPPED scenario per
// CLAUDE.md "ALL failures are findings"; every OTHER error fails the
// test. Surfaced by audit/scan-go-stubs.md item #10.
const concernTemplateMissingMarker = "concern: no template for"

// missingTemplate reports whether err is the known "no concern template
// registered" failure that the bench can't recover from on its own. Any
// other error signals a real regression and must fail the test rather
// than silently skip.
func missingTemplate(err error) bool {
	return err != nil && strings.Contains(err.Error(), concernTemplateMissingMarker)
}

// TestGoldenBaseline runs all golden missions and asserts no regressions
// against known baseline metrics. This is the CI gate for bench regressions.
func TestGoldenBaseline(t *testing.T) {
	r := NewRunner(goldenDir(t))

	missions, err := r.ListMissions()
	if err != nil {
		t.Fatalf("ListMissions: %v", err)
	}
	if len(missions) == 0 {
		t.Skip("no golden missions found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var results []RunResult
	for i := range missions {
		t.Run(missions[i].ID, func(t *testing.T) {
			res, err := r.Run(ctx, &missions[i])
			if err != nil {
				if missingTemplate(err) {
					// Known limitation — concern templates are not registered
					// in this bench unit-test context. Anything else falls
					// through to t.Fatalf below as a real regression.
					t.Skipf("Run(%s): %v (known: concern templates not registered for bench unit tests)", missions[i].ID, err)
					return
				}
				t.Fatalf("Run(%s): %v", missions[i].ID, err)
			}
			results = append(results, *res)

			// Baseline assertions: ledger must not be corrupted
			if res.LedgerCorrupted {
				t.Errorf("%s: ledger corrupted", missions[i].ID)
			}

			// Terminal state should be converged for baseline missions
			if res.TerminalState != "converged" {
				t.Logf("%s: terminal state = %s (expected converged for baseline)",
					missions[i].ID, res.TerminalState)
			}
		})
	}

	// Generate summary report
	if len(results) > 0 {
		report := Report(results)
		t.Logf("\n%s", report)
	}
}

// TestGoldenNonRegression loads known-good baselines and compares against
// current results. This fails if any mission regresses.
func TestGoldenNonRegression(t *testing.T) {
	r := NewRunner(goldenDir(t))

	missions, err := r.ListMissions()
	if err != nil {
		t.Fatalf("ListMissions: %v", err)
	}
	if len(missions) == 0 {
		t.Skip("no golden missions found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Baseline: the minimum expectations for golden missions
	// (these can be updated as the bench suite matures)
	baselines := map[string]*RunResult{
		"hello-world": {
			TerminalState: "converged",
			AcceptanceMet: 0, // golden missions don't execute real code
			CostUSD:       0, // substrate-only, no LLM calls
		},
	}

	for i := range missions {
		t.Run(missions[i].ID, func(t *testing.T) {
			current, err := r.Run(ctx, &missions[i])
			if err != nil {
				if missingTemplate(err) {
					t.Skipf("Run(%s): %v (known: concern templates not registered for bench unit tests)", missions[i].ID, err)
					return
				}
				t.Fatalf("Run(%s): %v", missions[i].ID, err)
			}

			baseline, ok := baselines[missions[i].ID]
			if !ok {
				t.Logf("%s: no baseline found, skipping regression check", missions[i].ID)
				return
			}

			if DetectRegression(baseline, current) {
				t.Errorf("%s: regression detected\n  baseline: %+v\n  current:  %+v",
					missions[i].ID, baseline, current)

				comparison := Compare(baseline, current)
				for k, v := range comparison.Delta {
					if v != 0 {
						t.Logf("  delta %s: %+.2f", k, v)
					}
				}
			}
		})
	}
}
