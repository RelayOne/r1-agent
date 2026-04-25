package consolidation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/memory"
)

func TestPromote_InternToJunior_Succeeds(t *testing.T) {
	in := Insight{
		ID:        "i1",
		Tier:      TierIntern,
		Samples:   10,
		Successes: 8, // 80% > 70% gate
		CreatedAt: time.Now().Add(-10 * 24 * time.Hour),
	}
	out, err := Promote(in)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if out.Tier != TierJunior {
		t.Errorf("tier=%q want Junior", out.Tier)
	}
	if out.Version != 1 {
		t.Errorf("version=%d want 1", out.Version)
	}
	if out.PreviousVersion == nil {
		t.Error("PreviousVersion should be linked for rollback")
	}
}

func TestPromote_GateUnmet_Samples(t *testing.T) {
	in := Insight{Tier: TierIntern, Samples: 2, Successes: 2, CreatedAt: time.Now().Add(-10 * 24 * time.Hour)}
	_, err := Promote(in)
	if !errors.Is(err, ErrPromotionGateUnmet) {
		t.Errorf("want ErrPromotionGateUnmet, got %v", err)
	}
}

func TestPromote_GateUnmet_SuccessRate(t *testing.T) {
	in := Insight{Tier: TierIntern, Samples: 10, Successes: 5, CreatedAt: time.Now().Add(-10 * 24 * time.Hour)}
	_, err := Promote(in)
	if !errors.Is(err, ErrPromotionGateUnmet) {
		t.Errorf("want ErrPromotionGateUnmet on 50%% success, got %v", err)
	}
}

func TestPromote_GateUnmet_Age(t *testing.T) {
	// Fresh insight with good stats — age gate still blocks.
	in := Insight{Tier: TierIntern, Samples: 10, Successes: 10, CreatedAt: time.Now()}
	_, err := Promote(in)
	if !errors.Is(err, ErrPromotionGateUnmet) {
		t.Errorf("want ErrPromotionGateUnmet on fresh insight, got %v", err)
	}
}

func TestPromote_AlreadyTopTier(t *testing.T) {
	in := Insight{Tier: TierSenior}
	_, err := Promote(in)
	if !errors.Is(err, ErrAlreadyTopTier) {
		t.Errorf("want ErrAlreadyTopTier, got %v", err)
	}
}

func TestRollback_RestoresPrevious(t *testing.T) {
	in := Insight{
		ID:        "i1",
		Tier:      TierIntern,
		Samples:   20, Successes: 18,
		CreatedAt: time.Now().Add(-30 * 24 * time.Hour),
	}
	promoted, err := Promote(in)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	reverted, err := Rollback(promoted)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if reverted.Tier != TierIntern {
		t.Errorf("reverted tier=%q want Intern", reverted.Tier)
	}
}

func TestRollback_NoHistory(t *testing.T) {
	in := Insight{Tier: TierIntern}
	_, err := Rollback(in)
	if !errors.Is(err, ErrNoHistory) {
		t.Errorf("want ErrNoHistory, got %v", err)
	}
}

func TestMisevolved_DegradationFires(t *testing.T) {
	base := SafetyMetric{SafetyRefusalRate: 0.9, HallucinationRate: 0.05}
	bad := SafetyMetric{SafetyRefusalRate: 0.7, HallucinationRate: 0.05}
	if !Misevolved(base, bad, DefaultThreshold) {
		t.Error("20 pp drop in safety refusal should fire Misevolved")
	}
}

func TestMisevolved_HallucinationRiseFires(t *testing.T) {
	base := SafetyMetric{SafetyRefusalRate: 0.9, HallucinationRate: 0.05}
	bad := SafetyMetric{SafetyRefusalRate: 0.9, HallucinationRate: 0.25}
	if !Misevolved(base, bad, DefaultThreshold) {
		t.Error("20 pp rise in hallucination should fire Misevolved")
	}
}

func TestMisevolved_StableDoesNotFire(t *testing.T) {
	base := SafetyMetric{SafetyRefusalRate: 0.9, HallucinationRate: 0.05}
	cur := SafetyMetric{SafetyRefusalRate: 0.91, HallucinationRate: 0.04}
	if Misevolved(base, cur, DefaultThreshold) {
		t.Error("slight improvement shouldn't fire Misevolved")
	}
}

func TestSuccessRate_ZeroSamples(t *testing.T) {
	in := Insight{}
	if rate := in.SuccessRate(); rate != 0 {
		t.Errorf("zero-sample rate=%v want 0", rate)
	}
}

func TestBackgroundJob_RunOnce(t *testing.T) {
	router := memory.NewRouter()
	router.Register(memory.TierEpisodic, memory.NewInMemoryStorage())
	router.Register(memory.TierSemantic, memory.NewInMemoryStorage())
	ctx := context.Background()
	_ = router.Put(ctx, memory.Item{ID: "e1", Tier: memory.TierEpisodic, Content: "pattern", Tags: []string{"go"}})
	_ = router.Put(ctx, memory.Item{ID: "e2", Tier: memory.TierEpisodic, Content: "pattern", Tags: []string{"go"}})

	extract := func(_ context.Context, items []memory.Item) ([]Insight, error) {
		// Trivially consolidate: every episodic item becomes
		// one insight. Real extractors would cluster.
		out := make([]Insight, 0, len(items))
		for _, it := range items {
			out = append(out, Insight{
				ID:        "insight-" + it.ID,
				Tier:      TierIntern,
				Content:   it.Content,
				Tags:      it.Tags,
				Samples:   1, Successes: 1,
				CreatedAt: time.Now().UTC(),
			})
		}
		return out, nil
	}
	job := NewBackgroundJob(router, extract, 0)
	sub := job.Subscribe()
	if err := job.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	select {
	case r := <-sub:
		if r.EpisodicScanned != 2 {
			t.Errorf("scanned=%d want 2", r.EpisodicScanned)
		}
		if r.InsightsAdded != 2 {
			t.Errorf("added=%d want 2", r.InsightsAdded)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subscribe never received report")
	}
}

func TestBackgroundJob_ExtractErrorPropagated(t *testing.T) {
	router := memory.NewRouter()
	router.Register(memory.TierEpisodic, memory.NewInMemoryStorage())
	_ = router.Put(context.Background(), memory.Item{ID: "e1", Tier: memory.TierEpisodic, Content: "x"})

	extract := func(context.Context, []memory.Item) ([]Insight, error) {
		return nil, errors.New("boom")
	}
	job := NewBackgroundJob(router, extract, 0)
	err := job.RunOnce(context.Background())
	if err == nil {
		t.Error("extract error should propagate")
	}
}

func TestAllTiers_Three(t *testing.T) {
	if got := AllTiers(); len(got) != 3 {
		t.Errorf("AllTiers()=%d want 3", len(got))
	}
}
