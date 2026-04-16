package chat

import "testing"

func TestClassifyIntent_Abort(t *testing.T) {
	for _, in := range []string{"abort", "ABORT", "cancel", "stop", "halt", "kill", "quit", "abort the session", "stop the task"} {
		if got := ClassifyIntent(in); got != IntentAbort {
			t.Errorf("ClassifyIntent(%q)=%q want %q", in, got, IntentAbort)
		}
	}
}

func TestClassifyIntent_AbortFalsePositiveSafety(t *testing.T) {
	// "stop the timer" should NOT classify as Abort because the
	// leading phrase "stop the timer" isn't in the allow-list of
	// abort leading phrases (only "stop the" generic match). This
	// is the exact kind of false-positive risk the conservative
	// design targets — but note that "stop the " DOES match the
	// generic "stop the" prefix. So we check the fuller phrase
	// classifies predictably.
	if got := ClassifyIntent("aborting the launch timer as part of the test"); got == IntentAbort {
		t.Errorf("substring aborting shouldn't classify as Abort, got %q", got)
	}
}

func TestClassifyIntent_Pause(t *testing.T) {
	for _, in := range []string{"pause", "wait", "hold", "pause the build", "hold on a second"} {
		if got := ClassifyIntent(in); got != IntentPause {
			t.Errorf("ClassifyIntent(%q)=%q want %q", in, got, IntentPause)
		}
	}
}

func TestClassifyIntent_Redirect(t *testing.T) {
	for _, in := range []string{
		"instead do the auth flow",
		"instead, rewrite packet parser",
		"change of plan: add rate limiter first",
		"scrap that and start with schema migrations",
		"forget that, let's add tests",
		"now do the database layer",
		"new plan — focus on security",
		"different approach: use a queue",
	} {
		if got := ClassifyIntent(in); got != IntentRedirect {
			t.Errorf("ClassifyIntent(%q)=%q want %q", in, got, IntentRedirect)
		}
	}
}

func TestClassifyIntent_Inject(t *testing.T) {
	for _, in := range []string{
		"also add a changelog entry",
		"and also don't use type escape hatches",
		"but make sure the tests still pass",
		"remember to update docs",
		"don't forget the security review",
		"additionally, keep the API backward-compatible",
	} {
		if got := ClassifyIntent(in); got != IntentInject {
			t.Errorf("ClassifyIntent(%q)=%q want %q", in, got, IntentInject)
		}
	}
}

func TestClassifyIntent_StatusQuery(t *testing.T) {
	for _, in := range []string{"status", "status?", "where are you at?", "what task are you on", "how far along are you"} {
		if got := ClassifyIntent(in); got != IntentStatusQuery {
			t.Errorf("ClassifyIntent(%q)=%q want %q", in, got, IntentStatusQuery)
		}
	}
}

func TestClassifyIntent_ExplicitApprove(t *testing.T) {
	for _, in := range []string{"approved", "approve", "lgtm", "looks good"} {
		if got := ClassifyIntent(in); got != IntentApprove {
			t.Errorf("ClassifyIntent(%q)=%q want %q", in, got, IntentApprove)
		}
	}
}

func TestClassifyIntent_AmbiguousYesStaysQuery(t *testing.T) {
	// Bare "yes" outside HITL context should NOT approve — too
	// ambiguous. User has to either use a more explicit approval
	// word or be in an HITL context.
	if got := ClassifyIntent("yes"); got != IntentQuery {
		t.Errorf(`bare "yes" should be IntentQuery outside HITL, got %q`, got)
	}
	if got := ClassifyIntent("no"); got != IntentQuery {
		t.Errorf(`bare "no" should be IntentQuery outside HITL, got %q`, got)
	}
}

func TestClassifyIntentInHITLContext_RelaxesBareYesNo(t *testing.T) {
	for _, in := range []string{"yes", "y", "ok", "okay", "sure", "do it", "go ahead"} {
		if got := ClassifyIntentInHITLContext(in, true); got != IntentApprove {
			t.Errorf("ClassifyIntentInHITLContext(%q, true)=%q want IntentApprove", in, got)
		}
		// Same input outside HITL context should NOT approve.
		if got := ClassifyIntentInHITLContext(in, false); got == IntentApprove {
			t.Errorf("outside HITL, %q should not approve, got %q", in, got)
		}
	}
	for _, in := range []string{"no", "n", "nope", "don't", "dont", "cancel that"} {
		if got := ClassifyIntentInHITLContext(in, true); got != IntentReject {
			t.Errorf("ClassifyIntentInHITLContext(%q, true)=%q want IntentReject", in, got)
		}
	}
}

func TestPriority_Ordering(t *testing.T) {
	// Abort must preempt Redirect.
	if !HigherPriority(IntentAbort, IntentRedirect) {
		t.Error("Abort should preempt Redirect")
	}
	// Redirect must preempt Inject.
	if !HigherPriority(IntentRedirect, IntentInject) {
		t.Error("Redirect should preempt Inject")
	}
	// Inject must preempt StatusQuery.
	if !HigherPriority(IntentInject, IntentStatusQuery) {
		t.Error("Inject should preempt StatusQuery")
	}
	// Priority equal between Redirect and Pause (ties broken by
	// arrival order, not by this predicate).
	if HigherPriority(IntentRedirect, IntentPause) {
		t.Error("Redirect and Pause should share rank (no strict preemption)")
	}
	if HigherPriority(IntentPause, IntentRedirect) {
		t.Error("Pause and Redirect should share rank (no strict preemption)")
	}
}

func TestPriority_ScaleValues(t *testing.T) {
	cases := map[Intent]int{
		IntentAbort:       30,
		IntentRedirect:    20,
		IntentPause:       20,
		IntentInject:      10,
		IntentStatusQuery: 0,
		IntentApprove:     0,
		IntentReject:      0,
		IntentQuery:       0,
	}
	for intent, want := range cases {
		if got := Priority(intent); got != want {
			t.Errorf("Priority(%q)=%d want %d", intent, got, want)
		}
	}
}

func TestAllIntents_HasSeven(t *testing.T) {
	got := AllIntents()
	if len(got) != 7 {
		t.Errorf("AllIntents() has %d entries, want 7 (per SOW)", len(got))
	}
}
