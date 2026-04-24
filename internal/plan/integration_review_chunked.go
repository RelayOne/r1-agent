// Package plan — integration_review_chunked.go
//
// Timeout-resilient wrapper around RunIntegrationReview. When a
// session's scope is large (e.g. 13-task, 50+-file "Shared
// Packages" builds) the 10-minute default budget can lapse before
// the reviewer emits a JSON verdict. Previously this silently
// returned an empty report, which sow_native.go's Phase 1.4 read
// as "surface clean" — a false negative that let integration bugs
// through and caused ACs to plateau.
//
// Chunked mode retries-with-narrowed-scope on timeout. It
// enumerates the repo's natural package boundaries (dirs under
// apps/, packages/, services/, libs/, crates/, tools/ that carry
// a package.json, Cargo.toml, go.mod, or pyproject.toml) and
// dispatches one integration review per bucket with a budget
// proportional to the remaining time. Bounded recursion, two
// levels deep: full-scope attempt, then per-bucket attempts. If
// a per-bucket review also times out, the aggregate report
// records an "other"-kind gap describing the missed bucket so
// the caller can still dispatch a targeted follow-up.
package plan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/provider"
)

// chunkedMinBudget is the floor on a per-bucket review's time
// budget. Below this, the LLM doesn't have enough turns to read
// a package's manifests and run one or two greps.
const chunkedMinBudget = 90 * time.Second

// chunkedMaxBudget is the ceiling on a per-bucket review's time
// budget. Above this, a single bucket is consuming so much budget
// that it defeats the purpose of chunking.
const chunkedMaxBudget = 5 * time.Minute

// bucketScanMaxDepth caps how deep the bucket enumerator walks.
// Package manifests live at depth <= 2 under the standard
// monorepo roots (apps/foo/package.json, packages/bar/Cargo.toml);
// deeper trees are intermediate source dirs.
const bucketScanMaxDepth = 2

// bucketRoots is the set of conventional monorepo top-level dirs
// where per-package chunks live. Extended over time as new
// layouts appear.
var bucketRoots = []string{"apps", "packages", "services", "libs", "crates", "tools"}

// bucketManifests is the set of filenames that mark a directory
// as an independent package/service worthy of its own chunk.
var bucketManifests = []string{"package.json", "Cargo.toml", "go.mod", "pyproject.toml"}

// RunIntegrationReviewChunked is the timeout-resilient entry
// point. It starts with the full session scope. If that attempt
// times out, the function discovers the repo's natural package
// boundaries (directories under apps/, packages/, services/,
// etc. that contain a package.json, Cargo.toml, go.mod, or
// pyproject.toml) and dispatches one integration review per
// bucket, each with a budget = totalBudget / len(buckets).
//
// Up to two recursion levels. If per-chunk reviews also time out,
// it returns the aggregate of whatever chunks DID succeed plus a
// one-line summary noting the timeout scope.
//
// Prefer this over RunIntegrationReview when the session is
// large enough that the 10-minute default might not cover it.
func RunIntegrationReviewChunked(ctx context.Context, prov provider.Provider, model string, in IntegrationReviewInput, totalBudget time.Duration) (*IntegrationReport, error) {
	if prov == nil {
		return nil, nil
	}
	if totalBudget <= 0 {
		totalBudget = 10 * time.Minute
	}

	// Cap the ENTIRE chunked operation at totalBudget so the
	// fallback per-bucket attempts can't exceed the advertised
	// deadline. Without this, the original code would burn the
	// full totalBudget on the first attempt and then start a fresh
	// budget per bucket, multiplying total wall time by N+1 buckets.
	rootCtx, rootCancel := context.WithTimeout(ctx, totalBudget)
	defer rootCancel()

	// Attempt 1: full-scope review with up to half the total budget,
	// leaving the other half for chunked retries on timeout.
	firstAttemptBudget := totalBudget / 2
	if firstAttemptBudget < chunkedMinBudget {
		firstAttemptBudget = chunkedMinBudget
	}
	fullCtx, cancel := context.WithTimeout(rootCtx, firstAttemptBudget)
	report, err := RunIntegrationReview(fullCtx, prov, model, in)
	cancel()
	if err == nil && report != nil && !isTimeoutLikely(report, fullCtx.Err()) {
		return report, nil
	}

	// If the outer rootCtx is done (user cancel or total budget
	// elapsed), bail out — no point starting bucket attempts that
	// can't possibly complete.
	if rootCtx.Err() != nil {
		return report, rootCtx.Err()
	}

	// Attempt 2: enumerate buckets and review each narrowly.
	buckets := enumerateBuckets(in.RepoRoot)
	if len(buckets) == 0 {
		// Nothing to split by — return whatever the first attempt
		// produced so the caller isn't worse off than before.
		if report == nil {
			report = &IntegrationReport{Summary: "integration review timed out; no bucket boundaries to retry against"}
		}
		return report, err
	}

	fmt.Printf("  🔗 integration review: full-scope attempt timed out — chunking into %d bucket(s)\n", len(buckets))

	// Budget for chunked attempts = whatever's left of totalBudget.
	// Distribute across buckets, clamped to [min, max] per bucket.
	// Loop bails early when rootCtx is exhausted, so total wall time
	// is bounded by totalBudget regardless of bucket count.
	remaining := time.Until(rootDeadline(rootCtx))
	if remaining < chunkedMinBudget {
		remaining = chunkedMinBudget
	}
	perBucket := remaining / time.Duration(len(buckets))
	if perBucket < chunkedMinBudget {
		perBucket = chunkedMinBudget
	}
	if perBucket > chunkedMaxBudget {
		perBucket = chunkedMaxBudget
	}

	aggregate := &IntegrationReport{}
	var summaries []string

	for _, bucket := range buckets {
		if rootCtx.Err() != nil {
			break
		}
		subInput := in
		subInput.ScopeHint = bucket

		// Cap each per-bucket ctx by both perBucket AND remaining
		// rootCtx — whichever is tighter. A late-arriving rootCtx
		// deadline must not leak into a longer per-bucket sub-ctx.
		bctx, bcancel := context.WithTimeout(rootCtx, perBucket)
		subReport, subErr := RunIntegrationReview(bctx, prov, model, subInput)
		timedOut := isTimeoutLikely(subReport, bctx.Err())
		bcancel()

		if subErr != nil {
			fmt.Printf("     - bucket %s: error %v\n", bucket, subErr)
			aggregate.Gaps = append(aggregate.Gaps, IntegrationGap{
				Kind:              "other",
				Location:          "bucket:" + bucket,
				Detail:            fmt.Sprintf("integration reviewer errored for bucket %s: %v", bucket, subErr),
				SuggestedFollowup: "Rerun integration review for this bucket with a larger budget, or inspect logs for the underlying provider error.",
			})
			continue
		}
		if timedOut {
			fmt.Printf("     - bucket %s: timed out at per-bucket budget (%s)\n", bucket, perBucket)
			aggregate.Gaps = append(aggregate.Gaps, IntegrationGap{
				Kind:              "other",
				Location:          "bucket:" + bucket,
				Detail:            fmt.Sprintf("integration reviewer could not complete for bucket %s due to budget (%s)", bucket, perBucket),
				SuggestedFollowup: "Rerun integration review scoped to this bucket with more time, or split the bucket further.",
			})
			if subReport != nil && strings.TrimSpace(subReport.Summary) != "" {
				summaries = append(summaries, fmt.Sprintf("%s: %s (timed out)", bucket, firstLine(subReport.Summary)))
			}
			continue
		}
		if subReport != nil {
			aggregate.Gaps = append(aggregate.Gaps, subReport.Gaps...)
			if strings.TrimSpace(subReport.Summary) != "" {
				summaries = append(summaries, fmt.Sprintf("%s: %s", bucket, firstLine(subReport.Summary)))
			}
		}
	}

	aggregate.Summary = fmt.Sprintf("chunked review across %d bucket(s) — %s", len(buckets), strings.Join(summaries, " | "))
	return aggregate, nil
}

// rootDeadline returns the rootCtx's deadline, or "now + 30s" as a
// safe fallback if the ctx has no deadline (shouldn't happen in
// chunked mode where we always set one, but defensive).
func rootDeadline(ctx context.Context) time.Time {
	if dl, ok := ctx.Deadline(); ok {
		return dl
	}
	return time.Now().Add(30 * time.Second)
}

// enumerateBuckets walks bucketRoots under repoRoot and returns
// repo-relative paths of directories (at depth <= bucketScanMaxDepth)
// that carry one of bucketManifests. Results are sorted for
// deterministic dispatch ordering.
func enumerateBuckets(repoRoot string) []string {
	if strings.TrimSpace(repoRoot) == "" {
		return nil
	}
	rootAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var buckets []string

	for _, top := range bucketRoots {
		topAbs := filepath.Join(rootAbs, top)
		info, err := os.Stat(topAbs)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(topAbs, func(p string, d os.DirEntry, werr error) error {
			if werr != nil {
				return nil
			}
			// Compute depth under topAbs.
			rel, rerr := filepath.Rel(topAbs, p)
			if rerr != nil {
				return nil
			}
			depth := 0
			if rel != "." {
				depth = strings.Count(rel, string(filepath.Separator)) + 1
			}
			if d.IsDir() {
				base := d.Name()
				if base == ".git" || base == "node_modules" || base == "target" || base == "dist" || base == ".next" || base == "vendor" || base == "build" {
					return filepath.SkipDir
				}
				if depth > bucketScanMaxDepth {
					return filepath.SkipDir
				}
				return nil
			}
			// File — is it a manifest?
			for _, mf := range bucketManifests {
				if d.Name() == mf {
					bucketAbs := filepath.Dir(p)
					bucketRel, relErr := filepath.Rel(rootAbs, bucketAbs)
					if relErr != nil {
						return nil
					}
					if !seen[bucketRel] {
						seen[bucketRel] = true
						buckets = append(buckets, bucketRel)
					}
					return nil
				}
			}
			return nil
		})
	}

	sort.Strings(buckets)
	return buckets
}

// isTimeoutLikely detects whether a review attempt hit its budget
// ceiling rather than emitting a real verdict. Two signals:
//   - the attempt's ctx is DeadlineExceeded
//   - the report exists but the summary matches the "halted" /
//     "turn cap" / "budget" sentinels the reviewer emits when it
//     couldn't converge
func isTimeoutLikely(report *IntegrationReport, ctxErr error) bool {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return true
	}
	if report == nil {
		return false
	}
	s := strings.ToLower(report.Summary)
	if strings.Contains(s, "turn cap") || strings.Contains(s, "halted") || strings.Contains(s, "out of budget") || strings.Contains(s, "timed out") {
		return true
	}
	return false
}
