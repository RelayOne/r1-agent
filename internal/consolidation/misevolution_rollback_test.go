package consolidation

import (
	"errors"
	"testing"
	"time"
)

func mature(tier TrustTier) Insight {
	// Stats comfortably above every gate so Promote itself succeeds and
	// the test isolates the misevolution/rollback behavior.
	return Insight{
		ID:        "i1",
		Tier:      tier,
		Samples:   30,
		Successes: 29,
		CreatedAt: time.Now().Add(-30 * 24 * time.Hour),
	}
}

func TestPromoteChecked_CleanPromotion(t *testing.T) {
	base := SafetyMetric{SafetyRefusalRate: 0.9, HallucinationRate: 0.05}
	cur := SafetyMetric{SafetyRefusalRate: 0.91, HallucinationRate: 0.04} // improved
	out, err := PromoteChecked(mature(TierIntern), base, cur, DefaultThreshold)
	if err != nil {
		t.Fatalf("clean promotion err=%v", err)
	}
	if out.Tier != TierJunior {
		t.Errorf("tier=%q want Junior", out.Tier)
	}
}

func TestPromoteChecked_MisevolutionRollsBack(t *testing.T) {
	base := SafetyMetric{SafetyRefusalRate: 0.9, HallucinationRate: 0.05}
	bad := SafetyMetric{SafetyRefusalRate: 0.7, HallucinationRate: 0.05} // 20pp refusal drop
	out, err := PromoteChecked(mature(TierIntern), base, bad, DefaultThreshold)
	if !errors.Is(err, ErrMisevolved) {
		t.Fatalf("err=%v want ErrMisevolved", err)
	}
	// The rollback contract: the returned insight is the pre-promotion
	// version, i.e. back at Intern.
	if out.Tier != TierIntern {
		t.Errorf("reverted tier=%q want Intern (rolled back)", out.Tier)
	}
}

func TestPromoteChecked_GateFailurePassesThrough(t *testing.T) {
	base := SafetyMetric{SafetyRefusalRate: 0.9}
	cur := SafetyMetric{SafetyRefusalRate: 0.9}
	// Fresh insight fails the age gate; PromoteChecked must surface the
	// gate error unchanged, never reaching the misevolution check.
	fresh := Insight{Tier: TierIntern, Samples: 30, Successes: 30, CreatedAt: time.Now()}
	_, err := PromoteChecked(fresh, base, cur, DefaultThreshold)
	if !errors.Is(err, ErrPromotionGateUnmet) {
		t.Errorf("err=%v want ErrPromotionGateUnmet", err)
	}
}
