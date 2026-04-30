package convergence

import "testing"

func TestExtractCitations_AllKinds(t *testing.T) {
	report := `Shipped the OrderProcessor at apps/api/src/orders.ts:42.
Commit: abc1234. See PR #255 and https://github.com/RelayOne/actium/pull/255.
curl -sI /health → HTTP/2 200.
Live at actium-portal-00162-zcd.`

	cs := ExtractCitations(report)
	if len(cs) < 6 {
		t.Fatalf("expected >=6 citations, got %d: %+v", len(cs), cs)
	}
	kinds := map[EvidenceKind]int{}
	for _, c := range cs {
		kinds[c.Kind]++
	}
	for _, k := range []EvidenceKind{EvidenceFileLine, EvidencePR, EvidenceGHURL, EvidenceCurlProbe, EvidenceCloudRunRev} {
		if kinds[k] == 0 {
			t.Errorf("missing kind %s in %+v", k, kinds)
		}
	}
}

func TestCheckFileLineRequired_ClaimWithoutEvidenceFlagged(t *testing.T) {
	report := "Shipped the user signup flow. Done. Production-ready and live."
	got := CheckFileLineRequired(report)
	if len(got) == 0 {
		t.Fatalf("expected at least 1 finding for evidence-less claim")
	}
}

func TestCheckFileLineRequired_ClaimWithEvidenceAccepted(t *testing.T) {
	report := "Shipped the user signup flow at apps/web/src/signup.tsx:18. PR #200 merged."
	got := CheckFileLineRequired(report)
	if len(got) != 0 {
		t.Fatalf("expected 0 findings, got %+v", got)
	}
}

func TestCheckFileLineRequired_BlockedNotFlagged(t *testing.T) {
	report := "BLOCKED-rate-limit. The signup flow could not be shipped because the auth pool returned 429."
	got := CheckFileLineRequired(report)
	if len(got) != 0 {
		t.Fatalf("BLOCKED reports should not be flagged, got %+v", got)
	}
}

func TestCheckFileLineRequired_NeighborSentenceCounts(t *testing.T) {
	report := "Shipped X. See packages/agents/src/agents/email-reply-drafter-agent.ts:1 for details."
	got := CheckFileLineRequired(report)
	if len(got) != 0 {
		t.Fatalf("evidence in next sentence should count; got %+v", got)
	}
}

func TestCheckFileLineRequired_W29Regression(t *testing.T) {
	// The exact pattern that bit us: claim 16 shipped, no proof.
	report := "DONE: 16 xlsx workforce handlers shipped and wired into the registry. All 16 visible in the portal at /admin/ai-workforce."
	got := CheckFileLineRequired(report)
	if len(got) == 0 {
		t.Fatalf("W29 pattern should be flagged — got 0 findings")
	}
}

func TestCommitRegexMatchesBacktickAndPrefix(t *testing.T) {
	r1 := "merged in commit abc1234"
	r2 := "merged at `def5678`"
	cs1 := ExtractCitations(r1)
	cs2 := ExtractCitations(r2)
	if len(cs1) == 0 || cs1[0].Kind != EvidenceCommit {
		t.Errorf("expected commit citation in %q, got %+v", r1, cs1)
	}
	if len(cs2) == 0 || cs2[0].Kind != EvidenceCommit {
		t.Errorf("expected commit citation in %q, got %+v", r2, cs2)
	}
}
