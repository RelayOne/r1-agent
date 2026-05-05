package antitrunc

import (
	"strings"
	"testing"
)

// TestPhraseCatalog asserts every catalog entry has a non-empty ID
// and a compiled regex. Catches drift if someone adds a pattern but
// forgets the ID or the constructor.
func TestPhraseCatalog(t *testing.T) {
	for _, p := range TruncationPhrases {
		if p.ID == "" {
			t.Errorf("truncation pattern missing ID: %v", p.Regex)
		}
		if p.Regex == nil {
			t.Errorf("truncation pattern %q missing regex", p.ID)
		}
	}
	for _, p := range FalseCompletionPhrases {
		if p.ID == "" {
			t.Errorf("false-completion pattern missing ID: %v", p.Regex)
		}
		if p.Regex == nil {
			t.Errorf("false-completion pattern %q missing regex", p.ID)
		}
	}
	if len(TruncationPhrases) < 12 {
		t.Errorf("expected at least 12 truncation patterns, got %d", len(TruncationPhrases))
	}
	if len(FalseCompletionPhrases) < 2 {
		t.Errorf("expected at least 2 false-completion patterns, got %d", len(FalseCompletionPhrases))
	}
}

// TestPhraseIDs ensures the public-API helper returns every ID from
// both catalogs.
func TestPhraseIDs(t *testing.T) {
	got := PhraseIDs()
	want := len(TruncationPhrases) + len(FalseCompletionPhrases)
	if len(got) != want {
		t.Fatalf("PhraseIDs len = %d, want %d", len(got), want)
	}
	seen := map[string]bool{}
	for _, id := range got {
		if id == "" {
			t.Error("PhraseIDs returned empty ID")
		}
		if seen[id] {
			t.Errorf("PhraseIDs returned duplicate %q", id)
		}
		seen[id] = true
	}
}

// TestMatchAll covers every documented pattern with at least one
// positive case and every catalog with at least one negative case.
// Each row is one phrase or one negative; the input is the assistant
// or commit text, and wantPhraseIDs is the IDs expected in the result
// (any-order).
func TestMatchAll(t *testing.T) {
	cases := []struct {
		name           string
		text           string
		wantPhraseIDs  []string // any-order, all must be present
		wantNoMatches  bool     // when true, MatchAll must be empty
	}{
		// --- positives, one per truncation phrase ---
		{
			name:          "premature_stop_let_me_lower",
			text:          "i'll stop here and pick up later",
			wantPhraseIDs: []string{"premature_stop_let_me"},
		},
		{
			name:          "premature_stop_let_me_titlecase",
			text:          "Let me pause for now",
			wantPhraseIDs: []string{"premature_stop_let_me"},
		},
		{
			name:          "premature_stop_should_defer",
			text:          "I should defer the rest of this",
			wantPhraseIDs: []string{"premature_stop_let_me"},
		},
		{
			name:          "scope_kept_manageable_keep_scope_tight",
			text:          "to keep scope tight, I'm focusing on layer 1",
			wantPhraseIDs: []string{"scope_kept_manageable"},
		},
		{
			name:          "scope_kept_manageable_to_keep_things_focused",
			text:          "to keep things focused, here's the plan",
			wantPhraseIDs: []string{"scope_kept_manageable"},
		},
		{
			name:          "budget_running_out_token_budget",
			text:          "the token budget is running out so I'll skip layer 5",
			wantPhraseIDs: []string{"budget_running_out", "premature_stop_let_me"},
		},
		{
			name:          "budget_running_out_rate_limit_approaching",
			text:          "rate-limit window approaching; conserve compute",
			wantPhraseIDs: []string{"budget_running_out"},
		},
		{
			name:          "handoff_to_next_session_handoff",
			text:          "Handoff document is ready",
			wantPhraseIDs: []string{"handoff_to_next_session"},
		},
		{
			name:          "handoff_to_next_session_followup_session",
			text:          "this will be a follow-up session task",
			wantPhraseIDs: []string{"handoff_to_next_session"},
		},
		{
			name:          "false_completion_foundation_done",
			text:          "Foundation done; merging now.",
			wantPhraseIDs: []string{"false_completion_foundation"},
		},
		{
			name:          "false_completion_core_shipped",
			text:          "core shipped, deferring rest",
			wantPhraseIDs: []string{"false_completion_foundation"},
		},
		{
			name:          "false_completion_skeleton_complete",
			text:          "Skeleton complete; ready for review.",
			wantPhraseIDs: []string{"false_completion_foundation"},
		},
		{
			name:          "false_completion_good_enough",
			text:          "this is good enough to merge",
			wantPhraseIDs: []string{"false_completion_good_enough"},
		},
		{
			name:          "false_completion_ready_to_merge",
			text:          "the branch is ready to merge",
			wantPhraseIDs: []string{"false_completion_good_enough"},
		},
		{
			name:          "we_should_stop",
			text:          "we should stop here",
			wantPhraseIDs: []string{"we_should_stop"},
		},
		{
			name:          "we_can_wrap_up",
			text:          "we can wrap up the rest later",
			wantPhraseIDs: []string{"we_should_stop"},
		},
		{
			name:          "lets_punt",
			text:          "let's punt this to next time",
			wantPhraseIDs: []string{"we_should_stop"},
		},
		{
			name:          "out_of_scope_for_now",
			text:          "marking that out of scope for now",
			wantPhraseIDs: []string{"out_of_scope_for_now"},
		},
		{
			name:          "stretch_goal_today",
			text:          "stretch goal today; not blocking",
			wantPhraseIDs: []string{"out_of_scope_for_now"},
		},
		{
			name:          "deferring_to_followup",
			text:          "deferring to follow-up; logged in plans/",
			wantPhraseIDs: []string{"deferring_to_followup"},
		},
		{
			name:          "will_come_later",
			text:          "the rest will come later in a follow-up",
			wantPhraseIDs: []string{"deferring_to_followup"},
		},
		{
			name:          "classify_as_skip_pre_existing",
			text:          "Classifying as pre-existing failure",
			wantPhraseIDs: []string{"classify_as_skip"},
		},
		{
			name:          "classify_as_skip_user_skipped",
			text:          "classified as user-skipped per harness",
			wantPhraseIDs: []string{"classify_as_skip"},
		},
		{
			name:          "anthropic_load_balance_fiction_load_balance",
			text:          "due to Anthropic's load balance limit, i'll stop here",
			wantPhraseIDs: []string{"anthropic_load_balance_fiction", "premature_stop_let_me"},
		},
		{
			name:          "anthropic_load_balance_fiction_provider_rate",
			text:          "provider rate limit hit again",
			wantPhraseIDs: []string{"anthropic_load_balance_fiction"},
		},
		{
			name:          "respect_provider_capacity",
			text:          "to respect Anthropic capacity I'll cut scope",
			wantPhraseIDs: []string{"respect_provider_capacity"},
		},
		{
			name:          "stay_within_provider_budget",
			text:          "to stay within provider budget, deferring",
			wantPhraseIDs: []string{"respect_provider_capacity"},
		},

		// --- false-completion catalog ---
		{
			name:          "spec_done_lower",
			text:          "spec 9 done — merging",
			wantPhraseIDs: []string{"false_completion_spec_done"},
		},
		{
			name:          "spec_complete_titlecase",
			text:          "Spec 12 Complete",
			wantPhraseIDs: []string{"false_completion_spec_done"},
		},
		{
			name:          "all_tasks_done",
			text:          "all tasks done. closing out.",
			wantPhraseIDs: []string{"false_completion_all_tasks_done"},
		},
		{
			name:          "all_items_finished",
			text:          "All items finished today",
			wantPhraseIDs: []string{"false_completion_all_tasks_done"},
		},

		// --- negatives: legitimate uses that must NOT match ---
		{
			name:          "negative_stop_the_build",
			text:          "you can stop the build with ctrl-c",
			wantNoMatches: true,
		},
		{
			name:          "negative_rate_limiter",
			text:          "the rate limiter accepts 5 req/sec",
			wantNoMatches: true,
		},
		{
			name:          "negative_token_count_estimation",
			text:          "tokenest estimates the token count without an external API",
			wantNoMatches: true,
		},
		{
			name:          "negative_followup_email",
			text:          "the email mentioned a follow up meeting",
			wantNoMatches: true,
		},
		{
			name:          "negative_clean_text",
			text:          "the build is green and tests pass",
			wantNoMatches: true,
		},
		{
			name:          "negative_provider_capacity_unrelated",
			text:          "the cloud provider has 99.99% capacity",
			wantNoMatches: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchAll(tc.text)
			if tc.wantNoMatches {
				if len(got) != 0 {
					var ids []string
					for _, m := range got {
						ids = append(ids, m.PhraseID)
					}
					t.Errorf("expected zero matches, got %d (%s)", len(got), strings.Join(ids, ","))
				}
				return
			}
			seen := map[string]bool{}
			for _, m := range got {
				seen[m.PhraseID] = true
			}
			for _, want := range tc.wantPhraseIDs {
				if !seen[want] {
					var ids []string
					for _, m := range got {
						ids = append(ids, m.PhraseID)
					}
					t.Errorf("missing phrase %q; got %v", want, ids)
				}
			}
		})
	}
}

// TestMatchAll_PreservesOffsetsAndSnippet verifies the Match struct
// carries usable offsets and a non-empty snippet for audit log lines.
func TestMatchAll_PreservesOffsetsAndSnippet(t *testing.T) {
	text := "okay, i'll stop here for the day"
	got := MatchAll(text)
	if len(got) == 0 {
		t.Fatal("expected at least one match")
	}
	m := got[0]
	if m.Snippet == "" {
		t.Error("snippet must not be empty")
	}
	if m.Start < 0 || m.End <= m.Start || m.End > len(text) {
		t.Errorf("invalid offsets: [%d,%d] for %q", m.Start, m.End, text)
	}
	if text[m.Start:m.End] != m.Snippet {
		t.Errorf("snippet %q != text[%d:%d] %q", m.Snippet, m.Start, m.End, text[m.Start:m.End])
	}
	if m.Catalog != "truncation" {
		t.Errorf("catalog = %q, want \"truncation\"", m.Catalog)
	}
}

// TestMatchTruncation_FalseCompletion_ScopedCatalog verifies that
// the per-catalog helpers don't leak hits across catalogs.
func TestMatchTruncation_FalseCompletion_ScopedCatalog(t *testing.T) {
	// Pure truncation phrase.
	tr := MatchTruncation("i'll stop here")
	if len(tr) == 0 {
		t.Error("expected truncation hit")
	}
	for _, m := range tr {
		if m.Catalog != "truncation" {
			t.Errorf("catalog = %q, want truncation", m.Catalog)
		}
	}
	// Pure false-completion phrase.
	fc := MatchFalseCompletion("spec 1 done")
	if len(fc) == 0 {
		t.Error("expected false-completion hit")
	}
	for _, m := range fc {
		if m.Catalog != "false_completion" {
			t.Errorf("catalog = %q, want false_completion", m.Catalog)
		}
	}
	// Truncation phrase yields zero false-completion hits.
	if len(MatchFalseCompletion("i'll stop here")) != 0 {
		t.Error("truncation phrase leaked into false-completion catalog")
	}
}

// TestMatchAll_LongSnippetCapped guards the 200-char snippet cap so
// audit logs don't blow up on long matched runs.
func TestMatchAll_LongSnippetCapped(t *testing.T) {
	// Construct text whose match could be up to N chars; the cap
	// is enforced inside matchAll. This regex (budget_running_out)
	// uses .* which matches lots of chars so the snippet can grow.
	text := "rate-limit window approaching " + strings.Repeat("x", 500) + " conserve compute"
	got := MatchAll(text)
	for _, m := range got {
		if len(m.Snippet) > 200 {
			t.Errorf("snippet exceeds 200 chars: %d", len(m.Snippet))
		}
	}
}
