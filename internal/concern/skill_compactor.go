package concern

// skill_compactor.go — intended production caller for skilltracker's
// EvictByCompactor surface. NOT yet wired: no production code
// constructs NewSkillCompactor or calls EvictForBudget today, and the
// tracker it snapshots is itself only populated by the test-only
// hub/builtin.SkillInjector-with-Tracker path. See the BLOCKED-PARTIAL
// note in internal/skilltracker/tracker.go (issue #157, compactor
// caller) for the wiring status. (Audit A098: this header previously
// claimed production-caller status.)
//
// The existing internal/microcompact package compacts label-keyed
// text Sections — it doesn't know individual skills. SkillCompactor
// is a layer ABOVE that: it inspects the per-stance loaded-skill
// table maintained by skilltracker, decides which skills to drop
// under budget pressure (default policy: LRU by LoadedAt), and
// fires the matching SkillUnloaded ledger nodes.
//
// Wiring is one-way: the future caller (a skill-aware section
// shrinker or an explicit budget guard, per issue #157) calls
// SkillCompactor.EvictForBudget when it needs to free tokens by
// dropping skills. Do NOT wire EvictForBudget alone into
// internal/context or microcompact before the tracker's load side
// (SkillInjector with Tracker) is registered in production — it would
// evict from an always-empty table. This package exports the type + a
// small policy interface; it does NOT mutate any prompt content
// itself.

import (
	"context"
	"errors"
	"sort"

	"github.com/RelayOne/r1/internal/skilltracker"
)

// SkillCompactor decides which loaded skills to evict under budget
// pressure and tells the skilltracker to emit the matching
// SkillUnloaded ledger entries.
type SkillCompactor struct {
	tracker *skilltracker.Tracker
	budget  int // target tokens budget; ≤0 disables eviction
	policy  EvictionPolicy
}

// EvictionPolicy decides which skills to drop given a sorted list
// of loaded skills + the current token usage and the budget. The
// default is LRUPolicy. Plug in your own when a future spec adds
// e.g. priority-aware or cost-weighted policies.
type EvictionPolicy interface {
	Pick(loaded []skilltracker.LoadInfo, currentTokens, budget int) []skilltracker.EvictionRequest
}

// NewSkillCompactor constructs a compactor bound to the supplied
// tracker. budget ≤ 0 makes EvictForBudget a no-op (the tracker is
// inspected but never asked to evict). policy=nil defaults to LRU.
func NewSkillCompactor(t *skilltracker.Tracker, budget int, policy EvictionPolicy) *SkillCompactor {
	if policy == nil {
		policy = LRUPolicy{}
	}
	return &SkillCompactor{tracker: t, budget: budget, policy: policy}
}

// EvictForBudget asks the policy which skills should drop given
// currentTokens vs the configured budget, then fires
// Tracker.EvictByCompactor for the chosen list. Returns the list
// that was actually emitted.
//
// Idempotent: calling with currentTokens ≤ budget returns nil + nil.
// Tracker == nil is a no-op (matches the rest of the skilltracker
// surface — feature-flagged callers can keep the same call site).
func (c *SkillCompactor) EvictForBudget(ctx context.Context, stanceID string, currentTokens int) ([]skilltracker.EvictionRequest, error) {
	if c == nil || c.tracker == nil {
		return nil, nil
	}
	if c.budget <= 0 || currentTokens <= c.budget {
		return nil, nil
	}
	if stanceID == "" {
		return nil, errors.New("concern: stance_id is required")
	}
	snap := c.tracker.Snapshot()
	bucket, ok := snap[stanceID]
	if !ok || len(bucket) == 0 {
		return nil, nil
	}
	loaded := make([]skilltracker.LoadInfo, 0, len(bucket))
	for _, info := range bucket {
		loaded = append(loaded, info)
	}
	chosen := c.policy.Pick(loaded, currentTokens, c.budget)
	if len(chosen) == 0 {
		return nil, nil
	}
	if _, err := c.tracker.EvictByCompactor(ctx, stanceID, chosen); err != nil {
		return chosen, err
	}
	return chosen, nil
}

// Budget reports the configured token budget.
func (c *SkillCompactor) Budget() int {
	if c == nil {
		return 0
	}
	return c.budget
}

// LRUPolicy drops oldest-loaded skills first until currentTokens −
// SUM(dropped.Tokens) ≤ budget. Tokens=0 entries are dropped only
// when no token-cost-bearing entry remains (better-than-nothing).
type LRUPolicy struct{}

func (LRUPolicy) Pick(loaded []skilltracker.LoadInfo, currentTokens, budget int) []skilltracker.EvictionRequest {
	if currentTokens <= budget {
		return nil
	}
	sorted := make([]skilltracker.LoadInfo, len(loaded))
	copy(sorted, loaded)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].LoadedAt.Before(sorted[j].LoadedAt)
	})
	out := make([]skilltracker.EvictionRequest, 0, len(sorted))
	freed := 0
	target := currentTokens - budget
	for _, info := range sorted {
		if freed >= target {
			break
		}
		out = append(out, skilltracker.EvictionRequest{
			SkillRef:    info.SkillRef,
			TokensFreed: info.Tokens,
		})
		freed += info.Tokens
	}
	return out
}
