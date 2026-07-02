package wisdom

import (
	"strings"
	"testing"
	"time"
)

// TestFindSimilarRanksBySemanticCloseness covers SOTA gap #2: a learning
// whose text is semantically close to the query ranks above an unrelated
// one, even without an exact fingerprint match.
func TestFindSimilarRanksBySemanticCloseness(t *testing.T) {
	learnings := []Learning{
		{Description: "The auth middleware panicked on a nil session token; guard the token before dereferencing."},
		{Description: "Rename the CSS grid class to avoid a collision with the flex layout."},
		{Description: "A missing import for the json package broke the build; add encoding/json."},
	}
	got := FindSimilar(learnings, "nil pointer dereference in the authentication session token handler", 2)
	if len(got) == 0 {
		t.Fatal("FindSimilar returned nothing for a clearly-related query")
	}
	if !strings.Contains(got[0].Description, "auth middleware") {
		t.Errorf("top match = %q, want the auth/session learning first", got[0].Description)
	}
}

// TestFindBySimilarAcrossStores proves the method works on both store
// implementations and that recall survives a store round-trip (the whole
// point of persistence: a learning recorded in one run is recalled in the
// next).
func TestFindBySimilarAcrossStores(t *testing.T) {
	s := NewStore()
	s.Record("t1", Learning{Category: Gotcha, Description: "database connection pool exhausted under concurrent writes; cap the pool"})
	s.Record("t1", Learning{Category: Gotcha, Description: "typo in the readme heading"})

	got := s.FindBySimilar("connection pool ran out of connections during parallel writes", 1)
	if len(got) != 1 || !strings.Contains(got[0].Description, "connection pool") {
		t.Fatalf("Store.FindBySimilar = %+v, want the pool learning", got)
	}
}

// TestFindSimilarSkipsExpired ensures stale advice does not resurface.
func TestFindSimilarSkipsExpired(t *testing.T) {
	past := time.Now().Add(-24 * time.Hour)
	learnings := []Learning{
		{Description: "expired advice about the auth token", ValidUntil: &past},
		{Description: "current advice about the auth token guard"},
	}
	got := FindSimilar(learnings, "auth token", 5)
	for _, l := range got {
		if strings.Contains(l.Description, "expired") {
			t.Errorf("expired learning resurfaced: %q", l.Description)
		}
	}
}
