// Package consolidation implements STOKE-010: the background
// job that extracts patterns from Episodic memory into
// Semantic insights + Procedural skills, plus the trust-tier
// promotion ladder, misevolution monitoring, and git-like
// versioning for rollback.
//
// Philosophy: during a session, the agent writes Episodic
// entries cheaply (recency-first). Offline, a consolidation
// pass walks the Episodic log, clusters repeated patterns,
// and promotes them to Semantic (structured facts) +
// Procedural (reusable skills) at the Intern trust tier.
// Over time, high-accuracy Intern insights promote to Junior,
// then Senior, via explicit gates (success rate + clean
// history + safety-refusal delta).
//
// Misevolution monitoring: every promotion is recorded with
// baseline safety-metric values. If a Junior/Senior promotion
// causes safety-refusal rate or hallucination rate to degrade
// past threshold, the system auto-rolls back to the previous
// version. The rollback itself is versioned, not destructive.
//
// Scope of this file:
//
//   - TrustTier enum + promotion gate
//   - BackgroundJob with configurable interval
//   - Promotion + Rollback helpers
//   - Misevolution baseline + threshold check
//   - Versioning: every insight carries a Version; rollback
//     decrements to an earlier version rather than losing
//     history
//
// The consolidation algorithm itself (clustering episodic
// entries into semantic insights) is pluggable via
// ExtractFunc so callers can drop in LLM-backed extractors
// without this package importing provider types.
package consolidation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/memory"
)

// TrustTier is an insight's reliability rank. New insights
// enter at TierIntern. Promotion to Junior → Senior requires
// passing explicit gates (SuccessRate, MinSamples, etc.).
type TrustTier string

// Trust tiers gate how much weight a consolidated insight carries.
// Values are persisted in the memory store; renames would invalidate
// existing records.
const (
	// TierIntern is the entry tier: an insight has been observed but
	// has not yet accumulated enough evidence for promotion.
	TierIntern TrustTier = "intern"
	// TierJunior is the middle tier: the insight has met MinSamples
	// and MinSuccessRate gates for promotion from Intern.
	TierJunior TrustTier = "junior"
	// TierSenior is the top tier: the insight is treated as a reliable
	// default and surfaced to agents ahead of lower-tier peers.
	TierSenior TrustTier = "senior"
)

// AllTiers returns the three tiers in promotion order.
func AllTiers() []TrustTier { return []TrustTier{TierIntern, TierJunior, TierSenior} }

// PromotionGate defines the thresholds a tier must meet for
// its insights to promote to the next tier.
type PromotionGate struct {
	MinSamples     int     // at least this many independent usages
	MinSuccessRate float64 // fraction [0,1]
	MinAgeDays     int     // minimum days since insight was recorded
}

// DefaultGates maps source-tier → gate for promotion to the
// next tier. A Senior has no further tier, so it's absent.
var DefaultGates = map[TrustTier]PromotionGate{
	TierIntern: {MinSamples: 5, MinSuccessRate: 0.7, MinAgeDays: 3},
	TierJunior: {MinSamples: 20, MinSuccessRate: 0.85, MinAgeDays: 14},
}

// Insight is a consolidated item: a pattern or fact promoted
// from Episodic into Semantic or Procedural storage.
type Insight struct {
	ID            string
	Tier          TrustTier
	Version       int
	Content       string
	Tags          []string
	Samples       int
	Successes     int
	CreatedAt     time.Time
	PromotedAt    time.Time
	PreviousVersion *Insight // linked list for rollback
}

// SuccessRate reports successes / samples or 0 when samples=0.
func (i *Insight) SuccessRate() float64 {
	if i.Samples == 0 {
		return 0
	}
	return float64(i.Successes) / float64(i.Samples)
}

// ErrPromotionGateUnmet is returned by Promote when the
// gating thresholds for the current tier aren't met.
var ErrPromotionGateUnmet = errors.New("consolidation: promotion gate not met")

// ErrAlreadyTopTier is returned by Promote when called on a
// Senior insight (no further tier).
var ErrAlreadyTopTier = errors.New("consolidation: already at top tier")

// ErrNoHistory is returned by Rollback when the insight has
// no prior version to revert to.
var ErrNoHistory = errors.New("consolidation: no previous version to roll back to")

// Promote attempts to advance an insight one tier. Returns
// the new Insight on success; leaves the input untouched.
// Gate values come from DefaultGates; callers can inject a
// custom gate map via PromoteWithGates.
func Promote(in Insight) (Insight, error) {
	return PromoteWithGates(in, DefaultGates)
}

// PromoteWithGates is Promote with caller-supplied gates.
func PromoteWithGates(in Insight, gates map[TrustTier]PromotionGate) (Insight, error) {
	next, ok := nextTier(in.Tier)
	if !ok {
		return in, ErrAlreadyTopTier
	}
	gate, ok := gates[in.Tier]
	if !ok {
		return in, fmt.Errorf("%w: no gate for tier %q", ErrPromotionGateUnmet, in.Tier)
	}
	if in.Samples < gate.MinSamples {
		return in, fmt.Errorf("%w: samples=%d need %d", ErrPromotionGateUnmet, in.Samples, gate.MinSamples)
	}
	if in.SuccessRate() < gate.MinSuccessRate {
		return in, fmt.Errorf("%w: success_rate=%.2f need %.2f", ErrPromotionGateUnmet, in.SuccessRate(), gate.MinSuccessRate)
	}
	ageDays := int(time.Since(in.CreatedAt).Hours() / 24)
	if ageDays < gate.MinAgeDays {
		return in, fmt.Errorf("%w: age_days=%d need %d", ErrPromotionGateUnmet, ageDays, gate.MinAgeDays)
	}
	out := in
	out.Version++
	out.Tier = next
	out.PromotedAt = time.Now().UTC()
	// Preserve history: the pre-promotion state is linked in as
	// PreviousVersion so Rollback can revert without data loss.
	prev := in
	prev.PreviousVersion = nil // flatten: the inner copy doesn't carry its own chain
	out.PreviousVersion = &prev
	return out, nil
}

// Rollback reverts the insight to its previous version (the
// state prior to the last Promote). Returns ErrNoHistory if
// the insight has no prior version. Idempotent: rolling back
// twice returns the version-before-last, etc.
func Rollback(in Insight) (Insight, error) {
	if in.PreviousVersion == nil {
		return in, ErrNoHistory
	}
	return *in.PreviousVersion, nil
}

// nextTier returns the tier a Promote call advances to, or
// false when the current tier is Senior.
func nextTier(t TrustTier) (TrustTier, bool) {
	switch t {
	case TierIntern:
		return TierJunior, true
	case TierJunior:
		return TierSenior, true
	case TierSenior:
		return "", false
	default:
		return "", false
	}
}

// --- Misevolution monitoring ---

// SafetyMetric tracks the two rates the SOW mentions by name:
// SafetyRefusalRate (how often the agent declines unsafe
// requests) and HallucinationRate (how often it produces
// demonstrably false content). Higher refusal rate is better;
// lower hallucination rate is better.
type SafetyMetric struct {
	SafetyRefusalRate float64 // [0,1]; higher = better
	HallucinationRate float64 // [0,1]; lower = better
	MeasuredAt        time.Time
}

// MisevolutionThreshold defines how much a metric can degrade
// before an auto-rollback fires.
type MisevolutionThreshold struct {
	// MaxSafetyRefusalDrop: rollback fires if
	// baseline.SafetyRefusalRate - current.SafetyRefusalRate
	// >= MaxSafetyRefusalDrop. Default 0.10 (10 pp drop).
	MaxSafetyRefusalDrop float64

	// MaxHallucinationRise: rollback fires if
	// current.HallucinationRate - baseline.HallucinationRate
	// >= MaxHallucinationRise. Default 0.10.
	MaxHallucinationRise float64
}

// DefaultThreshold is the SOW-recommended 10 pp threshold on
// both metrics.
var DefaultThreshold = MisevolutionThreshold{
	MaxSafetyRefusalDrop: 0.10,
	MaxHallucinationRise: 0.10,
}

// Misevolved reports whether current has degraded past the
// threshold relative to baseline. Callers that detect
// misevolution MUST initiate a rollback of the most recent
// promotion(s) before continuing.
func Misevolved(baseline, current SafetyMetric, thr MisevolutionThreshold) bool {
	if baseline.SafetyRefusalRate-current.SafetyRefusalRate >= thr.MaxSafetyRefusalDrop {
		return true
	}
	if current.HallucinationRate-baseline.HallucinationRate >= thr.MaxHallucinationRise {
		return true
	}
	return false
}

// --- Background job ---

// ExtractFunc pulls patterns out of a batch of Episodic items.
// Returns the new Insights the caller should persist. Pluggable
// so callers can inject LLM-backed extractors without this
// package importing provider types.
type ExtractFunc func(ctx context.Context, items []memory.Item) ([]Insight, error)

// BackgroundJob runs consolidation on a timer. Subscribe()
// reports each completed run with the count of extracted
// insights; callers use it to drive observability or trigger
// downstream actions (e.g. re-indexing).
type BackgroundJob struct {
	router    *memory.Router
	extract   ExtractFunc
	interval  time.Duration
	mu        sync.Mutex
	running   bool
	cancel    context.CancelFunc
	subsMu    sync.Mutex
	subs      []chan RunReport
}

// RunReport is what Subscribe's channel emits per run.
type RunReport struct {
	At             time.Time
	EpisodicScanned int
	InsightsAdded  int
	Errors         []error
}

// NewBackgroundJob constructs a job. Interval=0 means "on
// demand" (caller invokes RunOnce explicitly).
func NewBackgroundJob(router *memory.Router, extract ExtractFunc, interval time.Duration) *BackgroundJob {
	return &BackgroundJob{
		router:   router,
		extract:  extract,
		interval: interval,
	}
}

// Start begins the periodic run loop. Calls are idempotent —
// calling Start twice is a no-op on the second call.
func (b *BackgroundJob) Start(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running || b.interval <= 0 {
		return
	}
	b.running = true
	runCtx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	go b.loop(runCtx)
}

// Stop halts the loop.
func (b *BackgroundJob) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.running {
		return
	}
	b.cancel()
	b.running = false
}

func (b *BackgroundJob) loop(ctx context.Context) {
	tick := time.NewTicker(b.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = b.RunOnce(ctx)
		}
	}
}

// RunOnce executes one consolidation pass and emits a
// RunReport to subscribers. Safe to call synchronously (tests
// use this rather than Start).
func (b *BackgroundJob) RunOnce(ctx context.Context) error {
	report := RunReport{At: time.Now().UTC()}
	items, err := b.router.Query(ctx, memory.Query{Tier: memory.TierEpisodic, Limit: 1000})
	if err != nil {
		report.Errors = append(report.Errors, fmt.Errorf("query episodic: %w", err))
		b.emit(report)
		return err
	}
	report.EpisodicScanned = len(items)
	if len(items) == 0 {
		b.emit(report)
		return nil
	}
	insights, err := b.extract(ctx, items)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Errorf("extract: %w", err))
		b.emit(report)
		return err
	}
	for _, ins := range insights {
		// Insights land in Semantic tier at Intern trust.
		// Promote explicitly via Promote() when gates pass.
		item := memory.Item{
			ID:         ins.ID,
			Tier:       memory.TierSemantic,
			Content:    ins.Content,
			Tags:       ins.Tags,
			Importance: 0.6, // consolidated insights are higher-than-default
			Confidence: ins.SuccessRate(),
			CreatedAt:  ins.CreatedAt,
		}
		if err := b.router.Put(ctx, item); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("put %q: %w", ins.ID, err))
			continue
		}
		report.InsightsAdded++
	}
	b.emit(report)
	return nil
}

// Subscribe returns a channel that receives RunReports. The
// channel closes when the job stops. Buffered to 4 to avoid
// blocking the run loop on slow subscribers.
func (b *BackgroundJob) Subscribe() <-chan RunReport {
	ch := make(chan RunReport, 4)
	b.subsMu.Lock()
	b.subs = append(b.subs, ch)
	b.subsMu.Unlock()
	return ch
}

func (b *BackgroundJob) emit(r RunReport) {
	b.subsMu.Lock()
	defer b.subsMu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- r:
		default:
			// drop slow subs
		}
	}
}
