package memory

import (
	"context"
	"testing"
)

func TestKeywordValidator_NegationFlip(t *testing.T) {
	existing := Item{
		ID: "i1", Tier: TierSemantic, Tags: []string{"auth"},
		Content: "JWT tokens expire after 15 minutes in our system",
	}
	incoming := Item{
		ID: "i2", Tier: TierSemantic, Tags: []string{"auth"},
		Content: "JWT tokens do not expire after 15 minutes in our system",
	}
	v := KeywordValidator{}
	c, err := v.Validate(context.Background(), existing, incoming)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c == nil {
		t.Fatal("expected contradiction, got nil")
	}
	if c.Kind != KindNegationFlip {
		t.Errorf("kind=%q want negation_flip", c.Kind)
	}
}

func TestKeywordValidator_FactualDelta(t *testing.T) {
	existing := Item{
		ID: "i1", Tier: TierSemantic, Tags: []string{"capitals"},
		Content: "capital of France is Paris",
	}
	incoming := Item{
		ID: "i2", Tier: TierSemantic, Tags: []string{"capitals"},
		Content: "capital of France is Lyon",
	}
	v := KeywordValidator{}
	c, _ := v.Validate(context.Background(), existing, incoming)
	if c == nil {
		t.Fatal("expected contradiction")
	}
	if c.Kind != KindFactualDelta {
		t.Errorf("kind=%q want factual_delta", c.Kind)
	}
}

func TestKeywordValidator_ConsistentFactsNoFlag(t *testing.T) {
	existing := Item{
		ID: "i1", Tier: TierSemantic,
		Content: "capital of France is Paris",
	}
	incoming := Item{
		ID: "i2", Tier: TierSemantic,
		Content: "Paris is a European city in France",
	}
	v := KeywordValidator{}
	c, _ := v.Validate(context.Background(), existing, incoming)
	// These facts are consistent — both talk about Paris
	// being in France. Token overlap exists but no
	// negation + no tag overlap → no flag.
	if c != nil {
		t.Errorf("consistent facts flagged as %q", c.Kind)
	}
}

func TestKeywordValidator_UnrelatedNoFlag(t *testing.T) {
	existing := Item{
		ID: "i1", Tier: TierSemantic,
		Content: "Rust is a systems programming language",
	}
	incoming := Item{
		ID: "i2", Tier: TierSemantic,
		Content: "Jazz originated in New Orleans",
	}
	v := KeywordValidator{}
	c, _ := v.Validate(context.Background(), existing, incoming)
	if c != nil {
		t.Errorf("unrelated facts flagged: %+v", c)
	}
}

func TestKeywordValidator_SameContentNoFlag(t *testing.T) {
	// Identical content is a rewrite, not a contradiction.
	text := "Go uses garbage collection"
	existing := Item{ID: "i1", Tier: TierSemantic, Content: text, Tags: []string{"go"}}
	incoming := Item{ID: "i2", Tier: TierSemantic, Content: text, Tags: []string{"go"}}
	v := KeywordValidator{}
	c, _ := v.Validate(context.Background(), existing, incoming)
	if c != nil {
		t.Errorf("identical content should not flag: %+v", c)
	}
}

func TestKeywordValidator_MinSharedTokensGuard(t *testing.T) {
	// High threshold eliminates weak overlap.
	existing := Item{ID: "i1", Content: "apple one"}
	incoming := Item{ID: "i2", Content: "apple two not three"}
	v := KeywordValidator{MinSharedTokens: 5}
	c, _ := v.Validate(context.Background(), existing, incoming)
	if c != nil {
		t.Errorf("high MinSharedTokens should gate out weak matches: %+v", c)
	}
}

func TestDetectContradictions_FindsFactualDeltaInStore(t *testing.T) {
	router := NewRouter()
	router.Register(TierSemantic, NewInMemoryStorage())
	ctx := context.Background()

	existing := Item{
		ID: "i1", Tier: TierSemantic, Tags: []string{"capitals"},
		Content: "capital of France is Paris",
	}
	_ = router.Put(ctx, existing)

	incoming := Item{
		ID: "i2", Tier: TierSemantic, Tags: []string{"capitals"},
		Content: "capital of France is Lyon",
	}
	cs, err := DetectContradictions(ctx, router, TierSemantic, incoming, KeywordValidator{})
	if err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("expected 1 contradiction, got %d", len(cs))
	}
	if cs[0].Kind != KindFactualDelta {
		t.Errorf("kind=%q want factual_delta", cs[0].Kind)
	}
}

func TestDetectContradictions_SkipsSameID(t *testing.T) {
	// Don't flag a write against its own prior version.
	router := NewRouter()
	store := NewInMemoryStorage()
	router.Register(TierSemantic, store)
	ctx := context.Background()
	item := Item{ID: "i1", Tier: TierSemantic, Tags: []string{"x"}, Content: "facts"}
	_ = router.Put(ctx, item)
	cs, err := DetectContradictions(ctx, router, TierSemantic, item, KeywordValidator{})
	if err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if len(cs) != 0 {
		t.Errorf("same-ID write shouldn't self-flag: %v", cs)
	}
}

func TestHasNegation(t *testing.T) {
	positive := []string{
		"The test passes", "All green", "This is correct",
	}
	negative := []string{
		"The test does not pass", "never green", "this isn't correct",
		"the answer is wrong", "false claim",
	}
	for _, s := range positive {
		if hasNegation(s) {
			t.Errorf("hasNegation(%q)=true (false positive)", s)
		}
	}
	for _, s := range negative {
		if !hasNegation(s) {
			t.Errorf("hasNegation(%q)=false (false negative)", s)
		}
	}
}

func TestExtractContentTokens_FiltersStopwordsAndShortTokens(t *testing.T) {
	toks := extractContentTokens("The quick brown fox and the lazy dog is a pangram")
	// "The" / "and" / "the" / "is" / "a" should be dropped.
	for _, stop := range []string{"the", "and", "is", "a"} {
		if toks[stop] {
			t.Errorf("stopword %q leaked through", stop)
		}
	}
}
