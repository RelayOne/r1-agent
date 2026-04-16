package chat

import "testing"

func TestShiftDetector_StableTopicNoFire(t *testing.T) {
	d := NewShiftDetector()
	turns := []string{
		"help me write a rust program for file parsing",
		"how do I read a file in rust?",
		"what's the best rust crate for argument parsing?",
		"how do rust closures work?",
		"can you explain rust traits?",
	}
	fired := false
	for _, turn := range turns {
		if d.Observe(turn) {
			fired = true
		}
	}
	if fired {
		t.Error("stable rust-related turns should not fire")
	}
}

func TestShiftDetector_AbruptShiftFires(t *testing.T) {
	d := NewShiftDetector()
	d.SetThreshold(0.3) // more sensitive for test
	d.SetStreak(2)
	rustTurns := []string{
		"help me write a rust program for file parsing",
		"how do I read a file in rust?",
		"what's the best rust crate for argument parsing?",
	}
	for _, turn := range rustTurns {
		_ = d.Observe(turn)
	}
	// Shift: now asking about cooking. The keyword vector
	// shares no tokens with rust programming.
	fired := false
	for _, turn := range []string{
		"how do I bake sourdough bread at home?",
		"what flour should I use for croissants?",
	} {
		if d.Observe(turn) {
			fired = true
		}
	}
	if !fired {
		t.Error("cooking-after-rust should fire a shift signal")
	}
}

func TestShiftDetector_FiresOnceUntilReset(t *testing.T) {
	d := NewShiftDetector()
	d.SetThreshold(0.5)
	d.SetStreak(1)
	_ = d.Observe("apples and oranges")
	_ = d.Observe("apples and oranges again")
	// Abrupt shift:
	if !d.Observe("submarines underwater") {
		t.Fatal("expected shift fire")
	}
	// Same shift continues — should NOT re-fire.
	if d.Observe("torpedo systems in navy") {
		t.Error("detector should not re-fire without Reset")
	}
	// Reset + another shift fires again.
	d.Reset()
	_ = d.Observe("garden vegetables growing")
	if d.Shifted() {
		t.Error("after Reset, Shifted should be false until another streak completes")
	}
}

func TestShiftDetector_ResetClears(t *testing.T) {
	d := NewShiftDetector()
	d.SetThreshold(0.5)
	d.SetStreak(1)
	_ = d.Observe("apples")
	_ = d.Observe("submarines")
	if !d.Shifted() {
		t.Fatal("expected shifted")
	}
	d.Reset()
	if d.Shifted() {
		t.Error("Shifted should be false after Reset")
	}
}

func TestTokenize_StopwordsFiltered(t *testing.T) {
	got := tokenize("the quick brown fox is on the mat")
	for _, s := range got {
		if stopWords[s] {
			t.Errorf("stopword %q leaked through", s)
		}
	}
}

func TestCosineSimilarity_SameVectorIsOne(t *testing.T) {
	a := vectorize("apple banana cherry")
	s := cosineSimilarity(a, a)
	if s < 0.99 {
		t.Errorf("same vector similarity=%v want ~1", s)
	}
}

func TestCosineSimilarity_DisjointIsZero(t *testing.T) {
	a := vectorize("apple banana")
	b := vectorize("submarine torpedo")
	s := cosineSimilarity(a, b)
	if s != 0 {
		t.Errorf("disjoint vectors similarity=%v want 0", s)
	}
}
